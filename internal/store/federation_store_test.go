package store

import "testing"

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
}
