package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
)

// Chain activity, read in the two shapes the operator console presents it in.
//
// Voucher movements (transfer, mint, burn) and pool operations (swap, deposit)
// are separate pages because they are separate facts. A swap carries two
// vouchers, two amounts and a fee; a transfer carries one of each and a
// counterparty. One table holding both would be mostly empty cells, and an
// operator scanning it could not compare a column against itself.
//
// Token metadata is not joined here. It comes from the local vouchers catalog,
// which MIGRATION.md §2 makes the single home for living voucher attributes —
// the same split FederationTransfer uses.
type (
	// AdminVoucherTransaction is one movement of one voucher: a peer-to-peer
	// transfer, a mint, or a burn.
	AdminVoucherTransaction struct {
		// ID packs (TxID, LogIndex) — see adminTxSeqScale. It is the cursor,
		// not a row identity in any table.
		ID            uint64    `db:"-"            json:"id"`
		TxID          int64     `db:"tx_id"        json:"txId"`
		LogIndex      int64     `db:"log_index"    json:"logIndex"`
		TxHash        string    `db:"tx_hash"      json:"txHash"`
		T             time.Time `db:"t"            json:"t"`
		Success       bool      `db:"success"      json:"success"`
		Type          string    `db:"-"            json:"type"`
		Voucher       string    `db:"voucher"      json:"voucher"`
		VoucherName   string    `db:"-"            json:"voucherName"`
		VoucherSymbol string    `db:"-"            json:"voucherSymbol"`
		// Null when the local catalog has no entry for the voucher. Null rather
		// than zero: a caller that cannot tell the two apart renders a raw
		// integer as a whole-unit amount, which is wrong by six orders of
		// magnitude on a typical voucher.
		VoucherDecimals *uint8 `db:"-"            json:"voucherDecimals"`
		From            string `db:"from_address" json:"from"`
		// Null for a burn: the supply is destroyed rather than delivered.
		To     *string `db:"to_address" json:"to"`
		Amount string  `db:"amount"     json:"amount"`
	}

	// AdminPoolTransaction is one pool operation: a swap through the pool or a
	// deposit into it.
	AdminPoolTransaction struct {
		ID       uint64    `db:"-"         json:"id"`
		TxID     int64     `db:"tx_id"     json:"txId"`
		LogIndex int64     `db:"log_index" json:"logIndex"`
		TxHash   string    `db:"tx_hash"   json:"txHash"`
		T        time.Time `db:"t"         json:"t"`
		Success  bool      `db:"success"   json:"success"`
		Type     string    `db:"-"         json:"type"`
		Pool     string    `db:"pool"      json:"pool"`
		// The account that sent the transaction, not the pool.
		Initiator       string `db:"initiator"    json:"initiator"`
		TokenIn         string `db:"token_in"  json:"tokenIn"`
		TokenInSymbol   string `db:"-"         json:"tokenInSymbol"`
		TokenInDecimals *uint8 `db:"-"         json:"tokenInDecimals"`
		AmountIn        string `db:"amount_in" json:"amountIn"`
		// The three out-side fields are null for a deposit, which is one-sided:
		// nothing leaves the pool and no fee is charged.
		TokenOut         *string `db:"token_out"  json:"tokenOut"`
		TokenOutSymbol   string  `db:"-"          json:"tokenOutSymbol"`
		TokenOutDecimals *uint8  `db:"-"          json:"tokenOutDecimals"`
		AmountOut        *string `db:"amount_out" json:"amountOut"`
		// Denominated in TokenOut, and excluding the protocol fee, which the
		// indexer only records for pools that emit SwapSettlement.
		Fee *string `db:"fee" json:"fee"`
	}

	// adminTxArm is one event table's contribution to a merged page. `table`,
	// `columns` and `exclude` are literals from the two slices below and never
	// carry request input; only the keyset bounds and the contract filter are
	// bound as parameters.
	adminTxArm struct {
		kind          string
		table         string
		columns       string
		searchColumns []string
		exclude       string
	}
)

