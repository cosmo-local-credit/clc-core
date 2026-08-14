package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live-database coverage for post-confirmation private attachments. Skipped
// unless CORE_TEST_DSN points at a database; the test helper applies migrations.

func TestPostConfirmationUserShareIsIdempotentAndPrivate(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	insertUser := func(address, name, email string) User {
		t.Helper()
		user, err := pg.InsertUser(ctx, tx, User{
			EthereumAddress: address,
			Name:            name,
			Email:           email,
			Role:            "USER",
			Status:          "APPROVED",
			MaxPools:        10,
			MaxTokens:       20,
		})
		require.NoError(t, err)
		return user
	}

	owner := insertUser("0x"+strings.Repeat("1", 40), "Share Owner", "share-owner-test@example.com")
	recipient := insertUser("0x"+strings.Repeat("2", 40), "Share Recipient", "share-recipient-test@example.com")
	stranger := insertUser("0x"+strings.Repeat("3", 40), "Share Stranger", "share-stranger-test@example.com")
	txHash := "0x" + strings.Repeat("a", 64)
	share := UserShare{
		TxHash:           txHash,
		OwnerUserID:      owner.ID,
		AuthorizedUserID: recipient.ID,
		SharePermission:  SharePermissionEmail,
		ExtraNote:        "confirmed note",
	}

	id, err := pg.InsertUserShare(ctx, tx, share)
	require.NoError(t, err)
	require.NotZero(t, id)

	noteOnly := share
	noteOnly.TxHash = "0x" + strings.Repeat("b", 64)
	noteOnly.SharePermission = 0
	noteOnly.ExtraNote = "note without PII"
	noteOnlyID, err := pg.InsertUserShare(ctx, tx, noteOnly)
	require.NoError(t, err, "a note must not require any PII permission")
	require.NotZero(t, noteOnlyID)

	// Retrying the same receipt submission is safe and returns the same row.
	retryID, err := pg.InsertUserShare(ctx, tx, share)
	require.NoError(t, err)
	assert.Equal(t, id, retryID)

	// The same transaction cannot be rebound to different private metadata.
	different := share
	different.ExtraNote = "different note"
	_, err = pg.InsertUserShare(ctx, tx, different)
	require.True(t, errors.Is(err, pgx.ErrNoRows), "conflicting retry must return pgx.ErrNoRows: %v", err)

	for _, callerID := range []uint64{owner.ID, recipient.ID} {
		got, err := pg.GetUserShareByTxHash(ctx, tx, txHash, callerID)
		require.NoError(t, err)
		assert.Equal(t, id, got.ID)
		assert.Equal(t, txHash, got.TxHash)
		assert.Equal(t, "share-owner-test@example.com", got.Email)
	}

	_, err = pg.GetUserShareByTxHash(ctx, tx, txHash, stranger.ID)
	require.True(t, errors.Is(err, pgx.ErrNoRows), "unrelated user must not read attachment: %v", err)

	// Keep the intentional constraint violation last because PostgreSQL marks the
	// enclosing test transaction aborted after an error.
	empty := noteOnly
	empty.TxHash = "0x" + strings.Repeat("c", 64)
	empty.ExtraNote = ""
	_, err = pg.InsertUserShare(ctx, tx, empty)
	require.Error(t, err, "permission zero without a note must violate user_shares_has_content")
}
