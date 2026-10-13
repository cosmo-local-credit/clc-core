package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	CuratedCatalog struct {
		ID             uint64    `db:"id" json:"id"`
		Name           string    `db:"name" json:"name"`
		Description    *string   `db:"description" json:"description"`
		ImageURL       *string   `db:"image_url" json:"imageUrl"`
		LocationName   string    `db:"location_name" json:"locationName"`
		GeoLat         float64   `db:"geo_lat" json:"geoLat"`
		GeoLng         float64   `db:"geo_lng" json:"geoLng"`
		OwnerAddress   string    `db:"owner_address" json:"ownerAddress"`
		PoolCount      int64     `db:"pool_count" json:"poolCount"`
		VoucherCount   int64     `db:"voucher_count" json:"voucherCount"`
		DistanceMeters *float64  `db:"distance_meters" json:"distanceMeters"`
		Tags           []string  `db:"tags" json:"tags"`
		CreatedAt      time.Time `db:"created_at" json:"createdAt"`
		UpdatedAt      time.Time `db:"updated_at" json:"updatedAt"`
	}
	CuratedCatalogWrite struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		ImageURL     *string  `json:"imageUrl"`
		LocationName string   `json:"locationName"`
		GeoLat       *float64 `json:"geoLat"`
		GeoLng       *float64 `json:"geoLng"`
		Tags         []string `json:"tags"`
	}
	CuratedCatalogMember struct {
		ID       uint64  `db:"id" json:"id"`
		ChainID  int64   `db:"chain_id" json:"chainId"`
		Address  string  `db:"address" json:"address"`
		Name     string  `db:"name" json:"name"`
		ImageURL *string `db:"image_url" json:"imageUrl"`
	}
	CuratedCatalogCursor struct {
		ID       uint64  `json:"id"`
		Distance float64 `json:"distance"`
		Origin   string  `json:"origin"`
	}
	CuratedCatalogStore interface {
		CreateCuratedCatalog(context.Context, pgx.Tx, uint64, CuratedCatalogWrite) (uint64, error)
		UpdateCuratedCatalog(context.Context, pgx.Tx, uint64, CuratedCatalogWrite) error
		LockCuratedCatalog(context.Context, pgx.Tx, uint64) (uint64, error)
		GetCuratedCatalog(context.Context, pgx.Tx, uint64) (CuratedCatalog, error)
		ListCuratedCatalogs(context.Context, pgx.Tx, uint64, CatalogFilter, *CatalogLocation, CuratedCatalogCursor, uint64) ([]CuratedCatalog, error)
		ListCuratedCatalogTagFacets(context.Context, pgx.Tx, uint64, CatalogFilter, *CatalogLocation) ([]TagFacet, error)
		ReplaceCuratedCatalogTags(context.Context, pgx.Tx, uint64, []string) error
		ListCuratedMembers(context.Context, pgx.Tx, uint64, bool, uint64, uint64) ([]CuratedCatalogMember, error)
		SetCuratedMembership(context.Context, pgx.Tx, uint64, bool, uint64, bool) error
		CuratedMembership(context.Context, pgx.Tx, uint64, bool, uint64) (bool, error)
	}
)

func (pg *Pg) CreateCuratedCatalog(ctx context.Context, tx pgx.Tx, owner uint64, w CuratedCatalogWrite) (uint64, error) {
	var id uint64
	err := tx.QueryRow(ctx, pg.queries.CreateCuratedCatalog, owner, w.Name, nullableDescription(w.Description), w.ImageURL, w.LocationName, w.GeoLat, w.GeoLng).Scan(&id)
	return id, err
}
func (pg *Pg) UpdateCuratedCatalog(ctx context.Context, tx pgx.Tx, id uint64, w CuratedCatalogWrite) error {
	_, err := tx.Exec(ctx, pg.queries.UpdateCuratedCatalog, id, w.Name, nullableDescription(w.Description), w.ImageURL, w.LocationName, w.GeoLat, w.GeoLng)
	return err
}
func (pg *Pg) LockCuratedCatalog(ctx context.Context, tx pgx.Tx, id uint64) (uint64, error) {
	var owner uint64
	err := tx.QueryRow(ctx, pg.queries.LockCuratedCatalog, id).Scan(&owner)
	return owner, err
}
func (pg *Pg) GetCuratedCatalog(ctx context.Context, tx pgx.Tx, id uint64) (CuratedCatalog, error) {
	var row CuratedCatalog
	err := pgxscan.Get(ctx, tx, &row, pg.queries.GetCuratedCatalog, id)
	return row, err
}

