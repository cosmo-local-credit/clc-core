package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWantDiscoveryGenericPlanDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	var ownerID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO users(blockchain_address,name,email)
		VALUES('0x00000000000000000000000000000000feed0570','Want plan owner','want-plan@example.test')
		RETURNING id`).Scan(&ownerID))
	_, err := tx.Exec(ctx, `DROP INDEX wants_public_page_idx`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		INSERT INTO wants(owner_user_id,name,description,location_name,geo_lat,geo_lng,status,contact_text)
		SELECT $1,'Want plan '||g,'Representative discovery row','Nairobi',-1.28,36.82,
		       CASE WHEN g % 100 = 0 THEN 'OPEN' ELSE 'CLOSED' END,'Contact'
		FROM generate_series(1,13000) g`, ownerID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `CREATE INDEX wants_public_page_idx ON wants(id DESC) WHERE status='OPEN'`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "ANALYZE wants")
	require.NoError(t, err)

	plan := explainGenericPlan(t, tx, pg.queries.ListWants)
	if !strings.Contains(plan, "wants_public_page_idx") {
		t.Fatalf("Want discovery must retain its lifecycle index at representative scale.\nplan:\n%s", plan)
	}
}
