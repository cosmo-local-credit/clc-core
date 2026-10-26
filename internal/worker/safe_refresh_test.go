package worker

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

type (
	fakeHeadReader struct {
		state *safe.State
		head  uint64
		err   error
	}

	safeRefreshStore struct {
		store.Store

		pool     *pgxpool.Pool
		owned    bool
		replaced [][]any
	}
)

func (f fakeHeadReader) ReadSafeLatest(context.Context, common.Address) (*safe.State, uint64, error) {
	return f.state, f.head, f.err
}

func (s *safeRefreshStore) Pool() *pgxpool.Pool { return s.pool }

func (s *safeRefreshStore) ReplaceSafeSigners(_ context.Context, _ pgx.Tx, chainID int64, address string, signers []string, threshold int, block, index uint64) (bool, error) {
	s.replaced = append(s.replaced, []any{chainID, address, signers, threshold, block, index})
	return true, nil
}

func (s *safeRefreshStore) CatalogHasOwner(context.Context, pgx.Tx, int64, string) (bool, error) {
	return s.owned, nil
}

func refreshJob(address string, minBlock uint64, register bool) *river.Job[SafeSignersRefreshArgs] {
	return &river.Job[SafeSignersRefreshArgs]{Args: SafeSignersRefreshArgs{Address: address, MinBlock: minBlock, Register: register}}
}

// None of these may touch the store: the WorkerContainer has none, so a write
// would panic.
func TestSafeRefreshRetriesOrSkipsWithoutWriting(t *testing.T) {
	one := &safe.State{Owners: []common.Address{common.HexToAddress("0xa1")}, Threshold: 1}
	for name, tc := range map[string]struct {
		reader  fakeHeadReader
		wantErr bool
	}{
		"RPC failure retries":                      {fakeHeadReader{err: errors.New("connection refused")}, true},
		"lagging head retries":                     {fakeHeadReader{state: one, head: 99}, true},
		"not yet deployed on a lagging head":       {fakeHeadReader{head: 99, err: safe.ErrNotSafe}, true},
		"not a multisig at a current head is done": {fakeHeadReader{head: 100, err: safe.ErrNotSafe}, false},
	} {
		t.Run(name, func(t *testing.T) {
			w := &SafeSignersRefreshWorker{wc: &WorkerContainer{chainID: 100}, reader: tc.reader}
			err := w.Work(context.Background(), refreshJob("0x00000000000000000000000000000000000000c1", 100, true))
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSafeRefreshStoresTheHeadSetAndQueuesRegistration(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	queue, err := river.NewClient(driver, &river.Config{})
	require.NoError(t, err)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	refreshedSafe := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	countRegistrations := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='REGISTER_USER' AND args->>'userAddress'=$1`, refreshedSafe).Scan(&n))
		return n
	}
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM river_job WHERE kind='REGISTER_USER' AND args->>'userAddress'=$1`, refreshedSafe)
		require.NoError(t, err)
	})

	signers := []common.Address{common.HexToAddress("0xa1"), common.HexToAddress("0xa2")}
	for _, tc := range []struct {
		register, owned bool
		registrations   int
	}{{false, true, 0}, {true, false, 0}, {true, true, 1}} {
		fake := &safeRefreshStore{pool: pool, owned: tc.owned}
		w := &SafeSignersRefreshWorker{
			wc:     &WorkerContainer{chainID: 100, store: fake, queueClient: queue},
			reader: fakeHeadReader{state: &safe.State{Owners: signers, Threshold: 2}, head: 120},
		}
		require.NoError(t, w.Work(ctx, refreshJob(refreshedSafe, 100, tc.register)))
		require.Equal(t, [][]any{{int64(100), refreshedSafe, []string{signers[0].Hex(), signers[1].Hex()}, 2, uint64(120), uint64(math.MaxInt64)}}, fake.replaced,
			"the head's set dominates the head block and yields to later events")
		require.Equal(t, tc.registrations, countRegistrations())
	}
}
