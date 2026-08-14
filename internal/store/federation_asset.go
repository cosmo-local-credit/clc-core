package store

import (
	"context"
	"sort"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5/pgtype"
)

// naiveUTC binds a timestamptz Go value as timestamp without time zone.
// chain_data_v2.date_block is naive UTC; a timestamptz parameter drops the
// remote date filter and the FDW scan times out.
func naiveUTC(t time.Time) pgtype.Timestamp {
	return pgtype.Timestamp{Time: t.UTC(), Valid: true}
}

type (
	// FederationTokenBalance carries chain facts scanned from the federation
	// DB; the token metadata fields (db:"-") are enriched afterwards from the
	// local vouchers catalog (MIGRATION.md §2 — the catalog is the metadata
	// home). JSON shape is unchanged from the pre-cutover responses.
	FederationTokenBalance struct {
		ID            uint64    `db:"id"              json:"id"`
		TokenAddress  string    `db:"token_address"   json:"tokenAddress"`
		TokenSymbol   string    `db:"-"               json:"tokenSymbol"`
		TokenName     string    `db:"-"               json:"tokenName"`
		TokenDecimals uint8     `db:"-"               json:"tokenDecimals"`
		Balance       string    `db:"balance"         json:"balance"`
		IconURL       *string   `db:"-"               json:"iconUrl,omitempty"`
		LastDateBlock time.Time `db:"last_date_block" json:"lastDateBlock"`
	}

	// FederationTransfer: same split as FederationTokenBalance — chain facts
	// from federation, token metadata enriched from the local catalog.
	FederationTransfer struct {
		ID               uint64    `db:"id"                   json:"id"`
		TxHash           string    `db:"tx_hash"              json:"txHash"`
		DateBlock        time.Time `db:"date_block"           json:"dateBlock"`
		Direction        string    `db:"direction"            json:"direction"`
		Counterparty     string    `db:"counterparty_address" json:"counterpartyAddress"`
		SenderAddress    string    `db:"sender_address"       json:"senderAddress"`
		RecipientAddress string    `db:"recipient_address"    json:"recipientAddress"`
		TokenAddress     string    `db:"contract_address"     json:"tokenAddress"`
		TokenSymbol      string    `db:"-"                    json:"tokenSymbol"`
		TokenName        string    `db:"-"                    json:"tokenName"`
		TokenDecimals    uint8     `db:"-"                    json:"tokenDecimals"`
		Amount           string    `db:"amount"               json:"amount"`
		Success          bool      `db:"success"              json:"success"`
	}
)

func (s *FederationPg) ListUserTokenHoldings(ctx context.Context, address string, limit uint64) ([]FederationTokenBalance, error) {
	var holdings []FederationTokenBalance
	if err := pgxscan.Select(ctx, s.db, &holdings, s.queries.ListUserTokenHoldings, address, limit); err != nil {
		return nil, err
	}
	return holdings, nil
}

func (s *FederationPg) ListUserTokenHoldingsNext(ctx context.Context, address string, cursor uint64, limit uint64) ([]FederationTokenBalance, error) {
	var holdings []FederationTokenBalance
	if err := pgxscan.Select(ctx, s.db, &holdings, s.queries.ListUserTokenHoldingsNext, address, cursor, limit); err != nil {
		return nil, err
	}
	return holdings, nil
}

func (s *FederationPg) ListUserTokenHoldingsPrevious(ctx context.Context, address string, cursor uint64, limit uint64) ([]FederationTokenBalance, error) {
	var holdings []FederationTokenBalance
	if err := pgxscan.Select(ctx, s.db, &holdings, s.queries.ListUserTokenHoldingsPrevious, address, cursor, limit); err != nil {
		return nil, err
	}
	return holdings, nil
}

func (s *FederationPg) DashboardTransactionSeries(ctx context.Context, since, until time.Time, bucketSeconds int64) ([]DashboardTransactionPoint, error) {
	var rows []DashboardTransactionPoint
	if err := pgxscan.Select(ctx, s.db, &rows, s.queries.DashboardTransactionSeries, naiveUTC(since), naiveUTC(until), bucketSeconds); err != nil {
		return nil, err
	}
	if rows == nil {
		return []DashboardTransactionPoint{}, nil
	}
	return rows, nil
}

const dashboardTxIDSentinel int64 = 2_147_483_647

type dashboardEventSpec struct {
	table  string
	amount string
}

var dashboardEventSpecs = map[string]dashboardEventSpec{
	"transfer": {table: "chain_data_v2.token_transfer", amount: "transfer_value"},
	"swap":     {table: "chain_data_v2.pool_swap", amount: "in_value"},
	"mint":     {table: "chain_data_v2.token_mint", amount: "mint_value"},
	"burn":     {table: "chain_data_v2.token_burn", amount: "burn_value"},
	"deposit":  {table: "chain_data_v2.pool_deposit", amount: "in_value"},
	"faucet":   {table: "chain_data_v2.faucet_give", amount: "give_value"},
}

func (s *FederationPg) ListDashboardTransactions(ctx context.Context, cursor *uint64, limit uint64, next bool, kind, voucher string) ([]DashboardTransaction, error) {
	if kind != "" || voucher != "" {
		return s.listDashboardTransactionsByEvent(ctx, cursor, limit, next, kind, voucher)
	}
	return s.listDashboardTransactionsByEnvelope(ctx, cursor, limit, next)
}

