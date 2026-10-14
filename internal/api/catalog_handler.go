package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	paginatedCatalogListCallbacks[T any] struct {
		firstPage func(context.Context, pgx.Tx, uint64) ([]T, error)
		nextPage  func(context.Context, pgx.Tx, uint64, uint64) ([]T, error)
		prevPage  func(context.Context, pgx.Tx, uint64, uint64) ([]T, error)
	}

	// catalogListHooks are the optional per-endpoint extensions to the shared
	// paginated list. All three are nil for endpoints that need none.
	catalogListHooks struct {
		// validate runs inside the read transaction before the page query, for
		// checks that need the database — tag values against the token_tag
		// vocabulary, for instance. It reports handled=true when it has already
		// written a response, matching bindCatalogAddressPagination's contract.
		validate func(pgx.Tx) (handled bool, err error)
		// extras runs inside the same transaction after the page query and
		// merges additional keys into the result envelope. It must not
		// overwrite the page's own keys.
		extras func(pgx.Tx) (map[string]any, error)
		// onSuccess runs after a successful commit, before the response is
		// written — cache headers, for instance.
		onSuccess func(*echo.Context)
	}

	offeringCursorEnvelope struct {
		store.OfferingCursor

		Scope string `json:"scope"`
	}
)

func (a *API) poolVouchersListHandler(c *echo.Context) error {
	return a.poolVouchersList(c, false)
}

func (a *API) adminPoolVouchersListHandler(c *echo.Context) error {
	return a.poolVouchersList(c, true)
}

