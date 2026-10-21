package store

import (
	"regexp"
	"strings"
	"testing"
)

func TestFederationStoreInterfaceCompile(t *testing.T) {
	var _ FederationStore = (*FederationPg)(nil)
}

func TestLoadFederationQueries(t *testing.T) {
	queries, err := loadFederationQueries("../../federated_queries.sql")
	if err != nil {
		t.Fatalf("load federation queries: %v", err)
	}
	if queries.LegacyUserExistsByBlockchainAddress == "" {
		t.Fatal("expected legacy user lookup query to be loaded")
	}
	if !strings.Contains(queries.LegacyUserExistsByBlockchainAddress, "sarafu_network.accounts") {
		t.Fatal("expected legacy user lookup query to target sarafu_network.accounts")
	}
	for name, query := range map[string]string{
		"list-user-token-holdings":        queries.ListUserTokenHoldings,
		"list-user-token-holdings-next":   queries.ListUserTokenHoldingsNext,
		"list-user-token-holdings-prev":   queries.ListUserTokenHoldingsPrevious,
		"list-user-token-balances":        queries.ListUserTokenBalances,
		"list-user-transfer-history":      queries.ListUserTransferHistory,
		"list-user-transfer-history-next": queries.ListUserTransferHistoryNext,
		"list-user-transfer-history-prev": queries.ListUserTransferHistoryPrevious,
		"list-user-activity":              queries.ListUserActivity,
		"dashboard-transaction-series":    queries.DashboardTransactionSeries,
	} {
		if query == "" {
			t.Fatalf("expected %s query to be loaded", name)
		}
	}
	if !strings.Contains(queries.DashboardTransactionSeries, "generate_series") {
		t.Fatal("expected transaction series to fill buckets in SQL")
	}
	if !regexp.MustCompile(`\bFROM\s+tx\b`).MatchString(queries.DashboardTransactionSeries) {
		t.Fatal("expected transaction series to read the indexer tx table")
	}
	if strings.Contains(queries.DashboardTransactionSeries, "chain_data_v2.") {
		t.Fatal("federation queries must not hard-code a schema; the DSN search_path selects it")
	}
	if !strings.Contains(queries.DashboardTransactionSeries, "extract(epoch from $1::timestamp)") {
		t.Fatal("expected extract() to cast $1 to timestamp; untyped params are ambiguous")
	}
	for _, table := range []string{
		"token_transfer",
		"pool_swap",
		"token_mint",
		"token_burn",
		"pool_deposit",
		"token_approval",
		"sarafu_network",
	} {
		if strings.Contains(queries.DashboardTransactionSeries, table) {
			t.Fatalf("transaction series must read only tx, found %s", table)
		}
	}

	// Reads must hit the live source tables, not the retired materialized views.
	for name, query := range map[string]string{
		"list-user-token-holdings":   queries.ListUserTokenHoldings,
		"list-user-token-balances":   queries.ListUserTokenBalances,
		"list-user-transfer-history": queries.ListUserTransferHistory,
	} {
		if !regexp.MustCompile(`\bFROM\s+token_transfer\s+tt\b`).MatchString(query) {
			t.Fatalf("expected %s to read live from token_transfer", name)
		}
		if strings.Contains(query, "chain_data_v2.") {
			t.Fatalf("expected %s not to hard-code a schema", name)
		}
		if strings.Contains(query, "account_token_holding") || strings.Contains(query, "account_transfer") {
			t.Fatalf("expected %s not to reference a materialized view", name)
		}
	}

	// Federation queries return chain facts only: token metadata is enriched
	// from the local vouchers catalog, so the old metadata joins must not
	// come back.
	for name, query := range map[string]string{
		"list-user-token-holdings":        queries.ListUserTokenHoldings,
		"list-user-token-holdings-next":   queries.ListUserTokenHoldingsNext,
		"list-user-token-holdings-prev":   queries.ListUserTokenHoldingsPrevious,
		"list-user-token-balances":        queries.ListUserTokenBalances,
		"list-user-transfer-history":      queries.ListUserTransferHistory,
		"list-user-activity":              queries.ListUserActivity,
		"list-user-transfer-history-next": queries.ListUserTransferHistoryNext,
		"list-user-transfer-history-prev": queries.ListUserTransferHistoryPrevious,
	} {
		if strings.Contains(query, "JOIN tokens") || strings.Contains(query, "FROM tokens") {
			t.Fatalf("expected %s not to join tokens (metadata is local now)", name)
		}
		if strings.Contains(query, "sarafu_network.vouchers") {
			t.Fatalf("expected %s not to join sarafu_network.vouchers (metadata is local now)", name)
		}
	}
}
