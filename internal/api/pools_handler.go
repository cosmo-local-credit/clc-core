package api

import (
	"fmt"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

// poolsListHandler returns the network pool catalog (imported + clc-core
// deploys), cursor-paginated. Directory reads come from the pools catalog,
// not the deploy pipeline; deploy-status APIs keep
// reading deployed_pools.
//
// Optional `q`, `tags`, `tagMatch`, `vouchers`, `voucherMatch`, `owner`, and `facets`
// narrow the page. Pool tags are the pool's own curated
// `pool_tags` — the same values each row already returns — not member-voucher
// or member-offering tags, so a filtered row always carries the tag that
// matched it. Every page direction applies every active predicate.
func (a *API) poolsListHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	c.Response().Header().Set("Cache-Control", "no-store")

	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	pagination := normalizePagination(req)

	search, err := parseCatalogRelationshipSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	// `owner` narrows the page to one address' pools, the same parameter and the
	// same case-folded comparison the voucher catalog takes. It names an address
	// rather than reading the caller's session, because the console asks for
	// someone else's pools and the wallet asks for its own through one route —
	// and `owner_address` is already on every row this endpoint returns, so the
	// filter discloses nothing an unfiltered page would not.
	if search.filter.Owner, err = addressFilterParam(c, "owner"); err != nil {
		return respondCatalogSearchError(c, err)
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
		return handlePostgresError(c, err)
	}

	if handled, vErr := a.resolveNearbyCatalog(c, tx, &search); handled || vErr != nil {
		return vErr
	}
	if handled, vErr := a.rejectUnknownTags(c, tx, search.filter.Tags); handled || vErr != nil {
		return vErr
	}
	if handled, vErr := a.resolveVoucherFilter(c, tx, &search); handled || vErr != nil {
		return vErr
	}

	var page store.CatalogPage
	switch {
	case pagination.FirstPage:
		page = store.CatalogFirstPage
	case pagination.Next:
		page = store.CatalogNextPage
	default:
		page = store.CatalogPrevPage
	}

	pools, err := a.store.ListPoolsFiltered(ctx, tx, search.filter, page, pagination.Cursor, pagination.PerPage)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if pools == nil {
		pools = []store.Pool{}
	}

	var first, last uint64
	if len(pools) > 0 {
		first = pools[0].ID
		last = pools[len(pools)-1].ID
	}

	result := map[string]any{
		"pools": publicResources(pools, publicContactFields...),
		"first": first,
		"last":  last,
	}
	if search.facets && pagination.FirstPage {
		facets, fErr := a.store.ListPoolTagFacets(ctx, tx, search.filter)
		if fErr != nil {
			return handlePostgresError(c, fErr)
		}
		result["tagFacets"] = facets

		voucherFacets, vfErr := a.store.ListPoolVoucherFacets(ctx, tx, search.filter)
		if vfErr != nil {
			return handlePostgresError(c, vfErr)
		}
		result["voucherFacets"] = voucherFacets
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Pools",
		Result:      result,
	})
}

// poolsByAddressHandler returns a single catalog pool by contract address
// (case-insensitive lookup; stored addresses are EIP-55).
func (a *API) poolsByAddressHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	address := c.Param("address")

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pool, err := a.store.GetPoolByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if !pool.Active {
		return poolProfileNotFoundError(c)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Pool at address %s", address),
		Result: map[string]any{
			"pool": publicResource(pool, publicContactFields...),
		},
	})
}
