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
		"list-user-token-holdings":                        queries.ListUserTokenHoldings,
		"list-user-token-holdings-next":                   queries.ListUserTokenHoldingsNext,
		"list-user-token-holdings-prev":                   queries.ListUserTokenHoldingsPrevious,
		"list-user-transfer-history":                      queries.ListUserTransferHistory,
		"list-user-transfer-history-next":                 queries.ListUserTransferHistoryNext,
		"list-user-transfer-history-prev":                 queries.ListUserTransferHistoryPrevious,
		"create-account-token-holding-mv":                 queries.CreateAccountTokenHoldingMV,
		"create-account-token-holding-holder-token-idx":   queries.CreateAccountTokenHoldingHolderTokenIdx,
		"create-account-token-holding-holder-last-tx-idx": queries.CreateAccountTokenHoldingHolderLastTxIdx,
		"create-account-transfer-mv":                      queries.CreateAccountTransferMV,
		"create-account-transfer-holder-transfer-idx":     queries.CreateAccountTransferHolderTransferIdx,
		"refresh-account-token-holding-mv":                queries.RefreshAccountTokenHoldingMV,
		"refresh-account-transfer-mv":                     queries.RefreshAccountTransferMV,
	} {
		if query == "" {
			t.Fatalf("expected %s query to be loaded", name)
		}
	}

	if !strings.Contains(queries.CreateAccountTokenHoldingMV, "account_token_holding") {
		t.Fatal("expected holdings MV DDL to target account_token_holding")
	}
	if !strings.Contains(queries.CreateAccountTransferMV, "account_transfer") {
		t.Fatal("expected transfer MV DDL to target account_transfer")
	}
	if !strings.Contains(queries.RefreshAccountTokenHoldingMV, "CONCURRENTLY") {
		t.Fatal("expected holdings refresh to use CONCURRENTLY")
	}
}
