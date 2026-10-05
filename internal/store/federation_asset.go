package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5/pgtype"
)

// naiveUTC binds a timestamptz Go value as timestamp without time zone.
// The indexer's tx.date_block is naive UTC; a timestamptz parameter drops the
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
		// HasNote is filled by the API layer from clc-core's own database — the
		// federation DB knows nothing about private attachments.
		HasNote bool `db:"-"                    json:"hasNote"`
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
