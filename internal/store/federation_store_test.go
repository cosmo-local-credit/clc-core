package store

import (
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
		"list-user-transfer-history":      queries.ListUserTransferHistory,
		"list-user-transfer-history-next": queries.ListUserTransferHistoryNext,
		"list-user-transfer-history-prev": queries.ListUserTransferHistoryPrevious,
	} {
		if query == "" {
			t.Fatalf("expected %s query to be loaded", name)
		}
	}

	// Reads must hit the live source tables, not the retired materialized views.
	for name, query := range map[string]string{
		"list-user-token-holdings":   queries.ListUserTokenHoldings,
		"list-user-transfer-history": queries.ListUserTransferHistory,
	} {
		if !strings.Contains(query, "chain_data_v2.token_transfer") {
			t.Fatalf("expected %s to read live from chain_data_v2.token_transfer", name)
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
		"list-user-transfer-history":      queries.ListUserTransferHistory,
		"list-user-transfer-history-next": queries.ListUserTransferHistoryNext,
		"list-user-transfer-history-prev": queries.ListUserTransferHistoryPrevious,
	} {
		if strings.Contains(query, "chain_data_v2.tokens") {
			t.Fatalf("expected %s not to join chain_data_v2.tokens (metadata is local now)", name)
		}
		if strings.Contains(query, "sarafu_network.vouchers") {
			t.Fatalf("expected %s not to join sarafu_network.vouchers (metadata is local now)", name)
		}
	}
}
