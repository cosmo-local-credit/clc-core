package store

import (
	"context"
	"math"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func seedCuratedOwner(t *testing.T, tx pgx.Tx) uint64 {
	t.Helper()
	var id uint64
	err := tx.QueryRow(context.Background(), `INSERT INTO users(blockchain_address,name,email)
        VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Catalog fixture',gen_random_uuid()::text||'@catalog.test') RETURNING id`).Scan(&id)
	require.NoError(t, err)
	return id
}
func TestCuratedCatalogMembershipAndVisibilityDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	zero := 0.0
	owner := seedCuratedOwner(t, tx)
	id, err := pg.CreateCuratedCatalog(ctx, tx, owner, CuratedCatalogWrite{Name: "Čaršija", LocationName: "Sarajevo", GeoLat: &zero, GeoLng: &zero})
	require.NoError(t, err)
	v := seedSearchVoucher(t, tx, "Catalog independent voucher", "ZCATMEM")
	var pool uint64
	err = tx.QueryRow(ctx, `INSERT INTO pools(chain_id,address,name) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Catalog pool') RETURNING id`).Scan(&pool)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id) VALUES($1,$2)`, pool, v)
	require.NoError(t, err)
	require.NoError(t, pg.SetCuratedMembership(ctx, tx, id, false, pool, true))
	require.NoError(t, pg.SetCuratedMembership(ctx, tx, id, false, pool, true))
	row, err := pg.GetCuratedCatalog(ctx, tx, id)
	require.NoError(t, err)
	require.Equal(t, int64(1), row.PoolCount)
	require.Zero(t, row.VoucherCount)
	require.NoError(t, pg.SetCuratedMembership(ctx, tx, id, true, v, true))
	_, err = tx.Exec(ctx, `DELETE FROM pool_tokens WHERE pool_id=$1 AND voucher_id=$2`, pool, v)
	require.NoError(t, err)
	members, err := pg.ListCuratedMembers(ctx, tx, id, true, 0, 20)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.NoError(t, pg.SetCuratedMembership(ctx, tx, id, false, pool, false))
	require.NoError(t, pg.SetCuratedMembership(ctx, tx, id, false, pool, false))
	members, err = pg.ListCuratedMembers(ctx, tx, id, true, 0, 20)
	require.NoError(t, err)
	require.Len(t, members, 1)
	_, err = tx.Exec(ctx, `UPDATE vouchers SET active=false WHERE id=$1`, v)
	require.NoError(t, err)
	row, err = pg.GetCuratedCatalog(ctx, tx, id)
	require.NoError(t, err)
	require.Zero(t, row.VoucherCount)
	members, err = pg.ListCuratedMembers(ctx, tx, id, true, 0, 20)
	require.NoError(t, err)
	require.Empty(t, members)
	require.NotNil(t, members)
	require.ErrorIs(t, pg.SetCuratedMembership(ctx, tx, id, true, v, true), pgx.ErrNoRows)
	require.NoError(t, pg.SetCuratedMembership(ctx, tx, id, true, v, false))
	require.ErrorIs(t, pg.SetCuratedMembership(ctx, tx, id, false, 2147483647, true), pgx.ErrNoRows)
}

func TestCuratedCatalogDistancePaginationDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	// This isolated list must only see the fixtures in this rolled-back transaction.
	_, err := tx.Exec(ctx, `DELETE FROM catalogs`)
	require.NoError(t, err)
	origin := &CatalogLocation{Lat: 0, Lng: 0}
	var ids []uint64
	boundary := 1_000_000.0 / earthRadiusMeters * 180 / math.Pi
	for _, lng := range []float64{2, 1, 1, boundary - 0.00001, boundary + 0.00001} {
		lat := 0.0
		owner := seedCuratedOwner(t, tx)
		id, e := pg.CreateCuratedCatalog(ctx, tx, owner, CuratedCatalogWrite{Name: "Distance", LocationName: "Equator", GeoLat: &lat, GeoLng: &lng})
		require.NoError(t, e)
		ids = append(ids, id)
	}
	first, err := pg.ListCuratedCatalogs(ctx, tx, 0, CatalogFilter{}, origin, CuratedCatalogCursor{}, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, ids[1], first[0].ID)
	next, err := pg.ListCuratedCatalogs(ctx, tx, 0, CatalogFilter{}, origin, CuratedCatalogCursor{ID: first[0].ID, Distance: *first[0].DistanceMeters}, 10)
	require.NoError(t, err)
	require.Len(t, next, 3)
	require.Equal(t, []uint64{ids[2], ids[0], ids[3]}, []uint64{next[0].ID, next[1].ID, next[2].ID})
	for _, row := range next {
		require.LessOrEqual(t, *row.DistanceMeters, 1_000_000.0)
	}
	guests, err := pg.ListCuratedCatalogs(ctx, tx, 0, CatalogFilter{}, nil, CuratedCatalogCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, guests, 5)
	require.Equal(t, ids[4], guests[0].ID)
	require.Nil(t, guests[0].DistanceMeters)
	publicNext, err := pg.ListCuratedCatalogs(ctx, tx, 0, CatalogFilter{}, nil, CuratedCatalogCursor{ID: guests[0].ID}, 2)
	require.NoError(t, err)
	require.Equal(t, []uint64{ids[3], ids[2]}, []uint64{publicNext[0].ID, publicNext[1].ID})
	terminal, err := pg.ListCuratedCatalogs(ctx, tx, 0, CatalogFilter{}, nil, CuratedCatalogCursor{ID: ids[0]}, 2)
	require.NoError(t, err)
	require.Empty(t, terminal)
	require.NotNil(t, terminal)
}
