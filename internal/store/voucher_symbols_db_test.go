package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestVoucherDisplaySymbolLifecycle(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Enforce the rollout invariant for every write in this test without
	// changing the shared schema or requiring unrelated legacy fixtures to pass.
	_, err = tx.Exec(ctx, `ALTER TABLE vouchers ADD CONSTRAINT required_lifecycle_label
		CHECK (display_symbol IS NOT NULL) NOT VALID`)
	require.NoError(t, err)

	original := randomAddress(t, tx)
	var oldID uint64
	require.NoError(t, tx.QueryRow(ctx, `
        INSERT INTO vouchers(chain_id,address,name,symbol,display_symbol)
        VALUES(100,$1,'Old display symbol test','ZXDLC','ZXDLC') RETURNING id`, original).Scan(&oldID))
	taken, err := pg.ListTakenDisplaySymbols(ctx, tx, "ZXDLC")
	require.NoError(t, err)
	require.Equal(t, []string{"ZXDLC"}, taken)

	require.NoError(t, pg.SetVoucherActive(ctx, tx, oldID, false))
	taken, err = pg.ListTakenDisplaySymbols(ctx, tx, "ZXDLC")
	require.NoError(t, err)
	require.Empty(t, taken)
	legacyTaken, err := pg.ListTakenVoucherSymbols(ctx, tx, "ZXDLC")
	require.NoError(t, err)
	require.Equal(t, []string{"ZXDLC"}, legacyTaken, "the contract symbol remains unavailable")

	tracking := "voucher-display-lifecycle"
	native, err := pg.ReserveVoucherDeploymentSymbols(ctx, tx, tracking, "ZXDLC", "ZXDLC", true)
	require.NoError(t, err)
	require.Equal(t, "ZXDL2", native)
	taken, err = pg.ListTakenDisplaySymbols(ctx, tx, "ZXDLC")
	require.NoError(t, err)
	require.Equal(t, []string{"ZXDLC"}, taken, "pending claims are visible before the worker runs")

	attempt, err := tx.Begin(ctx)
	require.NoError(t, err)
	changed, err := pg.RestoreVoucherWithinLimitsChanged(ctx, attempt, original)
	require.False(t, changed)
	var conflict *pgconn.PgError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "clc_display_symbol_key", conflict.ConstraintName)
	require.NoError(t, attempt.Rollback(ctx))

	var active bool
	var stored, oldNative string
	require.NoError(t, tx.QueryRow(ctx, "SELECT active, display_symbol, symbol FROM vouchers WHERE id=$1", oldID).Scan(&active, &stored, &oldNative))
	require.False(t, active)
	require.Equal(t, "ZXDLC", stored, "retirement keeps the last label")
	require.Equal(t, "ZXDLC", oldNative)

	replacement := "ZXDLR"
	changed, err = pg.RestoreVoucherWithinLimitsWithSymbolChanged(ctx, tx, original, &replacement)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, tx.QueryRow(ctx, "SELECT active, display_symbol, symbol FROM vouchers WHERE id=$1", oldID).Scan(&active, &stored, &oldNative))
	require.True(t, active)
	require.Equal(t, replacement, stored)
	require.Equal(t, "ZXDLC", oldNative)

	attempt, err = tx.Begin(ctx)
	require.NoError(t, err)
	err = pg.ReserveDeploymentIdentity(ctx, attempt, "pool-display-lifecycle", "pool", "Fixture Pool Name", replacement)
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "clc_display_symbol_key", conflict.ConstraintName)
	require.NoError(t, attempt.Rollback(ctx))
}

func TestVoucherDisplaySymbolLegacyQueuedReservation(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// An older API version reserved only the native ticker before enqueuing.
	_, err = tx.Exec(ctx, `SELECT claim_identity('symbol', 'ZXDOLD', 'voucher', 'deploy:legacy-queue-fixture')`)
	require.NoError(t, err)
	taken, err := pg.ListTakenDisplaySymbols(ctx, tx, "ZXDOLD")
	require.NoError(t, err)
	require.Equal(t, []string{"ZXDOLD"}, taken)
	attempt, err := tx.Begin(ctx)
	require.NoError(t, err)
	_, err = pg.ReserveVoucherDeploymentSymbols(ctx, attempt, "new-queue-fixture", "ZXDNEW", "ZXDOLD", true)
	var conflict *pgconn.PgError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "clc_display_symbol_key", conflict.ConstraintName)
	require.NoError(t, attempt.Rollback(ctx))

	_, err = pg.ReserveVoucherDeploymentSymbols(ctx, tx, "new-queue-fixture", "ZXDNEW", "ZXDSET", true)
	require.NoError(t, err)
	taken, err = pg.ListTakenDisplaySymbols(ctx, tx, "ZXDNEW")
	require.NoError(t, err)
	require.Empty(t, taken, "a separate native ticker does not occupy a display label")
}

