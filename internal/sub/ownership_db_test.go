package sub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
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
