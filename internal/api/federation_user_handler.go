package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
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
		if valuation, ok := valuations[transferValuationKey(txHash, items[i].TokenAddress)]; ok {
			items[i].UOAValue = valuation.UOAValue
			items[i].UOA = valuation.UOA
		}

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
	}
	return nil
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
	tokens, err := a.federationStore.ListUserTokenHoldings(ctx, req.Address, 5)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := a.enrichTokenBalances(ctx, tokens); err != nil {
		return handlePostgresError(c, err)
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

func (a *API) accountTransfersHandler(c *echo.Context) error {
	req := apiresp.OTXByAccountRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	callerUserID, _, _ := getUserClaims(c)

	return cursorPaginatedFederationList(c, paginatedFederationListCallbacks[store.FederationTransfer]{
		firstPage: a.federationStore.ListUserTransferHistory,
		nextPage:  a.federationStore.ListUserTransferHistoryNext,
		prevPage:  a.federationStore.ListUserTransferHistoryPrevious,
		enrich: func(ctx context.Context, items []store.FederationTransfer) error {
			return a.enrichTransfers(ctx, items, callerUserID)
		},
	}, req.Address, normalizePagination(apiresp.CursorPaginationRequest{
		PerPage: req.PerPage,
		Next:    req.Next,
		Cursor:  req.Cursor,
	}), "transfers", fmt.Sprintf("Transfer history for %s", req.Address),
		func(t store.FederationTransfer) uint64 { return t.ID })
}