func nullableDescription(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func curatedCatalogPageQuery(queries *Queries, ownerID uint64, filter CatalogFilter, origin *CatalogLocation, cursor CuratedCatalogCursor, limit uint64) (string, []any) {
	query := queries.ListCuratedCatalogsGlobal
	args := []any{ownerID, filter.likeTerm(), filter.prefixOnly(), filter.Tags, filter.TagMatch == TagMatchAll, filter.PoolIDs, cursor.ID, limit}
	if origin != nil {
		query = queries.ListCuratedCatalogsNearby
		bounds := origin.bounds()
		args = []any{
			ownerID, filter.likeTerm(), filter.prefixOnly(), filter.Tags, filter.TagMatch == TagMatchAll, filter.PoolIDs, origin.Lat, origin.Lng,
			bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat,
			cursor.Distance, cursor.ID, limit,
		}
	}
	return query, args
}

// ListCuratedCatalogs paginates after distance calculation in PostgreSQL, never on an already
// paged client subset. The ID tie-breaker preserves catalogs at identical coordinates.
func (pg *Pg) ListCuratedCatalogs(ctx context.Context, tx pgx.Tx, ownerID uint64, filter CatalogFilter, origin *CatalogLocation, cursor CuratedCatalogCursor, limit uint64) ([]CuratedCatalog, error) {
	query, args := curatedCatalogPageQuery(pg.queries, ownerID, filter, origin, cursor, limit)
	rows := []CuratedCatalog{}
	if err := pgxscan.Select(ctx, tx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

func (pg *Pg) ListCuratedCatalogTagFacets(ctx context.Context, tx pgx.Tx, ownerID uint64, filter CatalogFilter, origin *CatalogLocation) ([]TagFacet, error) {
	query := pg.queries.ListCuratedCatalogTagFacetsGlobal
	args := []any{ownerID, filter.likeTerm(), filter.prefixOnly(), filter.PoolIDs}
	if origin != nil {
		query = pg.queries.ListCuratedCatalogTagFacetsNearby
		bounds := origin.bounds()
		args = []any{ownerID, filter.likeTerm(), filter.prefixOnly(), filter.PoolIDs, origin.Lat, origin.Lng, bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat}
	}
	rows := []TagFacet{}
	err := pgxscan.Select(ctx, tx, &rows, query, args...)
	return rows, err
}

func (pg *Pg) ReplaceCuratedCatalogTags(ctx context.Context, tx pgx.Tx, id uint64, tags []string) error {
	if _, err := tx.Exec(ctx, pg.queries.DeleteCuratedCatalogTags, id); err != nil {
		return err
	}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx, pg.queries.InsertCuratedCatalogTag, id, tag); err != nil {
			return err
		}
	}
	return nil
}
func (pg *Pg) ListCuratedMembers(ctx context.Context, tx pgx.Tx, id uint64, voucher bool, after, limit uint64) ([]CuratedCatalogMember, error) {
	query := pg.queries.ListCuratedPools
	if voucher {
		query = pg.queries.ListCuratedVouchers
	}
	rows := []CuratedCatalogMember{}
	err := pgxscan.Select(ctx, tx, &rows, query, id, after, limit)
	if rows == nil {
		rows = []CuratedCatalogMember{}
	}
	return rows, err
}
func (pg *Pg) SetCuratedMembership(ctx context.Context, tx pgx.Tx, id uint64, voucher bool, resource uint64, added bool) error {
	query := pg.queries.RemoveCuratedPool
	if voucher {
		query = pg.queries.RemoveCuratedVoucher
	}
	if added {
		visible := pg.queries.VisibleCuratedPool
		query = pg.queries.AddCuratedPool
		if voucher {
			visible = pg.queries.VisibleCuratedVoucher
			query = pg.queries.AddCuratedVoucher
		}
		var found uint64
		// Hold visibility stable until this owner's transaction commits.
		if err := tx.QueryRow(ctx, visible, resource).Scan(&found); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, query, id, resource)
	return err
}
func (pg *Pg) CuratedMembership(ctx context.Context, tx pgx.Tx, id uint64, voucher bool, resource uint64) (bool, error) {
	query := pg.queries.CuratedPoolMembership
	if voucher {
		query = pg.queries.CuratedVoucherMembership
	}
	var added bool
	err := tx.QueryRow(ctx, query, id, resource).Scan(&added)
	return added, err
}
