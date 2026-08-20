package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// The operator console's three transaction pages.
//
// They are three endpoints rather than one filtered endpoint because they read
// three different things. The first two come from the federation database — what
// the chain did — and the third from our own tables: what this service signed
// and whether the network took it. An OTX with status REVERTED and a reverted
// voucher transfer are not the same fact, and an operator looking for one is not
// looking for the other.
//
// All three page by cursor, newest first, and answer with `first`/`last` exactly
// as the catalog endpoints do.

// adminTxFilters reads and validates the `type` and contract filters shared by
// the two chain-backed pages. It reports handled = true when it has already
// written the error response.
func adminTxFilters(c *echo.Context, kinds []string, contractParam string) (kind, contract string, handled bool, err error) {
	kind = strings.ToLower(strings.TrimSpace(c.QueryParam("type")))
	if kind != "" && !slices.Contains(kinds, kind) {
		return "", "", true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "unknown type; use " + strings.Join(kinds, ", "),
		})
	}

	raw := strings.TrimSpace(c.QueryParam(contractParam))
	if raw == "" {
		return kind, "", false, nil
	}
	if !common.IsHexAddress(raw) {
		return "", "", true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: contractParam + " must be a valid hex address",
		})
	}
	// Checksummed before it reaches SQL: the federation schema stores EIP-55
	// addresses and compares them with plain equality, so a lowercased address
	// pasted into the URL would silently match nothing.
	return kind, common.HexToAddress(raw).Hex(), false, nil
}

// adminChainTxPage runs one federation-backed transaction page and writes the
// cursor response. The two surfaces differ only in their row type, their list
// function and the metadata they enrich, so the pagination and the envelope live
// here once.
func adminChainTxPage[T any](
	c *echo.Context,
	a *API,
	pagination paginationParams,
	resultKey, description string,
	getID func(T) uint64,
	list func(context.Context, *uint64, uint64, bool) ([]T, error),
	enrich func(context.Context, []T) error,
) error {
	ctx := c.Request().Context()

	var cursor *uint64
	if !pagination.FirstPage {
		cursor = &pagination.Cursor
	}

	rows, err := list(ctx, cursor, pagination.PerPage, pagination.Next)
	if err != nil {
		a.logg.Warn("admin transaction page failed", "resource", resultKey, "error", err)
		return handlePostgresError(c, err)
	}
	if rows == nil {
		rows = []T{}
	}
	if len(rows) > 0 {
		if err := enrich(ctx, rows); err != nil {
			return handlePostgresError(c, err)
		}
	}

	var first, last uint64
	if len(rows) > 0 {
		first = getID(rows[0])
		last = getID(rows[len(rows)-1])
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: description,
		Result: map[string]any{
			resultKey: rows,
			"first":   first,
			"last":    last,
		},
	})
}

// adminChainTxQuery reads everything the two chain-backed pages parse before they
// diverge: the federation dependency, the cursor page, the `type` and contract
// filters, and the free-text term. handled = true means the error response is
// already written.
//
// Extracted because the two handlers were byte-identical here, differing only in
// which kinds they accept and which contract parameter they name — and a filter
// fixed on one page but not the other is exactly the bug that shape invites.
func (a *API) adminChainTxQuery(
	c *echo.Context,
	kinds []string,
	contractParam string,
) (page paginationParams, kind, contract string, search store.AdminTransactionSearch, handled bool, err error) {
	if a.federationStore == nil {
		return page, "", "", search, true, respondFederationUnavailable(c)
	}
	page, err = bindCatalogPagination(c)
	if err != nil {
		return page, "", "", search, true, err
	}
	kind, contract, handled, err = adminTxFilters(c, kinds, contractParam)
	if handled {
		return page, "", "", search, true, err
	}
	search, err = store.NewAdminTransactionSearch(c.QueryParam("q"))
	if err != nil {
		return page, "", "", search, true, respondCatalogSearchError(c, err)
	}
	return page, kind, contract, search, false, nil
}

func (a *API) adminVoucherTransactionsHandler(c *echo.Context) error {
	page, kind, voucher, search, handled, err := a.adminChainTxQuery(
		c, store.AdminVoucherTxKinds(), "voucher")
	if handled {
		return err
	}

	return adminChainTxPage(c, a, page, "transactions", "Voucher transactions",
		func(row store.AdminVoucherTransaction) uint64 { return row.ID },
		func(ctx context.Context, cursor *uint64, limit uint64, next bool) ([]store.AdminVoucherTransaction, error) {
			return a.federationStore.ListAdminVoucherTransactions(ctx, cursor, limit, next, kind, voucher, search)
		},
		a.enrichVoucherTransactions)
}

