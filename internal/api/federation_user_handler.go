package api

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v5"
)

type (
	// paginatedFederationListCallbacks groups the three pagination query
	// functions for a federation resource of type T. Unlike
	// paginatedListCallbacks these read directly from the federation pool (no
	// pgx.Tx) because the federation store is read-only with respect to these
	// queries. The optional enrich hook runs on the fetched page before it is
	// serialized — used to fill token metadata from the local vouchers
	// catalog, which the federation queries no longer join.
	paginatedFederationListCallbacks[T any] struct {
		firstPage func(context.Context, string, uint64) ([]T, error)
		nextPage  func(context.Context, string, uint64, uint64) ([]T, error)
		prevPage  func(context.Context, string, uint64, uint64) ([]T, error)
		enrich    func(context.Context, []T) error
	}
)

// voucherMetadataByAddress loads local catalog metadata for the given token
// addresses, keyed by lowercased address. Addresses missing from the catalog
// simply have no entry — callers leave those rows' metadata zero-valued.
func (a *API) voucherMetadataByAddress(ctx context.Context, addresses []string) (map[string]store.VoucherMetadata, error) {
	meta := make(map[string]store.VoucherMetadata, len(addresses))
	if len(addresses) == 0 {
		return meta, nil
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := a.store.ListVoucherMetadataByAddresses(ctx, tx, addresses)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		meta[strings.ToLower(r.Address)] = r
	}
	return meta, nil
}

func dedupedLowerAddresses[T any](items []T, addr func(T) string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		a := strings.ToLower(addr(it))
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}

// enrichTokenBalances fills token metadata on federation holdings from the
// local vouchers catalog.
func (a *API) enrichTokenBalances(ctx context.Context, items []store.FederationTokenBalance) error {
	meta, err := a.voucherMetadataByAddress(ctx, dedupedLowerAddresses(items,
		func(t store.FederationTokenBalance) string { return t.TokenAddress }))
	if err != nil {
		return err
	}
	for i := range items {
		m, ok := meta[strings.ToLower(items[i].TokenAddress)]
		if !ok {
			continue
		}
		if m.Symbol != nil {
			items[i].TokenSymbol = *m.Symbol
		}
		if m.Name != nil {
			items[i].TokenName = *m.Name
		}
		if m.Decimals != nil {
			items[i].TokenDecimals = uint8(*m.Decimals)
		}
		items[i].IconURL = m.IconURL
		items[i].CatalogActive = &m.Active
	}
	return nil
}

