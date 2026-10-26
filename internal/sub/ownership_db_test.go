package sub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

func TestExternalRelayerOwnershipCommitsWithoutOTX(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set")
	}
	logger := slog.New(slog.DiscardHandler)
	pg, err := store.NewPgStore(store.PgOpts{Logg: logger, DSN: dsn, MigrationsFolderPath: "../../migrations", QueriesFolderPath: "../../core_queries.sql"})
	require.NoError(t, err)
	t.Cleanup(pg.Pool().Close)
	ctx := context.Background()
	address := func() string {
		var b [20]byte
		_, err := rand.Read(b[:])
		require.NoError(t, err)
		return common.BytesToAddress(b[:]).Hex()
	}
	pool, token, registry, old, next := address(), address(), address(), address(), address()
	// Names and symbols are unique catalog-wide, so each run claims its own.
	_, err = pg.Pool().Exec(ctx, `INSERT INTO vouchers (chain_id,address,name,symbol,owner_address,description) VALUES (100,$1,'External voucher '||$1,'EXTV'||right($1,6),$2,'keep voucher')`, token, old)
	require.NoError(t, err)
	_, err = pg.Pool().Exec(ctx, `INSERT INTO pools (chain_id,address,name,symbol,owner_address,token_registry_address,description) VALUES (100,$1,'External pool '||$1,'EXTP'||right($1,6),$2,$3,'keep pool')`, pool, old, registry)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pg.Pool().Exec(ctx, `DELETE FROM vouchers WHERE chain_id=100 AND address=$1`, token)
		require.NoError(t, err)
		_, err = pg.Pool().Exec(ctx, `DELETE FROM pools WHERE chain_id=100 AND address=$1`, pool)
		require.NoError(t, err)
		_, err = pg.Pool().Exec(ctx, `DELETE FROM catalog_ownership WHERE chain_id=100 AND address=ANY($1::text[])`, []string{strings.ToLower(pool), strings.ToLower(token), strings.ToLower(registry)})
		require.NoError(t, err)
	})
	// No publisher and no OTX: reaching deployment tracking would either fail
	// the lookup or dereference nil. The catalog projection must still commit.
	s := &Sub{store: pg, chainID: 100, logg: logger}
	for _, contract := range []string{registry, token, pool} {
		data, err := json.Marshal(event.Event{Success: true, TxType: "OWNERSHIP_TRANSFERRED", TxHash: "0xexternal-relayer", ContractAddress: contract, Block: 12, Index: 2, Payload: map[string]any{"newOwner": next}})
		require.NoError(t, err)
		require.NoError(t, s.processEvent(ctx, "TRACKER.OWNERSHIP_TRANSFERRED", data))
	}
	for _, row := range []struct{ table, address, description string }{{"pools", pool, "keep pool"}, {"vouchers", token, "keep voucher"}} {
		var owner, description string
		require.NoError(t, pg.Pool().QueryRow(ctx, `SELECT owner_address,description FROM `+row.table+` WHERE chain_id=100 AND address=$1`, row.address).Scan(&owner, &description))
		require.Equal(t, common.HexToAddress(next), common.HexToAddress(owner))
		require.Equal(t, row.description, description)
	}
}

// A subscriber that falls behind the RPC's pruning window reads Safe state at
// an event block the node no longer has. The ownership projection sharing the
// event's transaction must still commit, with a durable head refresh queued
// beside it, rather than the event being redelivered forever.
func TestPrunedSafeReadStillCommitsOwnershipAndQueuesARefresh(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set")
	}
	logger := slog.New(slog.DiscardHandler)
	pg, err := store.NewPgStore(store.PgOpts{Logg: logger, DSN: dsn, MigrationsFolderPath: "../../migrations", QueriesFolderPath: "../../core_queries.sql"})
	require.NoError(t, err)
	t.Cleanup(pg.Pool().Close)
	ctx := context.Background()
	driver := riverpgxv5.New(pg.Pool())
	migrator, err := rivermigrate.New(driver, nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	queue, err := river.NewClient(driver, &river.Config{})
	require.NoError(t, err)

	address := func() string {
		var b [20]byte
		_, err := rand.Read(b[:])
		require.NoError(t, err)
		return common.BytesToAddress(b[:]).Hex()
	}
	token, old, next := address(), address(), address()
	_, err = pg.Pool().Exec(ctx, `INSERT INTO vouchers (chain_id,address,name,symbol,owner_address) VALUES (100,$1,'Pruned voucher '||$1,'PRNV'||right($1,6),$2)`, token, old)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pg.Pool().Exec(ctx, `DELETE FROM vouchers WHERE chain_id=100 AND address=$1`, token)
		require.NoError(t, err)
		_, err = pg.Pool().Exec(ctx, `DELETE FROM catalog_ownership WHERE chain_id=100 AND address=$1`, strings.ToLower(token))
		require.NoError(t, err)
		_, err = pg.Pool().Exec(ctx, `DELETE FROM river_job WHERE kind='SAFE_SIGNERS_REFRESH' AND args->>'address'=$1`, strings.ToLower(next))
		require.NoError(t, err)
	})

	s := &Sub{store: pg, chainID: 100, logg: logger,
		safeReader: &fakeSafeReader{err: errors.New("state at block #12 is pruned")},
		refreshSafe: func(ctx context.Context, tx pgx.Tx, address string, minBlock uint64, register bool) error {
			return worker.EnqueueSafeSignersRefresh(ctx, queue, tx, address, minBlock, register)
		}}
	data, err := json.Marshal(event.Event{Success: true, TxType: "OWNERSHIP_TRANSFERRED", TxHash: "0xpruned-safe-read", ContractAddress: token, Block: 12, Index: 2, Payload: map[string]any{"newOwner": next}})
	require.NoError(t, err)
	require.NoError(t, s.processEvent(ctx, "TRACKER.OWNERSHIP_TRANSFERRED", data))

	var owner string
	require.NoError(t, pg.Pool().QueryRow(ctx, `SELECT owner_address FROM vouchers WHERE chain_id=100 AND address=$1`, token).Scan(&owner))
	require.Equal(t, common.HexToAddress(next), common.HexToAddress(owner))
	var args worker.SafeSignersRefreshArgs
	var raw []byte
	require.NoError(t, pg.Pool().QueryRow(ctx, `SELECT args FROM river_job WHERE kind='SAFE_SIGNERS_REFRESH' AND args->>'address'=$1`, strings.ToLower(next)).Scan(&raw))
	require.NoError(t, json.Unmarshal(raw, &args))
	require.Equal(t, worker.SafeSignersRefreshArgs{Address: strings.ToLower(next), MinBlock: 12, Register: true}, args)
}
