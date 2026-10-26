package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lmittmann/w3"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

func TestMultisigIndexDiscoveryDeduplicatesInTheProjectionTransaction(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	queue, err := river.NewClient(driver, &river.Config{})
	require.NoError(t, err)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(key.PublicKey).Hex()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	require.NoError(t, EnqueueSafeRegistration(ctx, queue, tx, owner))
	require.NoError(t, EnqueueSafeRegistration(ctx, queue, tx, strings.ToLower(owner)))
	var count int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='REGISTER_USER' AND args->>'userAddress'=$1`, strings.ToLower(owner)).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, tx.Rollback(ctx))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='REGISTER_USER' AND args->>'userAddress'=$1`, strings.ToLower(owner)).Scan(&count))
	require.Zero(t, count, "rolling back signer discovery must roll back its registration too")
}

func TestAccountRegistrationSkipsAnExistingIndexEntry(t *testing.T) {
	const owner = "0x1111111111111111111111111111111111111111"
	index := common.HexToAddress("0x2222222222222222222222222222222222222222")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any               `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "eth_call", request.Method)
		var call map[string]string
		require.NoError(t, json.Unmarshal(request.Params[0], &call))
		require.Equal(t, index.Hex(), common.HexToAddress(call["to"]).Hex())
		data, err := accountIndexContainsFunc.EncodeArgs(common.HexToAddress(owner))
		require.NoError(t, err)
		input := call["input"]
		if input == "" {
			input = call["data"]
		}
		require.Equal(t, fmt.Sprintf("0x%x", data), input)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "0x0000000000000000000000000000000000000000000000000000000000000001"})
	}))
	defer server.Close()
	client, err := w3.Dial(server.URL)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	// No store, key or nonce source: touching any would panic. An indexed
	// (including inactive) address must not sign or dispatch another add.
	worker := &RegisterUserWorker{wc: &WorkerContainer{
		accountIndexAddress: index, chainProvider: &ethutils.Provider{Client: client},
	}}
	require.NoError(t, worker.Work(context.Background(), &river.Job[RegisterUserArgs]{Args: RegisterUserArgs{UserAddress: owner}}))
}
