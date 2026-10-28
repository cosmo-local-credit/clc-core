package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdentityAvailabilityMatchesSignupConflicts(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	address := "0x" + strings.Repeat("a", 40)
	email := "zqavail-aude@example.test"
	phone := "+254700009184"
	username := "Aude-Péronne Zqavail"

	_, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: address,
		Name:            "Aude Péronne",
		Email:           email,
		Role:            "USER",
		Status:          "APPROVED",
	})
	require.NoError(t, err)

	_, err = pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address:  address,
		Name:     "Aude Péronne",
		Email:    email,
		Phone:    phone,
		Country:  "KE",
		Username: &username,
	})
	require.NoError(t, err)

	poolName := "Aude-Péronne Pool Zqavail"
	poolKey := "42220:0x" + strings.Repeat("b", 40)
	_, err = tx.Exec(ctx, `
		INSERT INTO identity_claims (namespace, name, owner_kind, owner_key)
		VALUES ('recipient', normalize_identity_name($1), 'pool', $2)`, poolName, poolKey)
	require.NoError(t, err)

	taken, err := pg.IdentityAvailability(ctx, tx, &email, &phone, &username, nil)
	require.NoError(t, err)
	requireTaken(t, taken.Email, true)
	requireTaken(t, taken.Phone, true)
	requireTaken(t, taken.Username, true)

	// The store compares email exactly. Signup lowercases before insert and
	// before this read; a mixed-case lookup must not be treated as the same row.
	mixedEmail := "Zqavail-Aude@Example.Test"
	otherPhone := "+254700009185"
	foldedUsername := "  AUDE-péronne   zqavail "
	decomposed := strings.ReplaceAll(foldedUsername, "é", "e\u0301")
	free, err := pg.IdentityAvailability(ctx, tx, &mixedEmail, &otherPhone, &decomposed, nil)
	require.NoError(t, err)
	requireTaken(t, free.Email, false)
	requireTaken(t, free.Phone, false)
	requireTaken(t, free.Username, true)

	spacedPool := "  aude-péronne   pool zqavail "
	poolOnly, err := pg.IdentityAvailability(ctx, tx, nil, nil, &spacedPool, nil)
	require.NoError(t, err)
	require.Nil(t, poolOnly.Email)
	require.Nil(t, poolOnly.Phone)
	requireTaken(t, poolOnly.Username, true)

	freeName := "Aude-Péronne Zqavail 2"
	open, err := pg.IdentityAvailability(ctx, tx, nil, nil, &freeName, nil)
	require.NoError(t, err)
	require.Nil(t, open.Email)
	requireTaken(t, open.Username, false)
}

// What an account already holds is free to that account: onboarding returns to
// the profile after signup has created the user, and must not be blocked by it.
// The owner is compared case-insensitively, the way identity_claims keys users.
func TestIdentityAvailabilityFreesTheOwnersIdentities(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	address := "0x" + strings.Repeat("aB", 20)
	email := "zqavail-owner@example.test"
	phone := "+254700009187"
	username := "Aude-Péronne Zqavail Owner"

	_, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: address,
		Name:            "Aude Péronne",
		Email:           email,
		Role:            "USER",
		Status:          "APPROVED",
	})
	require.NoError(t, err)
	_, err = pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address:  address,
		Name:     "Aude Péronne",
		Email:    email,
		Phone:    phone,
		Country:  "KE",
		Username: &username,
	})
	require.NoError(t, err)

	poolName := "Aude-Péronne Pool Zqavail Owner"
	_, err = tx.Exec(ctx, `
		INSERT INTO identity_claims (namespace, name, owner_kind, owner_key)
		VALUES ('recipient', normalize_identity_name($1), 'pool', $2)`, poolName, strings.ToLower(address))
	require.NoError(t, err)

	owner := strings.ToLower(address)
	own, err := pg.IdentityAvailability(ctx, tx, &email, &phone, &username, &owner)
	require.NoError(t, err)
	requireTaken(t, own.Email, false)
	requireTaken(t, own.Phone, false)
	requireTaken(t, own.Username, false)

	other := "0x" + strings.Repeat("c", 40)
	theirs, err := pg.IdentityAvailability(ctx, tx, &email, &phone, &username, &other)
	require.NoError(t, err)
	requireTaken(t, theirs.Email, true)
	requireTaken(t, theirs.Phone, true)
	requireTaken(t, theirs.Username, true)

	// A pool keyed by the same text is not the user; only a user claim is freed.
	pool, err := pg.IdentityAvailability(ctx, tx, nil, nil, &poolName, &owner)
	require.NoError(t, err)
	requireTaken(t, pool.Username, true)
}

func requireTaken(t *testing.T, got *bool, want bool) {
	t.Helper()
	require.NotNil(t, got)
	require.Equal(t, want, *got)
}