func (s *FederationPg) listDashboardTransactionsByEnvelope(ctx context.Context, cursor *uint64, limit uint64, next bool) ([]DashboardTransaction, error) {
	bound := dashboardTxIDSentinel
	query := s.queries.DashboardTransactionPage
	if cursor != nil {
		bound = int64(*cursor)
		if !next {
			query = s.queries.DashboardTransactionPagePrev
		}
	}
	var envelopes []dashboardTxEnvelope
	if err := pgxscan.Select(ctx, s.db, &envelopes, query, bound, int64(limit)); err != nil {
		return nil, err
	}
	if len(envelopes) == 0 {
		return []DashboardTransaction{}, nil
	}
	minID, maxID := envelopes[len(envelopes)-1].ID, envelopes[0].ID
	if minID > maxID {
		minID, maxID = maxID, minID
	}
	var events []dashboardTxEvent
	if err := pgxscan.Select(ctx, s.db, &events, s.queries.DashboardTransactionEvents, minID, maxID); err != nil {
		return nil, err
	}
	first := make(map[int64]dashboardTxEvent, len(events))
	for _, ev := range events {
		if _, ok := first[ev.TxID]; !ok {
			first[ev.TxID] = ev
		}
	}
	out := make([]DashboardTransaction, 0, len(envelopes))
	for _, env := range envelopes {
		row := DashboardTransaction{
			ID:      env.ID,
			TxID:    env.ID,
			TxHash:  env.TxHash,
			T:       env.T,
			Success: env.Success,
			Type:    "other",
		}
		if ev, ok := first[env.ID]; ok {
			row.Type = ev.Kind
			row.Voucher = ev.Voucher
			row.Amount = ev.Amount
		}
		out = append(out, row)
	}
	return out, nil
}

type dashboardEventRow struct {
	ID      int64   `db:"id"`
	TxID    int64   `db:"tx_id"`
	Voucher *string `db:"voucher"`
	Amount  *string `db:"amount"`
	Kind    string
}

var dashboardEventKindOrder = []string{"transfer", "swap", "mint", "burn", "deposit", "faucet"}

func (s *FederationPg) listDashboardTransactionsByEvent(ctx context.Context, cursor *uint64, limit uint64, next bool, kind, voucher string) ([]DashboardTransaction, error) {
	kinds := dashboardEventKindOrder
	if kind != "" {
		if _, ok := dashboardEventSpecs[kind]; !ok {
			return []DashboardTransaction{}, nil
		}
		kinds = []string{kind}
	}
	op, order := "<", "DESC"
	if cursor != nil && !next {
		op, order = ">", "ASC"
	}
	bound := dashboardTxIDSentinel
	if cursor != nil {
		bound = int64(*cursor)
	}
	var merged []dashboardEventRow
	for _, name := range kinds {
		spec := dashboardEventSpecs[name]
		q := "SELECT ev.id, ev.tx_id, ev.contract_address AS voucher, ev." + spec.amount + "::text AS amount FROM " + spec.table + " ev WHERE ev.id " + op + " $1"
		args := []any{bound}
		if voucher != "" {
			q += " AND ev.contract_address = $2 ORDER BY ev.id " + order + " LIMIT $3"
			args = append(args, voucher, int64(limit))
		} else {
			q += " ORDER BY ev.id " + order + " LIMIT $2"
			args = append(args, int64(limit))
		}
		var rows []dashboardEventRow
		if err := pgxscan.Select(ctx, s.db, &rows, q, args...); err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].Kind = name
		}
		merged = append(merged, rows...)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID > merged[j].ID })
	if len(merged) > int(limit) {
		merged = merged[:limit]
	}
	if len(merged) == 0 {
		return []DashboardTransaction{}, nil
	}
	minTx, maxTx := merged[0].TxID, merged[0].TxID
	for _, ev := range merged {
		if ev.TxID < minTx {
			minTx = ev.TxID
		}
		if ev.TxID > maxTx {
			maxTx = ev.TxID
		}
	}
	var envelopes []dashboardTxEnvelope
	if err := pgxscan.Select(ctx, s.db, &envelopes, s.queries.DashboardTransactionsByIDRange, minTx, maxTx); err != nil {
		return nil, err
	}
	byID := make(map[int64]dashboardTxEnvelope, len(envelopes))
	for _, env := range envelopes {
		byID[env.ID] = env
	}
	out := make([]DashboardTransaction, 0, len(merged))
	for _, ev := range merged {
		env := byID[ev.TxID]
		out = append(out, DashboardTransaction{
			ID:      ev.ID,
			TxID:    ev.TxID,
			TxHash:  env.TxHash,
			T:       env.T,
			Success: env.Success,
			Type:    ev.Kind,
			Voucher: ev.Voucher,
			Amount:  ev.Amount,
		})
	}
	return out, nil
}

func (s *FederationPg) ListUserTransferHistory(ctx context.Context, address string, limit uint64) ([]FederationTransfer, error) {
	var transfers []FederationTransfer
	if err := pgxscan.Select(ctx, s.db, &transfers, s.queries.ListUserTransferHistory, address, limit); err != nil {
		return nil, err
	}
	return transfers, nil
}

func (s *FederationPg) ListUserTransferHistoryNext(ctx context.Context, address string, cursor uint64, limit uint64) ([]FederationTransfer, error) {
	var transfers []FederationTransfer
	if err := pgxscan.Select(ctx, s.db, &transfers, s.queries.ListUserTransferHistoryNext, address, cursor, limit); err != nil {
		return nil, err
	}
	return transfers, nil
}

func (s *FederationPg) ListUserTransferHistoryPrevious(ctx context.Context, address string, cursor uint64, limit uint64) ([]FederationTransfer, error) {
	var transfers []FederationTransfer
	if err := pgxscan.Select(ctx, s.db, &transfers, s.queries.ListUserTransferHistoryPrevious, address, cursor, limit); err != nil {
		return nil, err
	}
	return transfers, nil
}
