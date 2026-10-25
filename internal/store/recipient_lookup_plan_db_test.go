package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestRecipientLookupIndexes(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	marker := "rp" + uuid.NewString()[:8]
	_, err = tx.Exec(ctx, `INSERT INTO users(blockchain_address,name,email,username,status)
        SELECT '0x' || md5($1 || i::TEXT) || '00000000', 'Plan User',
            $1 || i::TEXT || '@example.test', $1 || '-user' || i::TEXT, 'APPROVED'
        FROM generate_series(1,10000) i`, marker)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO pools(chain_id,address,name,symbol)
        SELECT 100, '0x' || md5($1 || i::TEXT) || '00000000',
            $1 || '-pool' || i::TEXT, $1 || '-symbol' || i::TEXT
        FROM generate_series(1,10000) i`, marker)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SELECT claim_identity('symbol',$1,'voucher',$2)`, marker+"-pending", "deploy:"+uuid.NewString())
	require.NoError(t, err)
	// Flush bulk-insert pending lists before checking settled GIN plans.
	// No scan paths are disabled in this test.
	for _, statement := range []string{"SELECT gin_clean_pending_list('pools_recipient_name_trgm_idx')", "ANALYZE users", "ANALYZE pools", "ANALYZE identity_claims", "SET LOCAL plan_cache_mode = force_generic_plan"} {
		_, err = tx.Exec(ctx, statement)
		require.NoError(t, err)
	}

	// PREPARE/EXECUTE works on PG14 too and checks the generic cached plans
	// used by pgx, rather than only a plan specialized to a literal query.
	for _, tc := range []struct {
		name, query, args string
		indexes           []string
	}{
		{"search", recipientSearchQuery, "'pool10000',100,'','pool10000'", []string{"users_recipient_username_exact_idx", "pools_recipient_name_trgm_idx"}},
		{"forward", recipientForwardLookup + " LIMIT 2", "'" + marker + "-pool10000',100", []string{"identity_claims_name_key", "users_blockchain_address_lower_key", "pools_chain_address_lower_key"}},
		{"cleanup", recipientDeploymentOwnersQuery, "", []string{"identity_claims_deployment_owner_idx"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "recipient_plan_" + tc.name
			_, err := tx.Exec(ctx, "PREPARE "+name+" AS "+tc.query)
			require.NoError(t, err)
			defer func() {
				_, err := tx.Exec(ctx, "DEALLOCATE "+name)
				if err != nil {
					t.Errorf("deallocate recipient plan %s: %v", name, err)
				}
			}()
			execute := "EXPLAIN (COSTS OFF) EXECUTE " + name
			if tc.args != "" {
				execute += "(" + tc.args + ")"
			}
			rows, err := tx.Query(ctx, execute)
			require.NoError(t, err)
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)
			plan := strings.Join(lines, "\n")
			t.Log(plan)
			for _, index := range tc.indexes {
				require.Contains(t, plan, index)
			}
		})
	}
}
