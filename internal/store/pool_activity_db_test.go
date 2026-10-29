package store

import (
	"context"
	"strings"
	"testing"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/stretchr/testify/require"
)

func TestPoolActivityDirectionsAndKindsDB(t *testing.T) {
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
        INSERT INTO tx SELECT id,'tx-'||id,'2026-10-01'::timestamp+id*interval '1 hour',true FROM generate_series(1,13) id;
        INSERT INTO token_transfer VALUES
            (1,1,'in',40,'user','pool'),
            (2,1,'in',7,'pool','user'),
            (3,1,'out',3,'pool','collector'),
            (4,1,'in',5,'pool','user'),
            (5,1,'in',10,'user','pool'),
            (5,2,'out',1,'pool','protocol'),
            (5,3,'out',9,'pool','user'),
            (5,5,'in',10,'user','pool'),
            (5,6,'extra',99,'user','friend'),
            (6,1,'in',11,'user','pool'),
            (6,2,'out',12,'pool','custom'),
            (7,1,'in',20,'user','pool'),
            (7,2,'out',22,'pool','user'),
            (7,4,'in',30,'friend','pool'),
            (7,5,'out',33,'pool','friend'),
            (8,1,'in',7,'pool','third'),
            (9,1,'in',17,'0x0000000000000000000000000000000000000000','pool'),
            (10,1,'in',8,'user','pool'),
            (11,1,'in',9,'USER','POOL'),
            (12,1,'in',6,'pool','pool-two'),
            (13,1,'in',8,'pool','pool-two');
        INSERT INTO pool_swap VALUES
            (5,4,'pool','user','in','out',10,9,2),
            (6,3,'pool','user','in','out',11,12,0),
            (7,3,'pool','user','in','out',20,22,0),
            (7,6,'pool','friend','in','out',30,33,0);
        INSERT INTO pool_withdrawal VALUES
            (1,2,'pool','liquidity','owner','in','user',7,2),
            (2,3,'pool','fees',NULL,'out','collector',3,2),
            (3,8,'pool','liquidity','owner','in','third',7,2),
            (4,13,'pool','liquidity','owner','in','pool-two',8,2);
    `)
	require.NoError(t, err)
	query := func(account string, kinds []string, cursor any, limit int) []FederationAccountActivity {
		t.Helper()
		var rows []FederationAccountActivity
		require.NoError(t, pgxscan.Select(ctx, tx, &rows, queries.ListUserActivity, account, kinds, []string{}, "", cursor, true, limit, []string{"pool", "pool-two"}))
		return rows
	}
	deposits := query("user", []string{"deposited"}, nil, 20)
	require.Len(t, deposits, 4, "direct sends, including one batched after a swap, remain deposits")
	for _, row := range deposits {
		require.Equal(t, "OUT", *row.Direction)
		require.Equal(t, []string{"pool"}, lowerAddressSlice(row.PoolAddresses))
	}
	first := query("user", []string{"deposited"}, nil, 1)
	next := query("user", []string{"deposited"}, int64(first[0].ID), 1)
	require.Len(t, next, 1, "type filtering must happen before the page limit")
	require.Less(t, next[0].ID, first[0].ID)
	withdrawals := query("user", []string{"withdrawn"}, nil, 20)
	require.Len(t, withdrawals, 1)
	require.Equal(t, "IN", *withdrawals[0].Direction)
	require.Equal(t, "owner", *withdrawals[0].InitiatorAddress)
	payouts := query("user", []string{"pool_payout"}, nil, 20)
	require.Len(t, payouts, 2, "Pay Out includes verified withdrawals")
	require.Equal(t, AccountActivityPoolPayout, payouts[0].Kind)
	require.Equal(t, "5", *payouts[0].Amount, "legacy outflow remains general")
	require.Len(t, query("collector", []string{"fee_withdrawn"}, nil, 20), 1)
	owner := query("owner", []string{"withdrawn"}, nil, 20)
	require.Len(t, owner, 3, "an owner sees withdrawals to separate recipients")
	require.Equal(t, "NONE", *owner[0].Direction, "initiating a withdrawal does not change the owner's balance")
	poolSwaps := query("pool", []string{"swapped"}, nil, 20)
	require.Len(t, poolSwaps, 4, "separate swaps through one pool in one transaction are separate operations")
	var swap FederationAccountActivity
	for _, row := range poolSwaps {
		if row.TxHash == "tx-5" {
			swap = row
		}
	}
	require.Equal(t, "out", *swap.TokenInAddress, "the pool spends the output voucher")
	require.Equal(t, "9", *swap.AmountIn)
	require.Equal(t, "in", *swap.TokenOutAddress, "the pool receives the input voucher")
	require.Equal(t, "10", *swap.AmountOut)
	require.Equal(t, "1", *swap.Fee, "the observed protocol transfer differs from the stored pool fee")
	require.Equal(t, "out", *swap.FeeTokenAddress)
	require.Len(t, query("pool", []string{"deposited"}, nil, 20), 4, "includes the other trader's deposit only when outside swaps")
	require.Len(t, query("pool", []string{"withdrawn"}, nil, 20), 3)
	require.Len(t, query("pool", []string{"fee_withdrawn"}, nil, 20), 1)
	require.Len(t, query("pool", []string{"creation"}, nil, 20), 1)
	poolPayouts := query("pool", []string{"pool_payout"}, nil, 20)
	require.Len(t, poolPayouts, 6, "parent filter includes liquidity and fee withdrawals")
	require.Equal(t, "withdrawn", poolPayouts[0].Kind)
	require.Equal(t, "pool_payout", poolPayouts[1].Kind)
	for _, row := range poolPayouts {
		require.Equal(t, "OUT", *row.Direction)
	}
	destination := query("pool-two", []string{"deposited"}, nil, 20)
	require.Len(t, destination, 2, "pool-to-pool sends are deposits for the destination")
	for _, row := range destination {
		require.Equal(t, "IN", *row.Direction)
		require.Equal(t, []string{"pool-two"}, row.PoolAddresses)
	}
	custom := query("custom", []string{}, nil, 20)
	require.Len(t, custom, 1)
	require.Equal(t, "received", custom[0].Kind, "a custom swap recipient did not withdraw liquidity")
	require.Len(t, query("user", []string{"sent"}, nil, 20), 1, "unrelated batched sends stay visible")
}

func lowerAddressSlice(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ToLower(value)
	}
	return result
}