const (
	// adminTxSeqScale packs (tx_id, log_index) into the single numeric cursor
	// these endpoints speak.
	//
	// Each event table carries its own identity sequence, so an `id` from
	// token_mint is not comparable with an `id` from token_transfer and cannot
	// order a merged page — which is what made the previous cursor interleave
	// rows out of chain order. (tx_id, log_index) is comparable across every
	// table and *is* chain order: tx.id is assigned in block order, log_index
	// orders the events inside one transaction.
	//
	// A log index cannot reach the scale. A LOG opcode costs at least 375 gas,
	// so no block can hold a million of them. The packed value also stays
	// inside float64's exact integer range (2^31 * 10^6 ~ 2.1e15 < 2^53), which
	// matters because the cursor travels through JSON and a browser URL.
	adminTxSeqScale = 1_000_000

	// The mint source and burn destination in an ERC-20 Transfer log.
	adminTxZeroAddress = "0x0000000000000000000000000000000000000000"
)

var (
	adminVoucherArms = []adminTxArm{
		{
			kind:          "transfer",
			table:         "token_transfer",
			columns:       "ev.contract_address AS voucher, ev.sender_address AS from_address, ev.recipient_address AS to_address, ev.transfer_value::text AS amount",
			searchColumns: []string{"ev.contract_address", "ev.sender_address", "ev.recipient_address"},
			// _mint and _burn each emit a Transfer log with the zero address on one
			// side, so every mint and every burn is in token_transfer as well as in
			// its own table. Dropping those two rows here is what keeps one movement
			// to one row; without it a mint appears twice, once with no minter.
			exclude: "ev.sender_address <> '" + adminTxZeroAddress + "' AND ev.recipient_address <> '" + adminTxZeroAddress + "'",
		},
		{
			kind:          "mint",
			table:         "token_mint",
			columns:       "ev.contract_address AS voucher, ev.minter_address AS from_address, ev.recipient_address AS to_address, ev.mint_value::text AS amount",
			searchColumns: []string{"ev.contract_address", "ev.minter_address", "ev.recipient_address"},
		},
		{
			kind:          "burn",
			table:         "token_burn",
			columns:       "ev.contract_address AS voucher, ev.burner_address AS from_address, NULL::text AS to_address, ev.burn_value::text AS amount",
			searchColumns: []string{"ev.contract_address", "ev.burner_address"},
		},
	}

	adminPoolArms = []adminTxArm{
		{
			kind:          "swap",
			table:         "pool_swap",
			columns:       "ev.contract_address AS pool, ev.initiator_address AS initiator, ev.token_in_address AS token_in, ev.token_out_address AS token_out, ev.in_value::text AS amount_in, ev.out_value::text AS amount_out, ev.fee::text AS fee",
			searchColumns: []string{"ev.contract_address", "ev.initiator_address", "ev.token_in_address", "ev.token_out_address"},
		},
		{
			kind:          "deposit",
			table:         "pool_deposit",
			columns:       "ev.contract_address AS pool, ev.initiator_address AS initiator, ev.token_in_address AS token_in, NULL::text AS token_out, ev.in_value::text AS amount_in, NULL::text AS amount_out, NULL::text AS fee",
			searchColumns: []string{"ev.contract_address", "ev.initiator_address", "ev.token_in_address"},
		},
	}
)

func adminTxSeq(txID, logIndex int64) uint64 {
	return uint64(txID*adminTxSeqScale + logIndex)
}

func splitAdminTxSeq(seq uint64) (txID, logIndex int64) {
	return int64(seq / adminTxSeqScale), int64(seq % adminTxSeqScale)
}

// AdminVoucherTxKinds is the set of `type` values the voucher page accepts, in
// the order the console offers them. Derived from the arms so a new event table
// cannot be listed without also being filterable.
func AdminVoucherTxKinds() []string { return adminTxKinds(adminVoucherArms) }

// AdminPoolTxKinds is the same for the pool page.
func AdminPoolTxKinds() []string { return adminTxKinds(adminPoolArms) }

func adminTxKinds(arms []adminTxArm) []string {
	kinds := make([]string, 0, len(arms))
	for _, arm := range arms {
		kinds = append(kinds, arm.kind)
	}
	return kinds
}

