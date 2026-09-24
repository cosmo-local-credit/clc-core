package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/keypair"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live-database coverage for the WebAuthn credential lifecycle: a credential
// is usable for sign-in only once a transaction carrying its registration has
// succeeded. Skipped unless CORE_TEST_DSN points at a database; runs inside a
// rolled-back transaction.

type webAuthnTestEnv struct {
	t      *testing.T
	pg     *Pg
	tx     pgx.Tx
	signer string
	nonce  uint64
}

func newWebAuthnTestEnv(t *testing.T) (*webAuthnTestEnv, func()) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	signer := "0x" + strings.Repeat("e", 40)
	require.NoError(t, pg.InsertKeyPair(context.Background(), tx, keypair.Key{Public: signer, Private: "webauthn-test"}))
	return &webAuthnTestEnv{t: t, pg: pg, tx: tx, signer: signer}, cleanup
}

// relay inserts one OTX + dispatch row under trackingID, as the relay worker
// does, and returns the OTX id.
func (e *webAuthnTestEnv) relay(trackingID, status string) uint64 {
	e.t.Helper()
	e.nonce++
	id, err := e.pg.InsertOTX(context.Background(), e.tx, OTX{
		TrackingID:    trackingID,
		OTXType:       RELAY_DELEGATE_EXEC,
		SignerAccount: e.signer,
		RawTx:         "0x00",
		TxHash:        fmt.Sprintf("0x%064x", e.nonce),
		Nonce:         e.nonce,
	})
	require.NoError(e.t, err)
	require.NoError(e.t, e.pg.InsertDispatchTx(context.Background(), e.tx, DispatchTx{OTXID: id, Status: status}))
	return id
}

func (e *webAuthnTestEnv) setStatus(otxID uint64, status string) {
	e.t.Helper()
	require.NoError(e.t, e.pg.UpdateDispatchTxStatus(context.Background(), e.tx, DispatchTx{OTXID: otxID, Status: status}))
}

func (e *webAuthnTestEnv) state(credentialID string) string {
	e.t.Helper()
	cred, err := e.pg.GetWebAuthnCredential(context.Background(), e.tx, credentialID)
	require.NoError(e.t, err)
	return cred.State
}

func (e *webAuthnTestEnv) usable(credentialID string) bool {
	e.t.Helper()
	_, err := e.pg.GetActiveWebAuthnCredential(context.Background(), e.tx, credentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	require.NoError(e.t, err)
	return true
}

func webAuthnTestCred(credentialID, address, trackingID string) WebAuthnCredential {
	return WebAuthnCredential{
		CredentialID: credentialID,
		Address:      address,
		KeyHash:      "0x" + strings.Repeat("a", 64),
		PublicKeyX:   "1",
		PublicKeyY:   "2",
		TrackingID:   &trackingID,
	}
}

func TestWebAuthnRegistrationActivatesOnlyOnSuccess(t *testing.T) {
	env, cleanup := newWebAuthnTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	address := "0x" + strings.Repeat("1", 40)
	tracking := uuid.NewString()
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-success", address, tracking)))
	first := env.relay(tracking, PENDING)

	assert.Equal(t, WebAuthnPending, env.state("cred-success"))
	assert.False(t, env.usable("cred-success"), "a pending registration must not sign in")

	env.setStatus(first, IN_NETWORK)
	assert.Equal(t, WebAuthnPending, env.state("cred-success"))

	// The reconciler bumps the fee: a replacement row joins the tracking id and
	// the original is superseded. The registration stays pending until the
	// replacement's outcome is known.
	replacement := env.relay(tracking, IN_NETWORK)
	env.setStatus(first, LOW_NONCE)
	assert.Equal(t, WebAuthnPending, env.state("cred-success"))
	assert.False(t, env.usable("cred-success"))

	env.setStatus(replacement, SUCCESS)
	assert.Equal(t, WebAuthnActive, env.state("cred-success"))
	assert.True(t, env.usable("cred-success"))

	// A later status write for the same transaction cannot demote it.
	env.setStatus(first, REVERTED)
	assert.Equal(t, WebAuthnActive, env.state("cred-success"))
}

func TestWebAuthnRegistrationRevertIsUnusableAndRetryable(t *testing.T) {
	env, cleanup := newWebAuthnTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	address := "0x" + strings.Repeat("2", 40)
	tracking := uuid.NewString()
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-revert", address, tracking)))
	otx := env.relay(tracking, IN_NETWORK)

	env.setStatus(otx, REVERTED)
	assert.Equal(t, WebAuthnFailed, env.state("cred-revert"))
	assert.False(t, env.usable("cred-revert"), "a reverted registration must never sign in")

	// Retrying the same mapping re-arms the row under the new tracking id.
	retry := uuid.NewString()
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-revert", address, retry)))
	assert.Equal(t, WebAuthnPending, env.state("cred-revert"))
	env.setStatus(env.relay(retry, IN_NETWORK), SUCCESS)
	assert.True(t, env.usable("cred-revert"))
}

func TestWebAuthnRegistrationConflicts(t *testing.T) {
	env, cleanup := newWebAuthnTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	victim := "0x" + strings.Repeat("3", 40)
	attacker := "0x" + strings.Repeat("4", 40)
	tracking := uuid.NewString()
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-owned", victim, tracking)))

	// Same credential id, another wallet: an explicit conflict, not a no-op.
	err := env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-owned", attacker, uuid.NewString()))
	require.ErrorIs(t, err, ErrWebAuthnCredentialConflict)

	// Same credential id, same wallet, different key: also a conflict.
	otherKey := webAuthnTestCred("cred-owned", victim, uuid.NewString())
	otherKey.KeyHash = "0x" + strings.Repeat("b", 64)
	require.ErrorIs(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, otherKey), ErrWebAuthnCredentialConflict)

	// Same mapping while pending: point the caller at the existing tracking id.
	err = env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-owned", victim, uuid.NewString()))
	var regErr *WebAuthnRegistrationError
	require.ErrorAs(t, err, &regErr)
	require.ErrorIs(t, err, ErrWebAuthnRegistrationPending)
	require.NotNil(t, regErr.Existing.TrackingID)
	assert.Equal(t, tracking, *regErr.Existing.TrackingID)

	env.setStatus(env.relay(tracking, IN_NETWORK), SUCCESS)
	require.ErrorIs(t,
		env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-owned", victim, uuid.NewString())),
		ErrWebAuthnAlreadyRegistered)

	cred, err := env.pg.GetActiveWebAuthnCredential(ctx, env.tx, "cred-owned")
	require.NoError(t, err)
	assert.Equal(t, victim, cred.Address)
}

