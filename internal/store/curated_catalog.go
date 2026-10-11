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
		ListCuratedCatalogs(context.Context, pgx.Tx, uint64, string, *CatalogLocation, CuratedCatalogCursor, uint64) ([]CuratedCatalog, error)
		ListCuratedMembers(context.Context, pgx.Tx, uint64, bool, uint64, uint64) ([]CuratedCatalogMember, error)
		SetCuratedMembership(context.Context, pgx.Tx, uint64, bool, uint64, bool) error
		CuratedMembership(context.Context, pgx.Tx, uint64, bool, uint64) (bool, error)
	}
)

// Counts and member reads share public-detail visibility: all pools, active vouchers.
// Coordinates are not required for a voucher's public detail or explicit curation.
const (
	curatedProjection = `SELECT c.id,c.name,c.description,c.image_url,c.location_name,c.geo_lat,c.geo_lng,
    u.blockchain_address AS owner_address,c.created_at,c.updated_at,
    (SELECT count(*) FROM catalog_pools cp JOIN pools p ON p.id=cp.pool_id WHERE cp.catalog_id=c.id) AS pool_count,
    (SELECT count(*) FROM catalog_vouchers cv JOIN vouchers v ON v.id=cv.voucher_id WHERE cv.catalog_id=c.id AND v.active) AS voucher_count,`
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
	err := pgxscan.Get(ctx, tx, &row, curatedProjection+`NULL::float8 AS distance_meters FROM catalogs c JOIN users u ON u.id=c.owner_user_id WHERE c.id=$1`, id)
	return row, err
}

// composeCuratedCatalogPage selects and orders the page before projecting its
// member counts. ownerID zero leaves the public list unrestricted.
func nullableDescription(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func curatedCatalogSearchPredicate(args *sqlArgs, term string) string {
	if term == "" {
		return ""
	}
	filter := CatalogFilter{Term: term}
	placeholder := args.bind(filter.likeTerm())
	if filter.prefixOnly() {
		return `(lower(c.name) LIKE lower(` + placeholder + `) || '%' ESCAPE '\'
            OR lower(c.location_name) LIKE lower(` + args.reuse() + `) || '%' ESCAPE '\'
            OR lower(COALESCE(c.description, '')) LIKE lower(` + args.reuse() + `) || '%' ESCAPE '\')`
	}
	return `(lower(c.name) LIKE '%' || lower(` + placeholder + `) || '%' ESCAPE '\'
        OR lower(c.location_name) LIKE '%' || lower(` + args.reuse() + `) || '%' ESCAPE '\'
        OR lower(COALESCE(c.description, '')) LIKE '%' || lower(` + args.reuse() + `) || '%' ESCAPE '\')`
}

func composeCuratedCatalogPage(ownerID uint64, term string, origin *CatalogLocation, cursor CuratedCatalogCursor, limit uint64) composed {
	args := &sqlArgs{}
	distance := "NULL::float8"
	where := "TRUE"
	if ownerID != 0 {
		where += " AND c.owner_user_id = " + args.bind(ownerID)
	}
	if search := curatedCatalogSearchPredicate(args, term); search != "" {
		where += " AND " + search
	}
	if origin != nil {
		where += " AND " + nearbyPredicate(args, origin, "c.geo_lat", "c.geo_lng")
		lat, lng := args.bind(origin.Lat)+"::float8", args.bind(origin.Lng)+"::float8"
		distance = haversineMetersSQL("c.geo_lat", "c.geo_lng", lat, lng)
	}
	pageWhere := "TRUE"
	if cursor.ID != 0 {
		if origin == nil {
			pageWhere = "id < " + args.bind(cursor.ID)
		} else {
			pageWhere = "(distance_meters, id) > (" + args.bind(cursor.Distance) + "::float8, " + args.bind(cursor.ID) + ")"
		}
	}
	pageOrder := "id DESC"
	resultOrder := "page.id DESC"
	if origin != nil {
		pageOrder = "distance_meters ASC, id ASC"
		resultOrder = "page.distance_meters ASC, page.id ASC"
	}
	sql := `WITH candidates AS (
    SELECT c.id, ` + distance + ` AS distance_meters
    FROM catalogs c
    WHERE ` + where + `
), page AS MATERIALIZED (
    SELECT id, distance_meters FROM candidates
    WHERE ` + pageWhere + `
    ORDER BY ` + pageOrder + `
    LIMIT ` + args.bind(limit) + `
)
` + curatedProjection + `page.distance_meters
FROM page
JOIN catalogs c ON c.id = page.id
JOIN users u ON u.id = c.owner_user_id
ORDER BY ` + resultOrder
	return composed{sql: sql, args: args.values}
}

// ListCuratedCatalogs paginates after distance calculation in PostgreSQL, never on an already
// paged client subset. The ID tie-breaker preserves catalogs at identical coordinates.
func (pg *Pg) ListCuratedCatalogs(ctx context.Context, tx pgx.Tx, ownerID uint64, term string, origin *CatalogLocation, cursor CuratedCatalogCursor, limit uint64) ([]CuratedCatalog, error) {
	query := composeCuratedCatalogPage(ownerID, term, origin, cursor, limit)
	rows := []CuratedCatalog{}
	if err := pgxscan.Select(ctx, tx, &rows, query.sql, query.args...); err != nil {
		return nil, err
	}
	return rows, nil
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
