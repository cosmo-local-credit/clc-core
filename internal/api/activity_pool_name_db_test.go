package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// All activity tables exist. A pool without metadata is a valid Core record;
// adding its display name must never prevent the API from returning history.
func TestAccountActivityPoolNamesDB(t *testing.T) {
	a, token := newReportSearchAPI(t)
	a.chainID = 100
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	poolAddress := common.HexToAddress(fmt.Sprintf("0x%040x", stamp)).Hex()
	const account = "0x7B65565C27d335A6F79c0ad919a077eBc6D2D282"
	var logs bytes.Buffer
	a.logg = slog.New(slog.NewJSONHandler(&logs, nil))

	schema := fmt.Sprintf("activity_names_%d", stamp)
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err := a.store.Pool().Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := a.store.Pool().Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	dsn, err := url.Parse(os.Getenv("CORE_TEST_DSN"))
	require.NoError(t, err)
	params := dsn.Query()
	params.Set("search_path", schema)
	dsn.RawQuery = params.Encode()
	fed, err := store.NewFederationStore(store.FederationStoreOpts{
		DSN: dsn.String(), Logg: a.logg, QueriesFolderPath: "../../federated_queries.sql",
	})
	require.NoError(t, err)
	t.Cleanup(fed.Close)
	_, err = fed.Pool().Exec(ctx, `
		CREATE TABLE tx(id INT PRIMARY KEY,tx_hash VARCHAR(66) NOT NULL,date_block TIMESTAMP NOT NULL,success BOOLEAN NOT NULL);
		CREATE TABLE pools(contract_address VARCHAR(42) PRIMARY KEY);
		CREATE TABLE token_transfer(tx_id INT REFERENCES tx(id),log_index BIGINT,contract_address VARCHAR(42),
			transfer_value NUMERIC,sender_address VARCHAR(42),recipient_address VARCHAR(42));
		CREATE TABLE pool_swap(tx_id INT REFERENCES tx(id),log_index BIGINT,contract_address VARCHAR(42),
			initiator_address VARCHAR(42),token_in_address VARCHAR(42),token_out_address VARCHAR(42),
			in_value NUMERIC,out_value NUMERIC,fee NUMERIC);
		CREATE TABLE pool_withdrawal(id BIGINT PRIMARY KEY,tx_id INT REFERENCES tx(id),log_index BIGINT,
			contract_address VARCHAR(42),kind TEXT,initiator_address VARCHAR(42),token_address VARCHAR(42),
			recipient_address VARCHAR(42),amount NUMERIC);
		INSERT INTO tx VALUES (1,'0xdeposit','2026-10-30',true),(2,'0xpayout','2026-10-30',true);
	`)
	require.NoError(t, err)
	_, err = fed.Pool().Exec(ctx, "INSERT INTO pools VALUES ($1)", poolAddress)
	require.NoError(t, err)
	_, err = fed.Pool().Exec(ctx, `INSERT INTO token_transfer VALUES
		(1,1,$1,10,$2,$1),(2,1,$1,9,$1,$2)`, poolAddress, account)
	require.NoError(t, err)
	_, err = a.store.Pool().Exec(ctx, "INSERT INTO pools(chain_id,address) VALUES ($1,$2)", a.chainID, poolAddress)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := a.store.Pool().Exec(ctx, "DELETE FROM pools WHERE chain_id=$1 AND address=$2", a.chainID, poolAddress)
		require.NoError(t, cleanupErr)
	})
	a.federationStore = fed
	a.router.GET("/api/v2/accounts/:address/activity", a.accountActivityHandler, a.flexibleAuthMiddleware())

	// Prove that SQL classification succeeds before testing API enrichment.
	classified, err := fed.ListUserActivity(ctx, account, store.AccountActivityFilter{}, nil, 20, true)
	require.NoError(t, err)
	require.Len(t, classified, 2)
	for _, tc := range []struct {
		label, want       string
		name, displayName any
	}{
		{label: "both_null"},
		{label: "empty_name", name: ""},
		{label: "display_only", displayName: fmt.Sprintf("Display %d", stamp), want: fmt.Sprintf("Display %d", stamp)},
		{label: "name_fallback", name: fmt.Sprintf("Pool %d", stamp), want: fmt.Sprintf("Pool %d", stamp)},
		{label: "display_name", name: fmt.Sprintf("Pool %d", stamp), displayName: fmt.Sprintf("Display %d", stamp), want: fmt.Sprintf("Display %d", stamp)},
	} {
		t.Run(tc.label, func(t *testing.T) {
			_, err := a.store.Pool().Exec(ctx, "UPDATE pools SET name=$1,display_name=$2 WHERE chain_id=$3 AND address=$4", tc.name, tc.displayName, a.chainID, poolAddress)
			require.NoError(t, err)
			for _, address := range []string{account, poolAddress} {
				logs.Reset()
				req := httptest.NewRequest(http.MethodGet, "/api/v2/accounts/"+address+"/activity?perPage=20", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				res := httptest.NewRecorder()
				a.router.ServeHTTP(res, req)
				require.Equal(t, http.StatusOK, res.Code, "body=%s logs=%s", res.Body.String(), logs.String())
				var body struct {
					Result struct {
						Activity []store.FederationAccountActivity `json:"activity"`
					} `json:"result"`
				}
				require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
				require.Len(t, body.Result.Activity, 2)
				for _, item := range body.Result.Activity {
					require.Equal(t, tc.want, item.PoolName)
					require.Equal(t, []string{poolAddress}, item.PoolAddresses)
				}
			}
		})
	}
	t.Run("account_without_activity", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/accounts/0x3333333333333333333333333333333333333333/activity?perPage=20", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		var body struct {
			Result struct {
				Activity []store.FederationAccountActivity `json:"activity"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
		require.Empty(t, body.Result.Activity)
	})
}