// includeRetired is selected only by the protected admin route, never a query parameter.
func (a *API) poolVouchersList(c *echo.Context, includeRetired bool) error {
	hooks := catalogListHooks{}
	if includeRetired {
		hooks.extras = func(tx pgx.Tx) (map[string]any, error) {
			total, err := a.store.CountAdminPoolVouchers(c.Request().Context(), tx, c.Param("address"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"total": total}, nil
		}
		hooks.onSuccess = func(c *echo.Context) { c.Response().Header().Set("Cache-Control", "no-store") }
	}
	return catalogAddressListHandler(c, a, "vouchers", "Vouchers in pool %s",
		func(v store.PoolVoucher) uint64 { return v.ID },
		func(ctx context.Context, tx pgx.Tx, address string, limit uint64) ([]store.PoolVoucher, error) {
			if includeRetired {
				return a.store.ListAdminPoolVouchers(ctx, tx, address, limit)
			}
			return a.store.ListPoolVouchers(ctx, tx, address, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.PoolVoucher, error) {
			if includeRetired {
				return a.store.ListAdminPoolVouchersNext(ctx, tx, address, cursor, limit)
			}
			return a.store.ListPoolVouchersNext(ctx, tx, address, cursor, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.PoolVoucher, error) {
			if includeRetired {
				return a.store.ListAdminPoolVouchersPrevious(ctx, tx, address, cursor, limit)
			}
			return a.store.ListPoolVouchersPrevious(ctx, tx, address, cursor, limit)
		}, hooks,
	)
}

func (a *API) offeringsListHandler(c *echo.Context) error {
	search, err := parseMarketDiscoverySearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	size, err := curatedPageSize(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
		return handlePostgresError(c, err)
	}
	if handled, readErr := a.resolveNearbyCatalog(c, tx, &search); handled || readErr != nil {
		return readErr
	}
	if handled, readErr := a.resolvePoolFilter(c, tx, &search); handled || readErr != nil {
		return readErr
	}
	if handled, readErr := a.rejectUnknownTags(c, tx, search.filter.Tags); handled || readErr != nil {
		return readErr
	}

	scope := offeringCursorScope(c, search.filter)
	cursor, err := parseOfferingCursor(c.QueryParam("cursor"), scope)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	rows, err := a.store.ListOfferingsFiltered(ctx, tx, search.filter, cursor, size+1)
	if err != nil {
		return handlePostgresError(c, err)
	}
	var next *string
	if uint64(len(rows)) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		distance := 0.0
		if last.DistanceMeters != nil {
			distance = *last.DistanceMeters
		}
		token := encodeOfferingCursor(offeringCursorEnvelope{
			OfferingCursor: store.OfferingCursor{ID: last.ID, Distance: distance},
			Scope:          scope,
		})
		next = &token
	}
	result := map[string]any{"offerings": rows, "nextCursor": next}
	if search.facets && c.QueryParam("cursor") == "" {
		facets, readErr := a.store.ListOfferingTagFacets(ctx, tx, search.filter)
		if readErr != nil {
			return handlePostgresError(c, readErr)
		}
		result["tagFacets"] = facets
	}
	if err = tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	setMarketCacheHeaders(c)
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Offerings", Result: result})
}

func offeringCursorScope(c *echo.Context, filter store.CatalogFilter) string {
	caller, _, _ := getUserClaims(c)
	payload, _ := json.Marshal(struct {
		Caller   uint64
		Term     string
		Tags     []string
		TagMatch store.TagMatch
		Pools    []uint64
		Nearby   *store.CatalogLocation
	}{caller, filter.Term, filter.Tags, filter.TagMatch, filter.PoolIDs, filter.Nearby})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func parseOfferingCursor(raw, scope string) (store.OfferingCursor, error) {
	if raw == "" {
		return store.OfferingCursor{}, nil
	}
	if len(raw) > 1024 {
		return store.OfferingCursor{}, curatedValidationError("invalid offering cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return store.OfferingCursor{}, curatedValidationError("invalid offering cursor")
	}
	var envelope offeringCursorEnvelope
	if err = json.Unmarshal(data, &envelope); err != nil || envelope.ID == 0 || envelope.Scope != scope ||
		math.IsNaN(envelope.Distance) || math.IsInf(envelope.Distance, 0) || envelope.Distance < 0 {
		return store.OfferingCursor{}, curatedValidationError(
			"offering cursor does not match this search and location; restart pagination")
	}
	return envelope.OfferingCursor, nil
}

func encodeOfferingCursor(cursor offeringCursorEnvelope) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func (a *API) voucherOfferingsListHandler(c *echo.Context) error { //nolint:dupl // distinct generic instantiations of catalogAddressListHandler, not extractable further
	return catalogAddressListHandler(c, a, "offerings", "Offerings for voucher %s",
		func(o store.Offering) uint64 { return o.ID },
		func(ctx context.Context, tx pgx.Tx, address string, limit uint64) ([]store.Offering, error) {
			return a.store.ListVoucherOfferings(ctx, tx, address, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Offering, error) {
			return a.store.ListVoucherOfferingsNext(ctx, tx, address, cursor, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Offering, error) {
			return a.store.ListVoucherOfferingsPrevious(ctx, tx, address, cursor, limit)
		},
		catalogListHooks{onSuccess: setMarketCacheHeaders},
	)
}

// poolOfferingsListHandler lists every available offering published against a
// voucher the pool allows. It is public with the other pool reads; see
// poolCatalogMiddleware.
func (a *API) poolOfferingsListHandler(c *echo.Context) error { //nolint:dupl // distinct generic instantiations of catalogAddressListHandler, not extractable further
	return catalogAddressListHandler(c, a, "offerings", "Offerings in pool %s",
		func(o store.Offering) uint64 { return o.ID },
		func(ctx context.Context, tx pgx.Tx, address string, limit uint64) ([]store.Offering, error) {
			return a.store.ListPoolOfferings(ctx, tx, address, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Offering, error) {
			return a.store.ListPoolOfferingsNext(ctx, tx, address, cursor, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Offering, error) {
			return a.store.ListPoolOfferingsPrevious(ctx, tx, address, cursor, limit)
		},
		catalogListHooks{},
	)
}

// catalogAddressListHandler is the shared implementation behind every
// paginated catalog endpoint keyed by a path address (pool or voucher).
func catalogAddressListHandler[T any](
	c *echo.Context,
	a *API,
	resultKey, descriptionFmt string,
	getID func(T) uint64,
	firstPage func(context.Context, pgx.Tx, string, uint64) ([]T, error),
	nextPage func(context.Context, pgx.Tx, string, uint64, uint64) ([]T, error),
	prevPage func(context.Context, pgx.Tx, string, uint64, uint64) ([]T, error),
	hooks catalogListHooks,
) error {
	address, pagination, valid, err := bindCatalogAddressPagination(c)
	if err != nil {
		return err
	}
	if !valid {
		return nil
	}

	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[T]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]T, error) {
			return firstPage(ctx, tx, address, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return nextPage(ctx, tx, address, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return prevPage(ctx, tx, address, cursor, limit)
		},
	}, pagination, resultKey, fmt.Sprintf(descriptionFmt, address), getID, hooks)
}

// vouchersListHandler serves the public market catalog: active, geolocated
// voucher summaries with a backend-derived marker kind. The public market
// routes carry a shared cache policy and per-IP rate limit.
//
// Optional `q`, `tags`, `tagMatch`, and `facets` narrow the page
// (PG_SEARCH_PLAN.md Slice 1). The response shape is unchanged when they are
// absent, and gains only the additive `tagFacets` key when `facets=true` is
// asked for on a first page, so the map client's bulk import is unaffected.
// Every page direction — first, next, and previous — applies every active
// predicate; no direction falls through to an unfiltered statement.
func (a *API) vouchersListHandler(c *echo.Context) error {
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}

	// Parsed once here; the box only ever selects the bounded prepared
	// statement and supplies its parameters. No SQL is built from user input.
	bounds, err := parseMarketBounds(c.Request().URL.Query().Get("bbox"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}

	search, err := parseMarketDiscoverySearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	if search.filter.Owner, err = addressFilterParam(c, "owner"); err != nil {
		return respondCatalogSearchError(c, err)
	}

	pagination := normalizePaginationWithMax(req, marketMaxPerPage)

	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[store.MarketVoucher]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]store.MarketVoucher, error) {
			return a.store.ListMarketVouchersFiltered(ctx, tx, bounds, search.filter, store.CatalogFirstPage, 0, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.MarketVoucher, error) {
			return a.store.ListMarketVouchersFiltered(ctx, tx, bounds, search.filter, store.CatalogNextPage, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.MarketVoucher, error) {
			return a.store.ListMarketVouchersFiltered(ctx, tx, bounds, search.filter, store.CatalogPrevPage, cursor, limit)
		},
	}, pagination, "vouchers", "Market vouchers", func(v store.MarketVoucher) uint64 {
		return v.ID
	}, catalogListHooks{
		validate: func(tx pgx.Tx) (bool, error) {
			if handled, err := a.resolveNearbyCatalog(c, tx, &search); handled || err != nil {
				return handled, err
			}
			if handled, err := a.resolvePoolFilter(c, tx, &search); handled || err != nil {
				return handled, err
			}
			return a.rejectUnknownTags(c, tx, search.filter.Tags)
		},
		extras: catalogTagFacetExtras(search, pagination, func(tx pgx.Tx) ([]store.TagFacet, error) {
			return a.marketVoucherTagFacets(c, tx, bounds, search.filter)
		}),
		onSuccess: setMarketCacheHeaders,
	})
}

func bindCatalogPagination(c *echo.Context) (paginationParams, error) {
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return paginationParams{}, handleBindError(c, err)
	}
	return normalizePagination(req), nil
}

func bindCatalogAddressPagination(c *echo.Context) (string, paginationParams, bool, error) {
	pagination, err := bindCatalogPagination(c)
	if err != nil {
		return "", paginationParams{}, false, err
	}

	address := c.Param("address")
	if !common.IsHexAddress(address) {
		err := c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address must be a valid hex address",
		})
		return "", paginationParams{}, false, err
	}
	return address, pagination, true, nil
}

func cursorPaginatedCatalogList[T any](
	c *echo.Context,
	a *API,
	callbacks paginatedCatalogListCallbacks[T],
	pagination paginationParams,
	resultKey, description string,
	getID func(T) uint64,
	hooks catalogListHooks,
) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Bound the read at the database before issuing it, so a pathological plan
	// cannot hold the connection past the timeout PostgreSQL does not impose by
	// default.
	if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
		return handlePostgresError(c, err)
	}

	if hooks.validate != nil {
		handled, vErr := hooks.validate(tx)
		if handled || vErr != nil {
			return vErr
		}
	}

	var items []T
	switch {
	case pagination.FirstPage:
		items, err = callbacks.firstPage(ctx, tx, pagination.PerPage)
	case pagination.Next:
		items, err = callbacks.nextPage(ctx, tx, pagination.Cursor, pagination.PerPage)
	default:
		items, err = callbacks.prevPage(ctx, tx, pagination.Cursor, pagination.PerPage)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if items == nil {
		items = []T{}
	}

	var first, last uint64
	if len(items) > 0 {
		first = getID(items[0])
		last = getID(items[len(items)-1])
	}

	result := map[string]any{
		resultKey: items,
		"first":   first,
		"last":    last,
	}
	if hooks.extras != nil {
		extras, eErr := hooks.extras(tx)
		if eErr != nil {
			return handlePostgresError(c, eErr)
		}
		maps.Copy(result, extras)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	if hooks.onSuccess != nil {
		hooks.onSuccess(c)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: description,
		Result:      result,
	})
}
