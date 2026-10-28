package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/georgysavva/scany/v2/pgxscan"
)

const (
	AccountActivityCreation = "creation"
	AccountActivitySent     = "sent"
	AccountActivityReceived = "received"
	AccountActivitySwapped  = "swapped"
)

var (
	accountActivityKindOrder = []string{
		AccountActivityCreation,
		AccountActivitySent,
		AccountActivityReceived,
		AccountActivitySwapped,
	}
)

type (
	AccountActivityFilter struct {
		Kinds          []string
		Search         CatalogFilter
		TokenAddresses []string
	}

	AccountActivityLeg struct {
		TokenAddress  string `json:"tokenAddress"`
		TokenName     string `json:"tokenName"`
		TokenSymbol   string `json:"tokenSymbol"`
		TokenDecimals uint8  `json:"tokenDecimals"`
		Amount        string `json:"amount"`
		UOAValue      string `json:"uoaValue,omitempty"`
		UOA           string `json:"uoa,omitempty"`
	}

	// FederationAccountActivity is the flat row returned by the federation query.
	// The API layer enriches its token legs and serializes the public tagged union.
	FederationAccountActivity struct {
		ID                  uint64              `db:"id"         json:"id"`
		Kind                string              `db:"kind"       json:"kind"`
		TxHash              string              `db:"tx_hash"    json:"txHash"`
		DateBlock           time.Time           `db:"date_block" json:"dateBlock"`
		Success             bool                `db:"success"    json:"success"`
		CounterpartyAddress *string             `db:"counterparty_address" json:"counterpartyAddress,omitempty"`
		SenderAddress       *string             `db:"sender_address"       json:"senderAddress,omitempty"`
		RecipientAddress    *string             `db:"recipient_address"    json:"recipientAddress,omitempty"`
		TokenAddress        *string             `db:"token_address"        json:"-"`
		Amount              *string             `db:"amount"               json:"-"`
		TokenInAddress      *string             `db:"token_in_address"     json:"-"`
		AmountIn            *string             `db:"amount_in"            json:"-"`
		TokenOutAddress     *string             `db:"token_out_address"    json:"-"`
		AmountOut           *string             `db:"amount_out"           json:"-"`
		Fee                 *string             `db:"fee"                  json:"-"`
		PoolAddresses       []string            `db:"pool_addresses"       json:"poolAddresses,omitempty"`
		Transfer            *AccountActivityLeg `db:"-" json:"transfer,omitempty"`
		Sent                *AccountActivityLeg `db:"-" json:"sent,omitempty"`
		Received            *AccountActivityLeg `db:"-" json:"received,omitempty"`
		ProtocolFee         *AccountActivityLeg `db:"-" json:"protocolFee,omitempty"`
		HasNote             *bool               `db:"-" json:"hasNote,omitempty"`
	}
)

// NormalizeAccountActivityKinds accepts comma-separated and repeated query
// values, deduplicating them into the stable order used by the app and cache.
func NormalizeAccountActivityKinds(values []string) ([]string, error) {
	requested := make(map[string]bool, len(values))
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			kind := strings.ToLower(strings.TrimSpace(part))
			if kind == "" {
				continue
			}
			switch kind {
			case AccountActivityCreation, AccountActivitySent, AccountActivityReceived, AccountActivitySwapped:
				requested[kind] = true
			default:
				return nil, errors.New("types contains an unsupported activity type")
			}
		}
	}
	result := make([]string, 0, len(requested))
	for _, kind := range accountActivityKindOrder {
		if requested[kind] {
			result = append(result, kind)
		}
	}
	return result, nil
}

func (s *FederationPg) ListUserActivity(
	ctx context.Context,
	address string,
	filter AccountActivityFilter,
	cursor *uint64,
	limit uint64,
	next bool,
) ([]FederationAccountActivity, error) {
	tokens := filter.TokenAddresses
	if tokens == nil {
		tokens = []string{}
	}
	kinds := filter.Kinds
	if kinds == nil {
		kinds = []string{}
	}
	var cursorArg any
	if cursor != nil {
		cursorArg = int64(*cursor)
	}
	var rows []FederationAccountActivity
	if err := pgxscan.Select(
		ctx,
		s.db,
		&rows,
		s.queries.ListUserActivity,
		address,
		kinds,
		tokens,
		filter.Search.Term,
		cursorArg,
		next,
		limit,
	); err != nil {
		return nil, err
	}
	return rows, nil
}

// LatestPoolReviewSwap returns the most recent successful, positive swap the
// account initiated through this pool. A multi-hop transaction contains one
// indexed row per pool, so every pool that actually executed a leg qualifies.
func (s *FederationPg) LatestPoolReviewSwap(
	ctx context.Context,
	poolAddress string,
	reviewerAddress string,
) (*time.Time, error) {
	var latest *time.Time
	err := s.db.QueryRow(
		ctx,
		s.queries.LatestPoolReviewSwap,
		indexedAddressVariants(poolAddress),
		indexedAddressVariants(reviewerAddress),
	).Scan(&latest)
	return latest, err
}

func indexedAddressVariants(address string) []string {
	lower := strings.ToLower(address)
	checksum := common.HexToAddress(address).Hex()
	if checksum == lower {
		return []string{lower}
	}
	return []string{lower, checksum}
}
