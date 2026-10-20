package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// TransferHistorySearch is resolved in Core before paging chain facts in the
// federation database. Metadata must never be filtered after LIMIT.
type TransferHistorySearch struct {
	Filter         CatalogFilter
	TokenAddresses []string
}

// TransferSearchTermMaxRunes permits a complete 0x-prefixed transaction hash.
const TransferSearchTermMaxRunes = 66

// NewTransferHistoryFilter validates the activity-specific text contract.
func NewTransferHistoryFilter(raw string) (CatalogFilter, error) {
	term := NormalizeCatalogTerm(raw)
	length := utf8.RuneCountInString(term)
	if length > TransferSearchTermMaxRunes {
		return CatalogFilter{}, errors.New("q must be at most 66 characters")
	}
	if length > 0 && length < CatalogTermMinRunes {
		return CatalogFilter{}, ErrCatalogTermTooShort
	}
	return CatalogFilter{Term: term, TagMatch: TagMatchAny}, nil
}

// ListTransferSearchTokens includes retired vouchers: retirement must not hide
// historical transfers. It deliberately ignores public discovery/location filters.
func (pg *Pg) ListTransferSearchTokens(ctx context.Context, tx pgx.Tx, filter CatalogFilter) ([]string, error) {
	args := &sqlArgs{}
	query := "SELECT DISTINCT lower(v.address) FROM vouchers v WHERE " + searchPredicate(args, filter, "v")
	var addresses []string
	if err := pgxscan.Select(ctx, tx, &addresses, query, args.values...); err != nil {
		return nil, err
	}
	return addresses, nil
}

func composeTransferHistorySearch(address string, search TransferHistorySearch, cursor *uint64, limit uint64, next bool) composed {
	args := &sqlArgs{}
	owner := args.bind(address)
	// Bind a concrete empty array rather than SQL NULL.
	tokens := search.TokenAddresses
	if tokens == nil {
		tokens = []string{}
	}
	tokenMatch := "lower(tt.contract_address) = ANY(" + args.bind(tokens) + "::text[])"
	textMatch := (AdminTransactionSearch{Term: search.Filter.Term}).predicate(
		[]string{"tt.contract_address", "counterparty_address", "tx.tx_hash"},
		func(value string) string { return args.bind(value) }, args.reuse,
	)
	order, comparison := "DESC", "<"
	if cursor != nil && !next {
		order, comparison = "ASC", ">"
	}
	bound := ""
	if cursor != nil {
		bound = " AND tt.id " + comparison + " " + args.bind(*cursor)
	}
	pageLimit := args.bind(limit)
	arms := make([]string, 0, 2)
	for _, incoming := range []bool{true, false} {
		direction, accountColumn, otherColumn := "IN", "tt.recipient_address", "tt.sender_address"
		if !incoming {
			direction, accountColumn, otherColumn = "OUT", "tt.sender_address", "tt.recipient_address"
		}
		// Expand the closed, store-owned counterparty column separately per arm.
		match := strings.ReplaceAll(textMatch, "counterparty_address", otherColumn)
		excludeSelf := ""
		if !incoming {
			excludeSelf = " AND tt.recipient_address <> " + owner
		}
		arms = append(arms, `(SELECT tt.id, tx.tx_hash, tx.date_block, '`+direction+`' AS direction,
            `+otherColumn+` AS counterparty_address, tt.sender_address, tt.recipient_address,
            tt.contract_address, tt.transfer_value::text AS amount, tx.success
            FROM token_transfer tt JOIN tx ON tx.id = tt.tx_id
            WHERE `+accountColumn+" = "+owner+excludeSelf+bound+
			" AND ("+tokenMatch+" OR "+match+") ORDER BY tt.id "+order+" LIMIT "+pageLimit+")")
	}
	return composed{sql: "SELECT * FROM (" + strings.Join(arms, " UNION ALL ") + ") history ORDER BY id " + order + " LIMIT " + pageLimit, args: args.values}
}

// configureTransferSearch keeps an underestimated foreign join from being fully
// fetched and sorted locally. An ordered FDW cursor lets LIMIT stop consumption
// after a page (with the FDW's configured fetch-batch overfetch). Custom planning
// also keeps parameterized text predicates shippable on repeated prepared reads.
// Both settings are transaction-local; other users of the pool retain their plans.
func configureTransferSearch(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT
        set_config('enable_sort', 'off', true),
        set_config('plan_cache_mode', 'force_custom_plan', true)`)
	return err
}

func (s *FederationPg) SearchUserTransferHistory(ctx context.Context, address string, search TransferHistorySearch, cursor *uint64, limit uint64, next bool) ([]FederationTransfer, error) {
	// A remote search must not occupy a federation connection indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := configureTransferSearch(ctx, tx); err != nil {
		return nil, err
	}
	query := composeTransferHistorySearch(address, search, cursor, limit, next)
	var transfers []FederationTransfer
	if err := pgxscan.Select(ctx, tx, &transfers, query.sql, query.args...); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return transfers, nil
}
