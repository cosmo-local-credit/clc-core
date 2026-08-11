package api

import (
	"errors"
	"fmt"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestUserIdentityConflictPerConstraintCode(t *testing.T) {
	for _, tc := range []struct {
		constraint string
		wantCode   string
	}{
		{"users_email_unique", apiresp.ErrCodeEmailConflict},
		{"users_phone_unique", apiresp.ErrCodePhoneConflict},
		{"users_blockchain_address_unique", apiresp.ErrCodeConflict},
	} {
		// Wrapped the way scany surfaces it from ScanOne, so the mapping is
		// exercised through errors.As rather than a bare *pgconn.PgError.
		err := fmt.Errorf("scany: rows final error: %w", &pgconn.PgError{
			Code:           pgUniqueViolation,
			ConstraintName: tc.constraint,
		})

		resp, ok := userIdentityConflict(err)
		if !ok {
			t.Fatalf("%s: expected a conflict response", tc.constraint)
		}
		if resp.ErrCode != tc.wantCode {
			t.Fatalf("%s: expected err code %q, got %q", tc.constraint, tc.wantCode, resp.ErrCode)
		}
		if resp.Ok {
			t.Fatalf("%s: expected ok=false", tc.constraint)
		}
		if resp.Description == "" {
			t.Fatalf("%s: expected a description", tc.constraint)
		}
	}
}

func TestUserIdentityConflictIgnoresOtherErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("boom"),
		// A non-unique postgres failure must still surface as a 500.
		&pgconn.PgError{Code: "23514", ConstraintName: "users_geo_valid"},
	} {
		if _, ok := userIdentityConflict(err); ok {
			t.Fatalf("expected %v not to map to a conflict", err)
		}
	}
}