func TestWebAuthnCredentialsAreScopedToTheirAddress(t *testing.T) {
	env, cleanup := newWebAuthnTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	alice := "0x" + strings.Repeat("5", 40)
	bob := "0x" + strings.Repeat("6", 40)
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-alice", alice, uuid.NewString())))
	require.NoError(t, env.pg.RecordWebAuthnRegistration(ctx, env.tx, webAuthnTestCred("cred-bob", bob, uuid.NewString())))

	creds, err := env.pg.ListWebAuthnCredentialsByAddress(ctx, env.tx, alice)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.Equal(t, "cred-alice", creds[0].CredentialID)
	assert.Equal(t, WebAuthnPending, creds[0].State)
	require.NotNil(t, creds[0].TrackingID)
}

func TestLegacyWebAuthnCredentialIsUnusableUntilResolved(t *testing.T) {
	env, cleanup := newWebAuthnTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	// A row as migration 038 leaves a pre-existing one.
	_, err := env.tx.Exec(ctx, `
		INSERT INTO webauthn_credentials (credential_id, address, key_hash, public_key_x, public_key_y, state)
		VALUES ('cred-legacy', $1, $2, 1, 2, 'legacy'), ('cred-legacy-bad', $1, $2, 1, 2, 'legacy')`,
		"0x"+strings.Repeat("7", 40), "0x"+strings.Repeat("c", 64))
	require.NoError(t, err)
	assert.False(t, env.usable("cred-legacy"), "a legacy row must not sign in before its chain check")

	legacy, err := env.pg.ListLegacyWebAuthnCredentials(ctx, env.tx)
	require.NoError(t, err)
	ids := make([]string, 0, len(legacy))
	for _, c := range legacy {
		ids = append(ids, c.CredentialID)
	}
	assert.Contains(t, ids, "cred-legacy")

	require.NoError(t, env.pg.ResolveLegacyWebAuthnCredential(ctx, env.tx, "cred-legacy", WebAuthnActive))
	require.NoError(t, env.pg.ResolveLegacyWebAuthnCredential(ctx, env.tx, "cred-legacy-bad", WebAuthnUnverified))
	assert.True(t, env.usable("cred-legacy"))
	assert.False(t, env.usable("cred-legacy-bad"))
	require.Error(t, env.pg.ResolveLegacyWebAuthnCredential(ctx, env.tx, "cred-legacy", WebAuthnPending))
}
