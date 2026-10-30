package store

import (
	"context"
	"testing"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/stretchr/testify/require"
)

func TestPoolActivityTaxedTransfersDB(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
        CREATE TEMP TABLE pools(contract_address TEXT) ON COMMIT DROP;
        CREATE TEMP TABLE tx(id BIGINT,tx_hash TEXT,date_block TIMESTAMP,success BOOLEAN) ON COMMIT DROP;
        CREATE TEMP TABLE token_transfer(tx_id BIGINT,log_index BIGINT,contract_address TEXT,
            transfer_value NUMERIC,sender_address TEXT,recipient_address TEXT) ON COMMIT DROP;
        CREATE TEMP TABLE pool_swap(tx_id BIGINT,log_index BIGINT,contract_address TEXT,initiator_address TEXT,
            token_in_address TEXT,token_out_address TEXT,in_value NUMERIC,out_value NUMERIC,fee NUMERIC) ON COMMIT DROP;
        CREATE TEMP TABLE pool_withdrawal(id BIGINT,tx_id BIGINT,contract_address TEXT,kind TEXT,
            initiator_address TEXT,token_address TEXT,recipient_address TEXT,amount NUMERIC,log_index BIGINT) ON COMMIT DROP;
        INSERT INTO tx SELECT n,'tx-'||n,'2026-10-29',true FROM generate_series(1,7) n;
        INSERT INTO token_transfer VALUES
            (1,1,'taxed',10,'pool','tax'),(1,2,'taxed',90,'pool','user'),
            (2,1,'input',113,'user','pool'),(2,2,'taxed',1,'pool','protocol'),
            (2,3,'taxed',11,'pool','tax'),(2,4,'taxed',99,'pool','user'),
            (3,1,'input',113,'user','pool'),(3,2,'taxed',1,'pool','protocol'),
            (3,4,'taxed',110,'pool','user'),
            (4,1,'taxed',10,'pool','tax'),(4,2,'taxed',90,'pool','collector'),
            (5,1,'taxed',30,'pool','user'),(5,2,'taxed',10,'pool','tax'),(5,3,'taxed',90,'pool','user'),
            (6,1,'input',100,'user','pool'),(6,2,'taxed',10,'pool','tax'),(6,3,'taxed',90,'pool','user'),
            (7,1,'input',100,'user','pool'),(7,3,'taxed',100,'pool','user');
        INSERT INTO pool_swap VALUES
            (2,5,'pool','user','input','taxed',113,99,2),
            (3,5,'pool','user','input','taxed',113,110,2),
            (6,4,'pool','user','input','taxed',100,90,0),
            (7,4,'pool','user','input','taxed',100,100,0);
        INSERT INTO pool_withdrawal VALUES
            (1,1,'pool','liquidity','owner','taxed','user',100,3),
            (2,4,'pool','fees',NULL,'taxed','collector',100,3),
            (3,5,'pool','liquidity','owner','taxed','user',100,4),
            -- Missing transfer evidence must not steal the earlier swap's output.
            (4,6,'pool','liquidity','owner','taxed','user',90,5);
    `)
	require.NoError(t, err)
	read := func(account string, kinds []string) []FederationAccountActivity {
		t.Helper()
		var rows []FederationAccountActivity
		require.NoError(t, pgxscan.Select(ctx, tx, &rows, queries.ListUserActivity,
			account, kinds, []string{}, "", nil, true, 50, []string{"pool"}, nil))
		return rows
	}
	for _, account := range []string{"pool", "user"} {
		swaps := read(account, []string{"swapped"})
		require.Len(t, swaps, 4)
		require.Nil(t, swaps[0].Fee, "no auxiliary transfer does not prove a protocol charge")
		require.Nil(t, swaps[1].Fee, "zero pool fee does not prove zero protocol fee; the transfer can be tax")
		require.Nil(t, swaps[2].Fee, "even one auxiliary transfer does not prove a protocol fee")
		require.Nil(t, swaps[3].Fee, "multiple auxiliary transfers cannot distinguish fee 1 from tax 11")
		withdrawals := read(account, []string{"withdrawn"})
		require.Len(t, withdrawals, 2)
		for _, row := range withdrawals {
			require.Equal(t, "90", *row.Amount)
			require.Equal(t, "100", *row.NominalAmount)
			require.Equal(t, "owner", *row.InitiatorAddress)
		}
	}
	all := read("pool", []string{})
	for _, row := range all {
		if row.TxHash == "tx-2" || row.TxHash == "tx-3" {
			require.Equal(t, "swapped", row.Kind, "tax/protocol legs must not leak as extra payouts")
		}
		if row.TxHash == "tx-5" && row.Kind == "pool_payout" && *row.RecipientAddress == "user" {
			require.Equal(t, "30", *row.Amount, "unrelated earlier payment remains visible")
		}
	}
	collection := read("collector", []string{"fee_withdrawn"})
	require.Len(t, collection, 1)
	require.Equal(t, "90", *collection[0].Amount)
	require.Equal(t, "100", *collection[0].NominalAmount)
	require.Nil(t, collection[0].InitiatorAddress)
	owner := read("owner", []string{"withdrawn"})
	require.Len(t, owner, 3)
	for _, row := range owner {
		require.Equal(t, "NONE", *row.Direction)
	}
}