// noteBearingTxHashes reports which of the given transfer hashes carry a
// private attachment the caller may read. The attachments live in clc-core's
// own database while the transfers come from the federation indexer, so the
// two can only be joined here, in Go.
//
// A caller without a user identity — the service token is also accepted on
// this route — sees no attachments rather than everyone's.
func (a *API) noteBearingTxHashes(ctx context.Context, txHashes []string, callerUserID uint64) (map[string]bool, error) {
	noted := make(map[string]bool, len(txHashes))
	if len(txHashes) == 0 || callerUserID == 0 {
		return noted, nil
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	hashes, err := a.store.ListUserShareTxHashes(ctx, tx, txHashes, callerUserID)
	if err != nil {
		return nil, err
	}
	for _, h := range hashes {
		noted[strings.ToLower(h)] = true
	}
	return noted, nil
}

// transferValuationsByTxHash loads immutable submission-time Unit-of-Account
// snapshots for a page of federation transfers. Transfers predating snapshots
// or submitted outside the relayer simply have no entry.
func transferValuationKey(txHash, tokenAddress string) string {
	return strings.ToLower(txHash) + ":" + strings.ToLower(tokenAddress)
}

func (a *API) transferValuationsByTxHash(ctx context.Context, txHashes []string) (map[string]store.TransferValuationSnapshot, error) {
	valuations := make(map[string]store.TransferValuationSnapshot, len(txHashes))
	if len(txHashes) == 0 {
		return valuations, nil
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := a.store.ListTransferValuationSnapshotsByTxHashes(ctx, tx, txHashes)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		valuations[transferValuationKey(row.TxHash, row.TokenAddress)] = row
	}
	return valuations, nil
}

// enrichTransfers fills token metadata on federation transfers from the local
// vouchers catalog, and flags the ones carrying a private attachment.
func (a *API) enrichTransfers(ctx context.Context, items []store.FederationTransfer, callerUserID uint64) error {
	meta, err := a.voucherMetadataByAddress(ctx, dedupedLowerAddresses(items,
		func(t store.FederationTransfer) string { return t.TokenAddress }))
	if err != nil {
		return err
	}
	noted, err := a.noteBearingTxHashes(ctx, dedupedLowerAddresses(items,
		func(t store.FederationTransfer) string { return t.TxHash }), callerUserID)
	if err != nil {
		return err
	}
	valuations, err := a.transferValuationsByTxHash(ctx, dedupedLowerAddresses(items,
		func(t store.FederationTransfer) string { return t.TxHash }))
	if err != nil {
		return err
	}
	for i := range items {
		txHash := strings.ToLower(items[i].TxHash)
		items[i].HasNote = noted[txHash]

		m, ok := meta[strings.ToLower(items[i].TokenAddress)]
		if ok {
			if m.Symbol != nil {
				items[i].TokenSymbol = *m.Symbol
			}
			if m.Name != nil {
				items[i].TokenName = *m.Name
			}
			if m.Decimals != nil && *m.Decimals >= 0 && *m.Decimals <= 255 {
				items[i].TokenDecimals = uint8(*m.Decimals)
			}
		}

		valuation, hasValuation := valuations[transferValuationKey(txHash, items[i].TokenAddress)]
		if hasValuation && ok && m.Decimals != nil {
			if value, valid := exactRawUnitValue(items[i].Amount, *m.Decimals, valuation.VoucherValue); valid {
				items[i].UOAValue = value
				items[i].UOA = valuation.VoucherUOA
			}
		}
	}
	return nil
}

// exactRawUnitValue multiplies one indexed raw token amount by the immutable
// database claim captured at relay submission. Both inputs are decimal text;
// no float conversion or chain lookup is involved.
func exactRawUnitValue(amountRaw string, decimals int32, voucherValue string) (string, bool) {
	if decimals < 0 || decimals > 255 || !decimalDigits(amountRaw) {
		return "", false
	}
	parts := strings.Split(voucherValue, ".")
	if len(parts) > 2 || len(parts) == 0 || !decimalDigits(parts[0]) {
		return "", false
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || !decimalDigits(fraction) {
			return "", false
		}
	}
	rate := new(big.Int)
	if _, valid := rate.SetString(parts[0]+fraction, 10); !valid || rate.Sign() <= 0 {
		return "", false
	}
	amount := new(big.Int)
	if _, valid := amount.SetString(amountRaw, 10); !valid || amount.Sign() < 0 {
		return "", false
	}
	product := new(big.Int).Mul(amount, rate)
	return canonicalScaledDecimal(product.String(), int(decimals)+len(fraction)), true
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func canonicalScaledDecimal(digits string, scale int) string {
	if scale == 0 {
		trimmed := strings.TrimLeft(digits, "0")
		if trimmed == "" {
			return "0"
		}
		return trimmed
	}
	if padding := scale + 1 - len(digits); padding > 0 {
		digits = strings.Repeat("0", padding) + digits
	}
	boundary := len(digits) - scale
	whole := strings.TrimLeft(digits[:boundary], "0")
	if whole == "" {
		whole = "0"
	}
	fraction := strings.TrimRight(digits[boundary:], "0")
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}

// cursorPaginatedFederationList executes a cursor-paginated federation query
// and returns a JSON response with the items and first/last cursor values. The
// caller is responsible for binding and validating the request (including the
// path-param address) and for normalising the pagination params, so this helper
// never re-binds the query string — mirroring accountOTXHandler.
func cursorPaginatedFederationList[T any](
	c *echo.Context,
	p paginatedFederationListCallbacks[T],
	address string,
	pagination paginationParams,
	resultKey, description string,
	getID func(T) uint64,
) error {
	ctx := c.Request().Context()

	var (
		items    []T
		queryErr error
	)

	switch {
	case pagination.FirstPage:
		items, queryErr = p.firstPage(ctx, address, pagination.PerPage)
	case pagination.Next:
		items, queryErr = p.nextPage(ctx, address, pagination.Cursor, pagination.PerPage)
	default:
		items, queryErr = p.prevPage(ctx, address, pagination.Cursor, pagination.PerPage)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	if p.enrich != nil && len(items) > 0 {
		if err := p.enrich(ctx, items); err != nil {
			return handlePostgresError(c, err)
		}
	}

	var first, last uint64
	if len(items) > 0 {
		first = getID(items[0])
		last = getID(items[len(items)-1])
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: description,
		Result: map[string]any{
			resultKey: items,
			"first":   first,
			"last":    last,
		},
	})
}

func (a *API) accountLatestTokensHandler(c *echo.Context) error {
	req := apiresp.AccountAddressParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()
	// Fill the summary after applying catalog visibility. Retired recent balances
	// must not hide older active holdings or produce a false empty wallet.
	const limit = 5
	tokens := make([]store.FederationTokenBalance, 0, limit)
	var cursor uint64
	for len(tokens) < limit {
		var page []store.FederationTokenBalance
		var err error
		if cursor == 0 {
			page, err = a.federationStore.ListUserTokenHoldings(ctx, req.Address, limit)
		} else {
			page, err = a.federationStore.ListUserTokenHoldingsNext(ctx, req.Address, cursor, limit)
		}
		if err != nil {
			return handlePostgresError(c, err)
		}
		if err := a.enrichTokenBalances(ctx, page); err != nil {
			return handlePostgresError(c, err)
		}
		for _, token := range page {
			if token.CatalogActive == nil || *token.CatalogActive {
				tokens = append(tokens, token)
				if len(tokens) == limit {
					break
				}
			}
		}
		if len(page) < limit {
			break
		}
		next := page[len(page)-1].ID
		if next == 0 || (cursor != 0 && next >= cursor) {
			return fmt.Errorf("token balance cursor did not advance")
		}
		cursor = next
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Latest token balances for %s", req.Address),
		Result: map[string]any{
			"tokens": tokens,
		},
	})
}

func (a *API) accountTokensHandler(c *echo.Context) error {
	req := apiresp.OTXByAccountRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	return cursorPaginatedFederationList(c, paginatedFederationListCallbacks[store.FederationTokenBalance]{
		firstPage: a.federationStore.ListUserTokenHoldings,
		nextPage:  a.federationStore.ListUserTokenHoldingsNext,
		prevPage:  a.federationStore.ListUserTokenHoldingsPrevious,
		enrich:    a.enrichTokenBalances,
	}, req.Address, normalizePagination(apiresp.CursorPaginationRequest{
		PerPage: req.PerPage,
		Next:    req.Next,
		Cursor:  req.Cursor,
	}), "tokens", fmt.Sprintf("Token balances for %s", req.Address),
		func(t store.FederationTokenBalance) uint64 { return t.ID })
}

// issuedTokenPage reads one page of the vouchers an address owns from the local
// catalog. The rows carry no balance until enrichIssuedTokens fills it.
func (a *API) issuedTokenPage(ctx context.Context, owner string, page store.CatalogPage, cursor, limit uint64) ([]store.FederationTokenBalance, error) {
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	return a.store.ListOwnedVoucherTokens(ctx, tx, owner, page, cursor, limit)
}

// enrichIssuedTokens fills the owner's balance of each issued voucher from the
// federation indexer, then the catalog metadata every balance row carries. A
// voucher the owner never moved keeps a zero balance.
func (a *API) enrichIssuedTokens(ctx context.Context, owner string, items []store.FederationTokenBalance) error {
	tokens := make([]string, len(items))
	for i := range items {
		// The catalog keeps whichever casing an address arrived in; the indexer
		// stores it checksummed and compares exactly.
		items[i].TokenAddress = common.HexToAddress(items[i].TokenAddress).Hex()
		tokens[i] = items[i].TokenAddress
	}
	balances, err := a.federationStore.ListUserTokenBalances(ctx, owner, tokens)
	if err != nil {
		return err
	}
	byAddress := make(map[string]store.FederationTokenBalance, len(balances))
	for _, b := range balances {
		byAddress[strings.ToLower(b.TokenAddress)] = b
	}
	for i := range items {
		items[i].Balance = "0"
		if b, ok := byAddress[strings.ToLower(items[i].TokenAddress)]; ok {
			items[i].Balance = b.Balance
			items[i].LastDateBlock = b.LastDateBlock
		}
	}
	return a.enrichTokenBalances(ctx, items)
}

// accountIssuedTokensHandler pages the vouchers an address owns, in the shape
// and cursor order of accountTokensHandler. Unlike it, a zero balance is kept:
// a voucher whose opening supply was never created, or whose owner handed out
// every unit, is still the owner's to manage, and the wallet has no other way
// back to it.
func (a *API) accountIssuedTokensHandler(c *echo.Context) error {
	req := apiresp.OTXByAccountRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	return cursorPaginatedFederationList(c, paginatedFederationListCallbacks[store.FederationTokenBalance]{
		firstPage: func(ctx context.Context, owner string, limit uint64) ([]store.FederationTokenBalance, error) {
			return a.issuedTokenPage(ctx, owner, store.CatalogFirstPage, 0, limit)
		},
		nextPage: func(ctx context.Context, owner string, cursor, limit uint64) ([]store.FederationTokenBalance, error) {
			return a.issuedTokenPage(ctx, owner, store.CatalogNextPage, cursor, limit)
		},
		prevPage: func(ctx context.Context, owner string, cursor, limit uint64) ([]store.FederationTokenBalance, error) {
			return a.issuedTokenPage(ctx, owner, store.CatalogPrevPage, cursor, limit)
		},
		enrich: func(ctx context.Context, items []store.FederationTokenBalance) error {
			return a.enrichIssuedTokens(ctx, req.Address, items)
		},
	}, req.Address, normalizePagination(apiresp.CursorPaginationRequest{
		PerPage: req.PerPage,
		Next:    req.Next,
		Cursor:  req.Cursor,
	}), "tokens", fmt.Sprintf("Issued tokens for %s", req.Address),
		func(t store.FederationTokenBalance) uint64 { return t.ID })
}

// accountTransfersHandler optionally searches the full account history with q.
// Voucher metadata is resolved locally; federation applies all filters before paging.
func (a *API) accountTransfersHandler(c *echo.Context) error {
	req := apiresp.OTXByAccountRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	filter, err := store.NewTransferHistoryFilter(c.QueryParam("q"))
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	callbacks := paginatedFederationListCallbacks[store.FederationTransfer]{
		firstPage: a.federationStore.ListUserTransferHistory,
		nextPage:  a.federationStore.ListUserTransferHistoryNext,
		prevPage:  a.federationStore.ListUserTransferHistoryPrevious,
	}
	if filter.Term != "" {
		ctx := c.Request().Context()
		tx, err := a.store.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
			return handlePostgresError(c, err)
		}
		tokens, err := a.store.ListTransferSearchTokens(ctx, tx, filter)
		if err != nil {
			return handlePostgresError(c, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return handlePostgresError(c, err)
		}
		search := store.TransferHistorySearch{Filter: filter, TokenAddresses: tokens}
		callbacks.firstPage = func(ctx context.Context, address string, limit uint64) ([]store.FederationTransfer, error) {
			return a.federationStore.SearchUserTransferHistory(ctx, address, search, nil, limit, true)
		}
		callbacks.nextPage = func(ctx context.Context, address string, cursor, limit uint64) ([]store.FederationTransfer, error) {
			return a.federationStore.SearchUserTransferHistory(ctx, address, search, &cursor, limit, true)
		}
		callbacks.prevPage = func(ctx context.Context, address string, cursor, limit uint64) ([]store.FederationTransfer, error) {
			return a.federationStore.SearchUserTransferHistory(ctx, address, search, &cursor, limit, false)
		}
	}
	callerUserID, _, _ := getUserClaims(c)

	callbacks.enrich = func(ctx context.Context, items []store.FederationTransfer) error {
		return a.enrichTransfers(ctx, items, callerUserID)
	}
	return cursorPaginatedFederationList(c, callbacks, req.Address, normalizePagination(apiresp.CursorPaginationRequest{
		PerPage: req.PerPage,
		Next:    req.Next,
		Cursor:  req.Cursor,
	}), "transfers", fmt.Sprintf("Transfer history for %s", req.Address),
		func(t store.FederationTransfer) uint64 { return t.ID })
}
