package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

func newRequiredAuditDB(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping database readiness audit test")
	}
	admin, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close(context.Background())) })

	name := fmt.Sprintf("clc_symbol_ready_%d", time.Now().UnixNano())
	dbIdentifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+dbIdentifier)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbIdentifier+" WITH (FORCE)")
		require.NoError(t, err)
	})

	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	config.Database = name
	conn, err := pgx.ConnectConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close(context.Background())) })

	migrator, err := migrate.NewMigrator(t.Context(), conn, "schema_version")
	require.NoError(t, err)
	require.NoError(t, migrator.LoadMigrations(os.DirFS("../../migrations")))
	require.NoError(t, migrator.Migrate(t.Context()))
	return conn
}

func auditRequiredReadOnly(t *testing.T, conn *pgx.Conn) []requiredIssue {
	t.Helper()
	tx, err := conn.BeginTx(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	issues, err := auditRequired(t.Context(), tx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	return issues
}

func TestRequiredAuditIncludesRetiredWithoutReservingTheirLabelsDB(t *testing.T) {
	conn := newRequiredAuditDB(t)
	ctx := t.Context()

	ready := auditRequiredReadOnly(t, conn)
	serialized, err := json.Marshal(ready)
	require.NoError(t, err)
	require.JSONEq(t, "[]", string(serialized))

	const active = "0x0000000000000000000000000000000000000010"
	const retired = "0x0000000000000000000000000000000000000020"
	const reused = "0x0000000000000000000000000000000000000030"
	_, err = conn.Exec(ctx, `
		INSERT INTO vouchers(chain_id, address, symbol, display_symbol, active)
		VALUES (100, $1, 'CUR', NULL, TRUE),
		       (100, $2, ' old ', NULL, FALSE),
		       (100, $3, 'NXT', 'OLD', TRUE)`,
		active, retired, reused)
	require.NoError(t, err)
	require.ElementsMatch(t, []requiredIssue{
		{Kind: "voucher", Reason: "missing", ChainID: 100, Address: active, Candidate: "CUR"},
		{Kind: "voucher", Reason: "missing-retired-label", ChainID: 100, Address: retired, Candidate: "OLD"},
	}, auditRequiredReadOnly(t, conn))

	_, err = conn.Exec(ctx, `UPDATE vouchers SET display_symbol = upper(btrim(symbol)) WHERE address IN ($1, $2)`, active, retired)
	require.NoError(t, err)
	require.Empty(t, auditRequiredReadOnly(t, conn), "a historical retired label may be reused")
}

func TestRequiredAuditDetectsUnresolvedDeploymentsAndQueuedCollisionsDB(t *testing.T) {
	conn := newRequiredAuditDB(t)
	ctx := t.Context()
	poolConfig, err := pgxpool.ParseConfig(conn.Config().ConnString())
	require.NoError(t, err)
	poolConfig.ConnConfig = conn.Config()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	river, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	require.NoError(t, err)
	_, err = river.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	const savedAddress = "0x0000000000000000000000000000000000000010"
	const retiredAddress = "0x0000000000000000000000000000000000000011"
	const retiredTracking = "00000000-0000-4000-8000-000000000011"
	const trackingWithSaved = "00000000-0000-4000-8000-000000000010"
	const unresolvedTracking = "00000000-0000-4000-8000-000000000020"
	const failedTracking = "00000000-0000-4000-8000-000000000021"
	const repairedTracking = "00000000-0000-4000-8000-000000000022"
	const healthyTracking = "00000000-0000-4000-8000-000000000023"
	const healthyAddress = "0x0000000000000000000000000000000000000023"
	const signer = "0x0000000000000000000000000000000000000040"
	var factoryID, chainID, signerID int64
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT id, chain_id FROM platform_contracts WHERE contract_type = 'ERC20' ORDER BY id LIMIT 1`,
	).Scan(&factoryID, &chainID))
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO keystore(public_key, private_key) VALUES ($1, 'test key') RETURNING id`, signer,
	).Scan(&signerID))
	_, err = tx.Exec(ctx, `
		INSERT INTO vouchers(chain_id, address, symbol, display_symbol, active)
		VALUES ($1, $2, 'LEG', 'SAVED', TRUE),
		       ($1, $3, 'OLD', 'SAVED', FALSE)`, chainID, savedAddress, retiredAddress)
	require.NoError(t, err)
	var unresolvedID int64
	require.NoError(t, tx.QueryRow(ctx, `
		WITH operations AS (
			INSERT INTO otx(tracking_id, otx_type, signer_account, raw_tx, tx_hash, nonce)
			VALUES ($1, 'TOKEN_PROXY_DEPLOY', $3, '0x', '0x1', 0),
			       ($2, 'TOKEN_PROXY_DEPLOY', $3, '0x', '0x2', 1),
			       ($7, 'TOKEN_PROXY_DEPLOY', $3, '0x', '0x3', 2),
			       ($9, 'TOKEN_PROXY_DEPLOY', $3, '0x', '0x4', 3),
			       ($10, 'TOKEN_PROXY_DEPLOY', $3, '0x', '0x5', 4),
			       ($11, 'TOKEN_PROXY_DEPLOY', $3, '0x', '0x6', 5)
			RETURNING id, tracking_id
		), deployments AS (
			INSERT INTO deployed_tokens(otx_id, platform_contract_id, symbol, display_symbol, name, admin, status, address, decimals)
			SELECT id, $4, CASE WHEN tracking_id = $11 THEN 'HLTNAT' ELSE 'invalid legacy ' || id END,
			       CASE WHEN tracking_id = $10 THEN 'FRESH' WHEN tracking_id = $11 THEN 'HLTHY' END,
			       'Review fixture', $5,
			       CASE WHEN tracking_id IN ($1, $7) THEN 'CONFIRMED'
			            WHEN tracking_id = $9 THEN 'FAILED' ELSE 'PENDING' END,
			       CASE WHEN tracking_id = $1 THEN $6 WHEN tracking_id = $7 THEN $8 WHEN tracking_id = $11 THEN $12 END, 6
			FROM operations
			RETURNING id, otx_id
		)
		SELECT d.id FROM deployments d JOIN operations o ON o.id = d.otx_id WHERE o.tracking_id = $2`,
		trackingWithSaved, unresolvedTracking, signerID, factoryID, signer, savedAddress, retiredTracking, retiredAddress,
		failedTracking, repairedTracking, healthyTracking, healthyAddress,
	).Scan(&unresolvedID))

	makeJob := func(trackingID, native string, alias *string, state string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, tx.QueryRow(ctx, `
			INSERT INTO river_job(kind, state, args, finalized_at, max_attempts, priority, queue)
			VALUES ('TOKEN_DEPLOY', $1::river_job_state,
			        jsonb_build_object('trackingId', $2::TEXT, 'symbol', $3::TEXT, 'displaySymbol', $4::TEXT),
			        CASE WHEN $1 IN ('completed', 'discarded', 'cancelled') THEN now() END, 3, 1, 'default')
			RETURNING id`, state, trackingID, native, alias).Scan(&id))
		return id
	}
	// A replay may use an existing catalog label, but the job can still
	// start a fresh attempt and must have valid arguments of its own.
	savedRetryID := makeJob(trackingWithSaved, "deprecated legacy ticker", nil, "retryable")
	failedRetryID := makeJob(failedTracking, "invalid retry ticker", nil, "retryable")
	// Repairing a pending row does not repair the queued retry. A healthy
	// job and its own pending row must not trigger a duplicate diagnostic.
	repairedRetryID := makeJob(repairedTracking, "invalid repaired ticker", nil, "retryable")
	healthyLabel := "HLTHY"
	makeJob(healthyTracking, "HLTNAT", &healthyLabel, "retryable")
	makeJob("00000000-0000-4000-8000-000000000030", "invalid completed ticker", nil, "completed")
	legacyTracking := "00000000-0000-4000-8000-000000000040"
	makeJob(legacyTracking, "  legacy ", nil, "scheduled")
	_, err = tx.Exec(ctx, `SELECT claim_identity('symbol', 'legacy', 'voucher', 'deploy:' || $1)`, legacyTracking)
	require.NoError(t, err)

	const collisionTracking = "00000000-0000-4000-8000-000000000050"
	const badTracking = "00000000-0000-4000-8000-000000000060"
	const dupe1Tracking = "00000000-0000-4000-8000-000000000070"
	const dupe2Tracking = "00000000-0000-4000-8000-000000000080"
	collisionID := makeJob(collisionTracking, " saved ", nil, "available")
	badLabel := "lower"
	badID := makeJob(badTracking, "VALID", &badLabel, "retryable")
	dupe1ID := makeJob(dupe1Tracking, " DUPE ", nil, "running")
	dupeLabel := "DUPE"
	dupe2ID := makeJob(dupe2Tracking, "OTHER", &dupeLabel, "pending")
	require.NoError(t, tx.Commit(ctx))

	require.ElementsMatch(t, []requiredIssue{
		{Kind: "deployment", Reason: "invalid-or-missing-label", DeploymentID: unresolvedID,
			ChainID: chainID, TrackingID: unresolvedTracking},
		{Kind: "river-job", Reason: "invalid-or-missing-label", TrackingID: trackingWithSaved, JobID: savedRetryID},
		{Kind: "river-job", Reason: "invalid-or-missing-label", TrackingID: failedTracking, JobID: failedRetryID},
		{Kind: "river-job", Reason: "invalid-or-missing-label", TrackingID: repairedTracking, JobID: repairedRetryID},
		{Kind: "river-job", Reason: "duplicate-or-reserved", TrackingID: collisionTracking, JobID: collisionID, Candidate: "SAVED"},
		{Kind: "river-job", Reason: "invalid-or-missing-label", TrackingID: badTracking, JobID: badID},
		{Kind: "river-job", Reason: "duplicate-or-reserved", TrackingID: dupe1Tracking, JobID: dupe1ID, Candidate: "DUPE"},
		{Kind: "river-job", Reason: "duplicate-or-reserved", TrackingID: dupe2Tracking, JobID: dupe2ID, Candidate: "DUPE"},
	}, auditRequiredReadOnly(t, conn))
}
