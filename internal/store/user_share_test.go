package store

import (
	"strings"
	"testing"
)

func TestUserShareQueryMasksPIIByPermissionBits(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	for field, predicate := range map[string]string{
		"email":    "(us.share_permission & 1) = 1",
		"phone":    "(us.share_permission & 2) = 2",
		"location": "(us.share_permission & 4) = 4",
	} {
		if !strings.Contains(queries.GetUserShareByTxHash, predicate) {
			t.Errorf("%s must be masked by %q", field, predicate)
		}
	}
}
