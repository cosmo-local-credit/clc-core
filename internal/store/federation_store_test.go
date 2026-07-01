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
}