func (a *API) adminPoolTransactionsHandler(c *echo.Context) error {
	page, kind, pool, search, handled, err := a.adminChainTxQuery(
		c, store.AdminPoolTxKinds(), "pool")
	if handled {
		return err
	}

	return adminChainTxPage(c, a, page, "transactions", "Pool transactions",
		func(row store.AdminPoolTransaction) uint64 { return row.ID },
		func(ctx context.Context, cursor *uint64, limit uint64, next bool) ([]store.AdminPoolTransaction, error) {
			return a.federationStore.ListAdminPoolTransactions(ctx, cursor, limit, next, kind, pool, search)
		},
		a.enrichPoolTransactions)
}

// adminOTXHandler lists what this service signed, whatever it was signed for.
//
// Local tables only — no federation dependency — so it keeps answering when the
// warehouse link is down, which is exactly when an operator wants to know
// whether dispatches are still reaching the network.
func (a *API) adminOTXHandler(c *echo.Context) error {
	pagination, err := bindCatalogPagination(c)
	if err != nil {
		return err
	}

	otxType := strings.ToUpper(strings.TrimSpace(c.QueryParam("type")))
	if otxType != "" && !slices.Contains(store.OTXTypes, otxType) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "unknown type; use one of " + strings.Join(store.OTXTypes, ", "),
		})
	}
	status := strings.ToUpper(strings.TrimSpace(c.QueryParam("status")))
	if status != "" && !slices.Contains(store.DispatchStatuses, status) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "unknown status; use one of " + strings.Join(store.DispatchStatuses, ", "),
		})
	}
	search, err := store.NewAdminTransactionSearch(c.QueryParam("q"))
	if err != nil {
		return respondCatalogSearchError(c, err)
	}

	return adminPagedList(c, a, pagination, "otx", "Origin transactions",
		func(row store.AdminOTX) uint64 { return row.ID },
		func(ctx context.Context, tx pgx.Tx, page store.CatalogPage, cursor, limit uint64) ([]store.AdminOTX, error) {
			return a.store.ListAdminOTX(ctx, tx, otxType, status, search, page, cursor, limit)
		})
}

func respondFederationUnavailable(c *echo.Context) error {
	return c.JSON(http.StatusServiceUnavailable, apiresp.ErrResponse{
		Ok:          false,
		Description: "Federation is not configured",
	})
}

// enrichVoucherTransactions fills the voucher's name, symbol and decimals from
// the local catalog. A voucher the catalog does not know keeps zero decimals,
// which the console renders as a raw amount rather than as a wrong one.
func (a *API) enrichVoucherTransactions(ctx context.Context, rows []store.AdminVoucherTransaction) error {
	meta, err := a.voucherMetadataByAddress(ctx, dedupedLowerAddresses(rows,
		func(row store.AdminVoucherTransaction) string { return row.Voucher }))
	if err != nil {
		return err
	}
	for i := range rows {
		m, ok := meta[strings.ToLower(rows[i].Voucher)]
		if !ok {
			continue
		}
		if m.Symbol != nil {
			rows[i].VoucherSymbol = *m.Symbol
		}
		if m.Name != nil {
			rows[i].VoucherName = *m.Name
		}
		rows[i].VoucherDecimals = catalogDecimals(m)
	}
	return nil
}

// enrichPoolTransactions fills both sides of a swap. A deposit has no out token,
// so only the in side is looked up for it.
func (a *API) enrichPoolTransactions(ctx context.Context, rows []store.AdminPoolTransaction) error {
	addresses := make([]string, 0, len(rows)*2)
	seen := make(map[string]struct{}, len(rows)*2)
	add := func(address string) {
		lowered := strings.ToLower(address)
		if lowered == "" {
			return
		}
		if _, ok := seen[lowered]; ok {
			return
		}
		seen[lowered] = struct{}{}
		addresses = append(addresses, lowered)
	}
	for _, row := range rows {
		add(row.TokenIn)
		if row.TokenOut != nil {
			add(*row.TokenOut)
		}
	}

	meta, err := a.voucherMetadataByAddress(ctx, addresses)
	if err != nil {
		return err
	}
	for i := range rows {
		if m, ok := meta[strings.ToLower(rows[i].TokenIn)]; ok {
			if m.Symbol != nil {
				rows[i].TokenInSymbol = *m.Symbol
			}
			rows[i].TokenInDecimals = catalogDecimals(m)
		}
		if rows[i].TokenOut == nil {
			continue
		}
		if m, ok := meta[strings.ToLower(*rows[i].TokenOut)]; ok {
			if m.Symbol != nil {
				rows[i].TokenOutSymbol = *m.Symbol
			}
			rows[i].TokenOutDecimals = catalogDecimals(m)
		}
	}
	return nil
}

// catalogDecimals narrows a catalog row's decimals to the byte the chain uses.
// A row that carries none stays null, so the console can say it cannot scale the
// amount rather than showing it at the wrong magnitude.
func catalogDecimals(m store.VoucherMetadata) *uint8 {
	if m.Decimals == nil || *m.Decimals < 0 || *m.Decimals > 255 {
		return nil
	}
	decimals := uint8(*m.Decimals)
	return &decimals
}
