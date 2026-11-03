package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live-database coverage for RotateUserAddress (migration 069): one account
// moving from a replaced sign-in card to a new one. Every row the person wrote
// under the old address follows them, passkeys wait as 'moved' for the new
// wallet to register them, and the old address can never become an account
// again. Skipped unless CORE_TEST_DSN is set; runs in a rolled-back transaction.

func rotationTestAddress(t *testing.T) string {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	return crypto.PubkeyToAddress(key.PublicKey).Hex()
}

func insertRotationTestUser(t *testing.T, pg *Pg, tx pgx.Tx, address string) User {
	t.Helper()
	user, err := pg.InsertUser(context.Background(), tx, User{
		EthereumAddress: address,
		Name:            "Rotation test",
		Email:           "pending+" + strings.TrimPrefix(strings.ToLower(address), "0x") + "@clc.local",
		Role:            "USER",
		Status:          "APPROVED",
	})
	require.NoError(t, err)
	return user
}

func isRetiredAddress(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == "users_address_retired"
}

func TestRotateUserAddressMovesEverythingTheOwnerWrote(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	oldAddress, newAddress := rotationTestAddress(t), rotationTestAddress(t)
	oldLower, newLower := strings.ToLower(oldAddress), strings.ToLower(newAddress)
	user := insertRotationTestUser(t, pg, tx, oldAddress)
	username := "zqrot" + strings.ToLower(oldAddress[2:10])
	_, err := tx.Exec(ctx, `UPDATE users SET username = $1 WHERE id = $2`, username, user.ID)
	require.NoError(t, err)

	var voucherID, poolID int64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, owner_address)
		VALUES (100, $1, 'Rotation voucher', 'ZQROT', 6, true, $2) RETURNING id`,
		rotationTestAddress(t), oldAddress).Scan(&voucherID))
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pools (chain_id, address) VALUES (100, $1) RETURNING id`,
		rotationTestAddress(t)).Scan(&poolID))

	var reportID, voucherReviewID int64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO reports (title, body, author_address, modifier_address)
		VALUES ('Rotation report', '{}'::jsonb, $1, $1) RETURNING id`, oldAddress).Scan(&reportID))
	_, err = tx.Exec(ctx, `INSERT INTO report_verifiers (report_id, verifier_address) VALUES ($1, $2)`, reportID, oldAddress)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		INSERT INTO offerings (voucher_id, seller_address, commodity_name, quantity)
		VALUES ($1, $2, 'Rotation offering', 1)`, voucherID, oldAddress)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO voucher_reviews (voucher_id, reviewer_address, stars, reply, reply_author_address)
		VALUES ($1, $2, 5, 'Thanks', $2) RETURNING id`, voucherID, oldLower).Scan(&voucherReviewID))
	_, err = tx.Exec(ctx, `
		INSERT INTO pool_reviews (pool_id, reviewer_address, stars, reply, reply_author_address)
		VALUES ($1, $2, 4, 'Thanks', $2)`, poolID, oldLower)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		INSERT INTO voucher_review_flags (review_id, reporter_address, reason, reported_stars, reported_comment, resolved_at, resolved_by)
		VALUES ($1, $2, 'spam', 5, '', now(), $2)`, voucherReviewID, oldLower)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		INSERT INTO retirement_requests (voucher_id, requester_address, owner_address, reason, status, snapshot, decision_at, decision_by)
		VALUES ($1, $2, $2, 'done', 'approved', '{}'::jsonb, now(), $2)`, voucherID, oldLower)
	require.NoError(t, err)

	// One passkey that signs in today, and one whose registration reverted:
	// only the first is worth taking along.
	activeTracking, failedTracking := uuid.NewString(), uuid.NewString()
	for _, row := range []struct{ id, tracking, state string }{
		{"rotation-active", activeTracking, WebAuthnActive},
		{"rotation-failed", failedTracking, WebAuthnFailed},
	} {
		_, err = tx.Exec(ctx, `
			INSERT INTO webauthn_credentials (credential_id, address, key_hash, public_key_x, public_key_y, tracking_id, state)
			VALUES ($1, $2, $3, 1, 2, $4::uuid, $5)`,
			row.id, oldAddress, "0x"+strings.Repeat("b", 64), row.tracking, row.state)
		require.NoError(t, err)
	}

	result, err := pg.RotateUserAddress(ctx, tx, AddressRotationRequest{
		UserID: user.ID, OldAddress: oldAddress, NewAddress: newAddress, InitiatedBy: RotationByUser,
	})
	require.NoError(t, err)
	assert.False(t, result.Replayed)
	assert.Equal(t, user.ID, result.User.ID)
	assert.Equal(t, newAddress, result.User.EthereumAddress)
	assert.Equal(t, "pending+"+newLower[2:]+"@clc.local", result.User.Email)
	require.Len(t, result.MovedPasskeys, 1)
	assert.Equal(t, "rotation-active", result.MovedPasskeys[0].CredentialID)
	assert.Equal(t, WebAuthnMoved, result.MovedPasskeys[0].State)

	var claimOwner string
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT owner_key FROM identity_claims WHERE namespace = 'recipient' AND name = $1`, username).Scan(&claimOwner))
	assert.Equal(t, newLower, claimOwner, "the username follows the account")

	type reference struct{ query, want string }
	for _, ref := range []reference{
		{`SELECT author_address FROM reports WHERE id = $1`, newAddress},
		{`SELECT modifier_address FROM reports WHERE id = $1`, newAddress},
		{`SELECT verifier_address FROM report_verifiers WHERE report_id = $1`, newAddress},
	} {
		var got string
		require.NoError(t, tx.QueryRow(ctx, ref.query, reportID).Scan(&got), ref.query)
		assert.Equal(t, ref.want, got, ref.query)
	}
	for _, ref := range []reference{
		{`SELECT seller_address FROM offerings WHERE voucher_id = $1`, newAddress},
		{`SELECT reviewer_address FROM voucher_reviews WHERE voucher_id = $1`, newLower},
		{`SELECT reply_author_address FROM voucher_reviews WHERE voucher_id = $1`, newLower},
		{`SELECT requester_address FROM retirement_requests WHERE voucher_id = $1`, newLower},
		{`SELECT owner_address FROM retirement_requests WHERE voucher_id = $1`, newLower},
		{`SELECT decision_by FROM retirement_requests WHERE voucher_id = $1`, newLower},
		{`SELECT reporter_address FROM voucher_review_flags WHERE review_id = (SELECT id FROM voucher_reviews WHERE voucher_id = $1)`, newLower},
		{`SELECT resolved_by FROM voucher_review_flags WHERE review_id = (SELECT id FROM voucher_reviews WHERE voucher_id = $1)`, newLower},
	} {
		var got string
		require.NoError(t, tx.QueryRow(ctx, ref.query, voucherID).Scan(&got), ref.query)
		assert.Equal(t, ref.want, got, ref.query)
	}
	for _, column := range []string{"reviewer_address", "reply_author_address"} {
		var got string
		require.NoError(t, tx.QueryRow(ctx, `SELECT `+column+` FROM pool_reviews WHERE pool_id = $1`, poolID).Scan(&got))
		assert.Equal(t, newLower, got, "pool_reviews."+column)
	}

	// Ownership is the chain's to change: the catalog keeps the old owner until
	// the client's transferOwnership is indexed.
	var voucherOwner string
	require.NoError(t, tx.QueryRow(ctx, `SELECT owner_address FROM vouchers WHERE id = $1`, voucherID).Scan(&voucherOwner))
	assert.Equal(t, oldAddress, voucherOwner)

	failed, err := pg.GetWebAuthnCredential(ctx, tx, "rotation-failed")
	require.NoError(t, err)
	assert.Equal(t, oldAddress, failed.Address, "a passkey that never worked stays behind")
	_, err = pg.GetActiveWebAuthnCredential(ctx, tx, "rotation-active")
	assert.ErrorIs(t, err, pgx.ErrNoRows, "a moved passkey must not sign in before the new wallet registers it")

	retired, err := pg.AddressIsRetired(ctx, tx, strings.ToLower(oldAddress))
	require.NoError(t, err)
	assert.True(t, retired)
	rotations, err := pg.ListAddressRotationsByUser(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Len(t, rotations, 1)
	assert.Equal(t, oldAddress, rotations[0].OldAddress)

	// The old card can never hold an account again, in any casing.
	_, err = tx.Exec(ctx, `SAVEPOINT reinsert`)
	require.NoError(t, err)
	_, err = pg.InsertUser(ctx, tx, User{
		EthereumAddress: oldLower, Name: "Reused", Email: "reused@example.invalid", Role: "USER", Status: "APPROVED",
	})
	assert.True(t, isRetiredAddress(err), "insert of a retired address: %v", err)
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT reinsert`)
	require.NoError(t, err)

	// An identical retry answers from the committed rotation.
	replayed, err := pg.RotateUserAddress(ctx, tx, AddressRotationRequest{
		UserID: user.ID, OldAddress: oldAddress, NewAddress: newAddress, InitiatedBy: RotationByUser,
	})
	require.NoError(t, err)
	assert.True(t, replayed.Replayed)
	assert.Equal(t, result.Rotation.ID, replayed.Rotation.ID)
	require.Len(t, replayed.MovedPasskeys, 1)

	// The same old card cannot be pointed anywhere else afterwards.
	_, err = pg.RotateUserAddress(ctx, tx, AddressRotationRequest{
		UserID: user.ID, OldAddress: oldAddress, NewAddress: rotationTestAddress(t), InitiatedBy: RotationByUser,
	})
	assert.ErrorIs(t, err, ErrAddressRetired)
}

func TestMovedPasskeyReRegistersOnTheNewWallet(t *testing.T) {
	env, cleanup := newWebAuthnTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	oldAddress, newAddress := rotationTestAddress(t), rotationTestAddress(t)
	user := insertRotationTestUser(t, env.pg, env.tx, oldAddress)
	tracking := uuid.NewString()
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("rotation-relink", oldAddress, tracking)))
	env.setStatus(env.relay(tracking, IN_NETWORK), SUCCESS)
	require.True(t, env.usable("rotation-relink"))

	_, err := env.pg.RotateUserAddress(ctx, env.tx, AddressRotationRequest{
		UserID: user.ID, OldAddress: oldAddress, NewAddress: newAddress, InitiatedBy: RotationByUser,
	})
	require.NoError(t, err)
	assert.Equal(t, WebAuthnMoved, env.state("rotation-relink"))

	// The new wallet's root key registering the same key re-arms the row; a
	// different wallet claiming it is still a conflict.
	cred := webAuthnTestCred("rotation-relink", rotationTestAddress(t), uuid.NewString())
	assert.ErrorIs(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, cred), ErrWebAuthnCredentialConflict)

	relink := uuid.NewString()
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("rotation-relink", newAddress, relink)))
	assert.Equal(t, WebAuthnPending, env.state("rotation-relink"))
	env.setStatus(env.relay(relink, IN_NETWORK), SUCCESS)
	assert.True(t, env.usable("rotation-relink"))
}

func TestRotateUserAddressRefusesUnsafeMoves(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	oldAddress := rotationTestAddress(t)
	user := insertRotationTestUser(t, pg, tx, oldAddress)
	taken := insertRotationTestUser(t, pg, tx, rotationTestAddress(t))

	_, err := pg.RotateUserAddress(ctx, tx, AddressRotationRequest{
		UserID: user.ID, OldAddress: oldAddress, NewAddress: taken.EthereumAddress, InitiatedBy: RotationByUser,
	})
	assert.ErrorIs(t, err, ErrAddressRotationTargetTaken)

	_, err = pg.RotateUserAddress(ctx, tx, AddressRotationRequest{
		UserID: taken.ID, OldAddress: oldAddress, NewAddress: rotationTestAddress(t), InitiatedBy: RotationByUser,
	})
	assert.ErrorIs(t, err, ErrAddressRotationStale, "another account's card")

	// A deploy job with no outcome yet would be cataloged under the old card.
	_, err = tx.Exec(ctx, `INSERT INTO user_deployments (user_id, tracking_id, resource_type) VALUES ($1, $2::uuid, 'TOKEN')`,
		user.ID, uuid.NewString())
	require.NoError(t, err)
	_, err = pg.RotateUserAddress(ctx, tx, AddressRotationRequest{
		UserID: user.ID, OldAddress: oldAddress, NewAddress: rotationTestAddress(t), InitiatedBy: RotationByUser,
	})
	assert.ErrorIs(t, err, ErrAddressRotationDeploying)

	var current string
	require.NoError(t, tx.QueryRow(ctx, `SELECT blockchain_address FROM users WHERE id = $1`, user.ID).Scan(&current))
	assert.Equal(t, common.HexToAddress(oldAddress).Hex(), current, "a refused rotation changes nothing")
}

// Every account insert passes the retired-address trigger, and a recipient
// lookup asks the same question, so both must stay index lookups as rotations
// accumulate.
func TestAddressRotationLookupsUseTheOldAddressIndex(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	marker := "rot" + uuid.NewString()[:8]
	_, err := tx.Exec(ctx, `
		WITH seeded AS (
			INSERT INTO users (blockchain_address, name, email, status)
			SELECT '0x' || md5($1 || i::TEXT) || '00000000', 'Plan user', $1 || i::TEXT || '@example.test', 'APPROVED'
			FROM generate_series(1, 10000) i
			RETURNING id, blockchain_address
		)
		INSERT INTO address_rotations (user_id, old_address, new_address, initiated_by)
		SELECT id, '0x' || md5('old' || blockchain_address) || '00000000', blockchain_address, 'user' FROM seeded`, marker)
	require.NoError(t, err)
	for _, statement := range []string{"ANALYZE users", "ANALYZE address_rotations", "SET LOCAL plan_cache_mode = force_generic_plan"} {
		_, err = tx.Exec(ctx, statement)
		require.NoError(t, err)
	}

	for name, query := range map[string]string{
		"retired":     pg.queries.AddressIsRetired,
		"by_old":      pg.queries.GetAddressRotationByOld,
		"trigger_sql": `SELECT 1 FROM address_rotations WHERE lower(old_address) = lower($1::TEXT)`,
	} {
		t.Run(name, func(t *testing.T) {
			statement := "rotation_plan_" + name
			_, err := tx.Exec(ctx, "PREPARE "+statement+" AS "+query)
			require.NoError(t, err)
			defer func() {
				if _, err := tx.Exec(ctx, "DEALLOCATE "+statement); err != nil {
					t.Errorf("deallocate %s: %v", statement, err)
				}
			}()
			rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) EXECUTE "+statement+"('0x"+strings.Repeat("1", 40)+"')")
			require.NoError(t, err)
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)
			plan := strings.Join(lines, "\n")
			t.Log(plan)
			require.Contains(t, plan, "address_rotations_old_address_key")
		})
	}
}
