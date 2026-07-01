package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
)

type (
	FederationTokenBalance struct {
		ID            uint64    `db:"id"              json:"id"`
		TokenAddress  string    `db:"token_address"   json:"tokenAddress"`
		TokenSymbol   string    `db:"token_symbol"    json:"tokenSymbol"`
		TokenName     string    `db:"token_name"      json:"tokenName"`
		TokenDecimals uint8     `db:"token_decimals"  json:"tokenDecimals"`
		Balance       string    `db:"balance"         json:"balance"`
		IconURL       *string   `db:"icon_url"        json:"iconUrl,omitempty"`
		LastDateBlock time.Time `db:"last_date_block" json:"lastDateBlock"`
	}

	FederationTransfer struct {
		ID               uint64    `db:"id"                   json:"id"`
		TxHash           string    `db:"tx_hash"              json:"txHash"`
		DateBlock        time.Time `db:"date_block"           json:"dateBlock"`
		Direction        string    `db:"direction"            json:"direction"`
		Counterparty     string    `db:"counterparty_address" json:"counterpartyAddress"`
		SenderAddress    string    `db:"sender_address"       json:"senderAddress"`
		RecipientAddress string    `db:"recipient_address"    json:"recipientAddress"`
		TokenAddress     string    `db:"contract_address"     json:"tokenAddress"`
		TokenSymbol      string    `db:"token_symbol"         json:"tokenSymbol"`
		TokenName        string    `db:"token_name"           json:"tokenName"`
		TokenDecimals    uint8     `db:"token_decimals"       json:"tokenDecimals"`
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