// selectAdminTxArms reads and merges one page across the arms of a surface.
//
// Each arm is queried for its own top `limit` rows, which is what makes the
// merge correct: the page's top `limit` rows can only come from those sets. The
// envelope is joined in the arm rather than fetched afterwards, so a page that
// is sparse in time — everything filtered to one rarely-used voucher — costs the
// same as a dense one.
func selectAdminTxArms[T any](
	ctx context.Context,
	s *FederationPg,
	arms []adminTxArm,
	kind, contract string,
	search AdminTransactionSearch,
	cursor *uint64,
	descending bool,
	limit uint64,
	stamp func(*T, string),
	key func(T) (int64, int64),
) ([]T, error) {
	merged := make([]T, 0, limit)

	for _, arm := range arms {
		if kind != "" && arm.kind != kind {
			continue
		}
		rows, err := selectAdminTxArm[T](ctx, s, arm, contract, search, cursor, descending, limit)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			stamp(&rows[i], arm.kind)
		}
		merged = append(merged, rows...)
	}

	sort.SliceStable(merged, func(i, j int) bool {
		leftTx, leftLog := key(merged[i])
		rightTx, rightLog := key(merged[j])
		if leftTx != rightTx {
			if descending {
				return leftTx > rightTx
			}
			return leftTx < rightTx
		}
		if descending {
			return leftLog > rightLog
		}
		return leftLog < rightLog
	})
	if uint64(len(merged)) > limit {
		merged = merged[:limit]
	}
	// A previous page is read ascending away from the cursor, so it comes back
	// oldest first. Rows always leave here newest first, whichever way the
	// caller stepped.
	if !descending {
		for i, j := 0, len(merged)-1; i < j; i, j = i+1, j-1 {
			merged[i], merged[j] = merged[j], merged[i]
		}
	}
	return merged, nil
}

func selectAdminTxArm[T any](
	ctx context.Context,
	s *FederationPg,
	arm adminTxArm,
	contract string,
	search AdminTransactionSearch,
	cursor *uint64,
	descending bool,
	limit uint64,
) ([]T, error) {
	var (
		args    []any
		clauses []string
	)
	param := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	if arm.exclude != "" {
		clauses = append(clauses, arm.exclude)
	}
	if cursor != nil {
		txID, logIndex := splitAdminTxSeq(*cursor)
		txParam, logParam := param(txID), param(logIndex)
		// Written as two predicates rather than the row-constructor form
		// `(tx_id, log_index) < ($1, $2)`: postgres_fdw does not ship a row
		// comparison, and the arm would then read its whole table locally. The
		// leading tx_id bound is what the remote index scan uses.
		if descending {
			clauses = append(clauses, fmt.Sprintf("ev.tx_id <= %s AND (ev.tx_id < %s OR ev.log_index < %s)", txParam, txParam, logParam))
		} else {
			clauses = append(clauses, fmt.Sprintf("ev.tx_id >= %s AND (ev.tx_id > %s OR ev.log_index > %s)", txParam, txParam, logParam))
		}
	}
	if contract != "" {
		clauses = append(clauses, "ev.contract_address = "+param(contract))
	}
	if predicate := search.predicate(
		append([]string{"tx.tx_hash"}, arm.searchColumns...),
		func(value string) string { return param(value) },
		func() string { return fmt.Sprintf("$%d", len(args)) },
	); predicate != "" {
		clauses = append(clauses, predicate)
	}

	order := "DESC"
	if !descending {
		order = "ASC"
	}

	query := "SELECT ev.tx_id, ev.log_index, tx.tx_hash, tx.date_block AS t, tx.success, " + arm.columns +
		" FROM " + arm.table + " ev JOIN tx ON tx.id = ev.tx_id"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY ev.tx_id " + order + ", ev.log_index " + order + " LIMIT " + param(int64(limit))

	var rows []T
	if err := pgxscan.Select(ctx, s.db, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *FederationPg) ListAdminVoucherTransactions(ctx context.Context, cursor *uint64, limit uint64, next bool, kind, voucher string, search AdminTransactionSearch) ([]AdminVoucherTransaction, error) {
	rows, err := selectAdminTxArms(ctx, s, adminVoucherArms, kind, voucher, search, cursor, cursor == nil || next, limit,
		func(row *AdminVoucherTransaction, armKind string) {
			row.Type = armKind
			row.ID = adminTxSeq(row.TxID, row.LogIndex)
		},
		func(row AdminVoucherTransaction) (int64, int64) { return row.TxID, row.LogIndex })
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *FederationPg) ListAdminPoolTransactions(ctx context.Context, cursor *uint64, limit uint64, next bool, kind, pool string, search AdminTransactionSearch) ([]AdminPoolTransaction, error) {
	rows, err := selectAdminTxArms(ctx, s, adminPoolArms, kind, pool, search, cursor, cursor == nil || next, limit,
		func(row *AdminPoolTransaction, armKind string) {
			row.Type = armKind
			row.ID = adminTxSeq(row.TxID, row.LogIndex)
		},
		func(row AdminPoolTransaction) (int64, int64) { return row.TxID, row.LogIndex })
	if err != nil {
		return nil, err
	}
	return rows, nil
}
