package store

import (
	"context"
	"strings"
	"testing"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/stretchr/testify/require"
)

func TestPublicProfileResourceResolutionUsesTypedIndexedIdentifiersDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	_, err := tx.Exec(ctx, `
        INSERT INTO users(blockchain_address,name,email,status)
        SELECT '0x'||lpad(to_hex(100000+g),40,'0'),
               'Profile plan user '||g,'profile-plan-'||g||'@example.test','APPROVED'
        FROM generate_series(1,12000) g;
        INSERT INTO vouchers(chain_id,address,name,owner_address,active)
        SELECT 100,'0x'||lpad(to_hex(200000+u.id),40,'0'),
               'Profile plan voucher '||u.id,u.blockchain_address,true
        FROM users u WHERE u.email LIKE 'profile-plan-%@example.test';
        INSERT INTO wants(owner_user_id,name,description,location_name,geo_lat,geo_lng,status,contact_text)
        SELECT u.id,'Profile plan Want '||u.id,'Public need','Nairobi',-1.286,36.817,'OPEN','Public instructions'
        FROM users u WHERE u.email LIKE 'profile-plan-%@example.test';
        INSERT INTO safe_state(chain_id,safe_address,threshold,block_number,log_index)
        SELECT 100,lower(u.blockchain_address),1,1,0
        FROM users u WHERE u.email LIKE 'profile-plan-%@example.test';
        INSERT INTO safe_signers(chain_id,safe_address,signer)
        SELECT 100,lower(u.blockchain_address),lower(u.blockchain_address)
        FROM users u WHERE u.email LIKE 'profile-plan-%@example.test';
    `)
	require.NoError(t, err)

	var targetID int64
	var targetAddress string
	require.NoError(t, tx.QueryRow(ctx, `
        SELECT id,blockchain_address FROM users
        WHERE email='profile-plan-1@example.test'`).Scan(&targetID, &targetAddress))
	const safeAddress = "0x00000000000000000000000000000000feed0850"
	const voucherAddress = "0x00000000000000000000000000000000feed0851"
	_, err = tx.Exec(ctx, `INSERT INTO safe_state(chain_id,safe_address,threshold,block_number,log_index)
        VALUES(100,$1,1,2,0)`, safeAddress)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO safe_signers(chain_id,safe_address,signer)
        VALUES(100,$1,lower($2))`, safeAddress, targetAddress)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO vouchers(chain_id,address,name,owner_address,active)
        VALUES(100,$1,'Profile resolver Safe fixture',$2,true)`, voucherAddress, safeAddress)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM wants WHERE owner_user_id=$1`, targetID).Scan(&targetID))
	_, err = tx.Exec(ctx, `ANALYZE users; ANALYZE vouchers; ANALYZE wants; ANALYZE safe_state; ANALYZE safe_signers`)
	require.NoError(t, err)

	planFor := func(args ...any) string {
		rows, queryErr := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+pg.queries.ResolvePublicResourceProfiles, args...)
		require.NoError(t, queryErr)
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			lines = append(lines, line)
		}
		require.NoError(t, rows.Err())
		return strings.Join(lines, "\n")
	}
	voucherPlan := planFor("voucher", voucherAddress, int64(0), int64(100))
	for _, index := range []string{"vouchers_chain_address_lower_key", "safe_signers_pkey", "users_blockchain_address_lower_key"} {
		require.Contains(t, voucherPlan, index, "voucher profile resolution must use %s:\n%s", index, voucherPlan)
	}
	wantPlan := planFor("want", "", targetID, int64(100))
	require.True(t, strings.Contains(wantPlan, "wants_pkey") || strings.Contains(wantPlan, "wants_public_page_idx"),
		"numeric Want identifiers must use an indexed id lookup:\n%s", wantPlan)
	require.Contains(t, wantPlan, "users_pkey", "Want ownership must use the creator primary key:\n%s", wantPlan)

	var profiles []PublicProfileReference
	require.NoError(t, pgxscan.Select(ctx, tx, &profiles, pg.queries.ResolvePublicResourceProfiles,
		"voucher", voucherAddress, int64(0), int64(100)))
	require.Len(t, profiles, 1)
	require.Equal(t, strings.ToLower(targetAddress), strings.ToLower(profiles[0].Address))

	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+pg.queries.ListPublicProfileWants,
		targetAddress, int64(0), int64(20))
	require.NoError(t, err)
	var lines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		lines = append(lines, line)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	sectionPlan := strings.Join(lines, "\n")
	require.Contains(t, sectionPlan, "users_blockchain_address_lower_key", sectionPlan)
	require.Contains(t, sectionPlan, "wants_owner_page_idx", sectionPlan)
}