func TestVoucherDisplaySymbolConcurrentReservation(t *testing.T) {
	pg := recipientTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := pg.Pool()
	winner, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer winner.Rollback(ctx)
	loser, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer loser.Rollback(ctx)

	const name = "ZXDRAC"
	const first = "voucher-display-race-first"
	const second = "voucher-display-race-second"
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM identity_claims WHERE owner_key IN ($1,$2)", "deploy:"+first, "deploy:"+second)
		require.NoError(t, err)
	})
	_, err = pg.ReserveVoucherDeploymentSymbols(ctx, winner, first, "ZXDNT1", name, true)
	require.NoError(t, err)

	var pid int
	require.NoError(t, loser.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	result := make(chan error, 1)
	go func() {
		_, err := pg.ReserveVoucherDeploymentSymbols(ctx, loser, second, "ZXDNT2", name, true)
		result <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, "SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting)
		return err == nil && waiting
	}, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, winner.Commit(ctx))
	var conflict *pgconn.PgError
	require.ErrorAs(t, <-result, &conflict)
	require.Equal(t, "clc_display_symbol_key", conflict.ConstraintName)
	require.NoError(t, loser.Rollback(ctx))
	var winnerHas, loserHas bool
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT count(*) FILTER (WHERE owner_key=$1 AND namespace='display_symbol') = 1,
               count(*) FILTER (WHERE owner_key=$2) > 0 FROM identity_claims`,
		"deploy:"+first, "deploy:"+second).Scan(&winnerHas, &loserHas))
	require.True(t, winnerHas)
	require.False(t, loserHas, "losing the label rolls back the candidate native ticker too")
}

func TestVoucherDisplaySymbolPromotionRetainedOnReplay(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	var tracking string
	require.NoError(t, tx.QueryRow(ctx, "SELECT tracking_id::TEXT FROM otx WHERE id=$1", otxID).Scan(&tracking))
	display := "ZXDPUB"
	native, err := pg.ReserveVoucherDeploymentSymbols(ctx, tx, tracking, "ZXDCHN", display, true)
	require.NoError(t, err)
	deployedID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID: otxID, PlatformContractID: platformContractID(t, tx, "ERC20"),
		Symbol: native, DisplaySymbol: &display, Name: "Promoted display symbol",
		Decimals: 6, Admin: randomAddress(t, tx),
	})
	require.NoError(t, err)

	address := randomAddress(t, tx)
	_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, address)
	require.NoError(t, err)
	id, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	var current, ticker string
	require.NoError(t, tx.QueryRow(ctx, "SELECT display_symbol, symbol FROM vouchers WHERE id=$1", id).Scan(&current, &ticker))
	require.Equal(t, display, current)
	require.Equal(t, native, ticker)
	_, err = tx.Exec(ctx, "UPDATE vouchers SET display_symbol='ZXDNEW' WHERE id=$1", id)
	require.NoError(t, err)
	_, err = pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(ctx, "SELECT display_symbol, symbol FROM vouchers WHERE id=$1", id).Scan(&current, &ticker))
	require.Equal(t, "ZXDNEW", current, "replayed deployments must not reset later owner edits")
	require.Equal(t, native, ticker)
	taken, err := pg.ListTakenDisplaySymbols(ctx, tx, display)
	require.NoError(t, err)
	require.Empty(t, taken, "a confirmed deployment must not reserve the old label after promotion")
	require.NoError(t, pg.SetVoucherActive(ctx, tx, id, false))
	taken, err = pg.ListTakenDisplaySymbols(ctx, tx, current)
	require.NoError(t, err)
	require.Empty(t, taken, "a confirmed deployment must not reserve the old label after retirement")
}

func TestVoucherDisplaySymbolPromotionReplaysLegacyTickerWithRequiredLabel(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Enforce the planned insert check without rewriting unrelated legacy
	// fixture rows already present in the integration database.
	_, err = tx.Exec(ctx, `ALTER TABLE vouchers ADD CONSTRAINT test_display_symbol_required
		CHECK (display_symbol IS NOT NULL) NOT VALID`)
	require.NoError(t, err)

	platformID := platformContractID(t, tx, "ERC20")
	admin := randomAddress(t, tx)
	stageLegacy := func(symbol string) (uint64, string) {
		t.Helper()
		otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
		id, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
			OTXID: otxID, PlatformContractID: platformID,
			Symbol: symbol, Name: "Legacy promotion fixture", Decimals: 6, Admin: admin,
		})
		require.NoError(t, err)
		address := randomAddress(t, tx)
		_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, address)
		require.NoError(t, err)
		return id, address
	}

	const legacyTicker = "ZX.DL"
	const approvedLabel = "ZXDREP"
	deployed, address := stageLegacy(legacyTicker)
	var existingID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO vouchers(chain_id,address,name,symbol,display_symbol)
		SELECT chain_id,$2,'Reviewed legacy voucher',$3,$4 FROM platform_contracts WHERE id=$1
		RETURNING id`, platformID, address, legacyTicker, approvedLabel).Scan(&existingID))
	replayed, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployed)
	require.NoError(t, err)
	require.Equal(t, existingID, replayed)
	var publishedLabel, nativeTicker string
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT v.display_symbol,v.symbol FROM vouchers v
		JOIN deployed_tokens d ON d.voucher_id=v.id WHERE d.id=$1`, deployed).Scan(&publishedLabel, &nativeTicker))
	require.Equal(t, approvedLabel, publishedLabel)
	require.Equal(t, legacyTicker, nativeTicker)

	unreviewed, target := stageLegacy("ZX.BAD")
	attempt, err := tx.Begin(ctx)
	require.NoError(t, err)
	defer attempt.Rollback(ctx)
	_, err = pg.UpsertVoucherFromDeploy(ctx, attempt, unreviewed)
	var violation *pgconn.PgError
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "test_display_symbol_required", violation.ConstraintName)
	require.NoError(t, attempt.Rollback(ctx))
	var catalogued, linked bool
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM vouchers WHERE address=$2),
		       EXISTS (SELECT 1 FROM deployed_tokens WHERE id=$1 AND voucher_id IS NOT NULL)`,
		unreviewed, target).Scan(&catalogued, &linked))
	require.False(t, catalogued)
	require.False(t, linked)
}
