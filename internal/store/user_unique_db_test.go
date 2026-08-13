package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the shape of a duplicate-phone failure — SQLSTATE 23505 plus
// the users_phone_unique constraint name, still reachable through scany's error
// wrapping. The API layer keys its 409 + E18 response off exactly those two
// fields (see userIdentityConflict), so a rename here must break this test
// rather than silently regress the endpoint to a 500 E01.
//
// Skipped unless CORE_TEST_DSN points at a live database.

func TestUpdateUserProfileDuplicatePhoneIsUniqueViolation(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	holderAddr := "0x" + strings.Repeat("c", 40)
	claimantAddr := "0x" + strings.Repeat("d", 40)
	phone := "+254700111222"

	_, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: holderAddr,
		Name:            "Phone Holder",
		Email:           "phone-holder-test@example.com",
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)

	_, err = pg.InsertUser(ctx, tx, User{
		EthereumAddress: claimantAddr,
		Name:            "Phone Claimant",
		Email:           "phone-claimant-test@example.com",
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)

	_, err = pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: holderAddr, Name: "Phone Holder", Email: "phone-holder-test@example.com", Phone: phone, Country: "KE",
	})
	require.NoError(t, err)

	_, err = pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: claimantAddr, Name: "Phone Claimant", Email: "phone-claimant-test@example.com", Phone: phone, Country: "KE",
	})
	require.Error(t, err, "a phone already bound to another user must be rejected")

	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "unique violation must survive scany wrapping: %v", err)
	assert.Equal(t, "23505", pgErr.Code)
	assert.Equal(t, "users_phone_unique", pgErr.ConstraintName)
}

func TestUpdateUserProfileEmptyPhoneDoesNotCollide(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// phone is NULLIF('')'d by update-user-profile, and NULLs are distinct
	// under a UNIQUE constraint — so any number of users may leave it unset.
	for _, u := range []struct{ addr, email string }{
		{"0x" + strings.Repeat("e", 40), "no-phone-1-test@example.com"},
		{"0x" + strings.Repeat("f", 40), "no-phone-2-test@example.com"},
	} {
		_, err := pg.InsertUser(ctx, tx, User{
			EthereumAddress: u.addr,
			Name:            "No Phone",
			Email:           u.email,
			Role:            "USER",
			Status:          "APPROVED",
			MaxPools:        10,
			MaxTokens:       20,
		})
		require.NoError(t, err)

		updated, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
			Address: u.addr, Name: "No Phone", Email: u.email, Country: "KE",
		})
		require.NoError(t, err, "an unset phone must not collide with another unset phone")
		assert.Equal(t, "", updated.Phone)
	}
}
