package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	WantStatusOpen   = "OPEN"
	WantStatusClosed = "CLOSED"

	WantContactEmail = 1
	WantContactPhone = 2
	WantContactAll   = WantContactEmail | WantContactPhone
)

type (
	Want struct {
		ID                uint64     `db:"id"                 json:"id"`
		OwnerUserID       uint64     `db:"owner_user_id"      json:"ownerUserId"`
		Name              string     `db:"name"               json:"name"`
		Description       string     `db:"description"        json:"description"`
		LocationName      string     `db:"location_name"      json:"locationName"`
		GeoLat            float64    `db:"geo_lat"            json:"geoLat"`
		GeoLng            float64    `db:"geo_lng"            json:"geoLng"`
		StartsAt          time.Time  `db:"starts_at"          json:"startsAt"`
		ExpiresAt         *time.Time `db:"expires_at"         json:"expiresAt"`
		Status            string     `db:"status"             json:"status"`
		State             string     `db:"state"              json:"state"`
		ImageURL          *string    `db:"image_url"          json:"imageUrl"`
		EffectiveImageURL *string    `db:"effective_image_url" json:"effectiveImageUrl"`
		ContactPermission int        `db:"contact_permission" json:"contactPermission,omitempty"`
		ContactText       *string    `db:"contact_text"       json:"contactText,omitempty"`
		Email             *string    `db:"email"              json:"email,omitempty"`
		Phone             *string    `db:"phone"              json:"phone,omitempty"`
		MarkerIcon        string     `db:"marker_icon"        json:"markerIcon"`
		MarkerColor       string     `db:"marker_color"       json:"markerColor"`
		Tags              []string   `db:"tags"               json:"tags"`
		OwnerName         string     `db:"owner_name"         json:"ownerName"`
		OwnerUsername     *string    `db:"owner_username"     json:"ownerUsername"`
		DistanceMeters    *float64   `db:"distance_meters"    json:"distanceMeters,omitempty"`
		CreatedAt         time.Time  `db:"created_at"         json:"createdAt"`
		UpdatedAt         time.Time  `db:"updated_at"         json:"updatedAt"`
	}

	WantWrite struct {
		Name              string
		Description       string
		LocationName      string
		GeoLat            float64
		GeoLng            float64
		StartsAt          time.Time
		ExpiresAt         *time.Time
		ImageURL          *string
		ContactPermission int
		ContactText       *string
		MarkerIcon        string
		MarkerColor       string
		Tags              []string
	}
)

func (pg *Pg) ListWants(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	bounds *MarketVoucherBounds,
	page CatalogPage,
	cursor, limit uint64,
) ([]Want, error) {
	query := pg.queries.ListWants
	switch page {
	case CatalogNextPage:
		query = pg.queries.ListWantsNext
	case CatalogPrevPage:
		query = pg.queries.ListWantsPrev
	}
	hasBounds := bounds != nil
	minLng, minLat, maxLng, maxLat := 0.0, 0.0, 0.0, 0.0
	if bounds != nil {
		minLng, minLat, maxLng, maxLat = bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat
	}
	nearby, originLat, originLng := false, 0.0, 0.0
	if filter.Nearby != nil {
		nearby, originLat, originLng = true, filter.Nearby.Lat, filter.Nearby.Lng
		if bounds == nil {
			box := filter.Nearby.bounds()
			hasBounds = true
			minLng, minLat, maxLng, maxLat = box.MinLng, box.MinLat, box.MaxLng, box.MaxLat
		}
	}
	rows := []Want{}
	err := pgxscan.Select(ctx, tx, &rows, query,
		filter.likeTerm(), filter.prefixOnly(), filter.Tags, filter.TagMatch == TagMatchAll,
		filter.PoolIDs, hasBounds, minLng, minLat, maxLng, maxLat, cursor, limit,
		nearby, originLat, originLng,
	)
	return rows, err
}

func (pg *Pg) ListWantTagFacets(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	bounds *MarketVoucherBounds,
) ([]TagFacet, error) {
	hasBounds := bounds != nil
	minLng, minLat, maxLng, maxLat := 0.0, 0.0, 0.0, 0.0
	if bounds != nil {
		minLng, minLat, maxLng, maxLat = bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat
	}
	nearby, originLat, originLng := false, 0.0, 0.0
	if filter.Nearby != nil {
		nearby, originLat, originLng = true, filter.Nearby.Lat, filter.Nearby.Lng
		if bounds == nil {
			box := filter.Nearby.bounds()
			hasBounds = true
			minLng, minLat, maxLng, maxLat = box.MinLng, box.MinLat, box.MaxLng, box.MaxLat
		}
	}
	rows := []TagFacet{}
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.ListWantTagFacets,
		filter.likeTerm(), filter.prefixOnly(), filter.PoolIDs,
		hasBounds, minLng, minLat, maxLng, maxLat, nearby, originLat, originLng,
	)
	return rows, err
}

func (pg *Pg) ListPoolWants(ctx context.Context, tx pgx.Tx, address string, page CatalogPage, cursor, limit uint64) ([]Want, error) {
	query := pg.queries.ListPoolWants
	switch page {
	case CatalogNextPage:
		query = pg.queries.ListPoolWantsNext
	case CatalogPrevPage:
		query = pg.queries.ListPoolWantsPrev
	}
	rows := []Want{}
	err := pgxscan.Select(ctx, tx, &rows, query, address, cursor, limit)
	return rows, err
}

func (pg *Pg) ListOwnedWants(ctx context.Context, tx pgx.Tx, ownerID, cursor, limit uint64) ([]Want, error) {
	rows := []Want{}
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.ListOwnedWants, ownerID, cursor, limit)
	return rows, err
}

func (pg *Pg) GetWant(ctx context.Context, tx pgx.Tx, id uint64, ownerID *uint64) (*Want, error) {
	rows := []Want{}
	if err := pgxscan.Select(ctx, tx, &rows, pg.queries.GetWant, id, ownerID); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (pg *Pg) LockWantOwner(ctx context.Context, tx pgx.Tx, id uint64) (uint64, error) {
	var ownerID uint64
	err := tx.QueryRow(ctx, pg.queries.LockWantOwner, id).Scan(&ownerID)
	return ownerID, err
}

func (pg *Pg) CreateWant(ctx context.Context, tx pgx.Tx, ownerID uint64, write WantWrite) (uint64, error) {
	var id uint64
	err := tx.QueryRow(ctx, pg.queries.CreateWant,
		ownerID, write.Name, write.Description, write.LocationName, write.GeoLat, write.GeoLng,
		write.StartsAt, write.ExpiresAt, write.ImageURL, write.ContactPermission, write.ContactText,
		write.MarkerIcon, write.MarkerColor,
	).Scan(&id)
	return id, err
}

func (pg *Pg) UpdateWant(ctx context.Context, tx pgx.Tx, id uint64, write WantWrite, reopen bool) error {
	_, err := tx.Exec(ctx, pg.queries.UpdateWant,
		id, write.Name, write.Description, write.LocationName, write.GeoLat, write.GeoLng,
		write.StartsAt, write.ExpiresAt, write.ImageURL, write.ContactPermission, write.ContactText,
		write.MarkerIcon, write.MarkerColor, reopen,
	)
	return err
}

func (pg *Pg) SetWantStatus(ctx context.Context, tx pgx.Tx, id uint64, status string) error {
	_, err := tx.Exec(ctx, pg.queries.SetWantStatus, id, status)
	return err
}

func (pg *Pg) DeleteWant(ctx context.Context, tx pgx.Tx, id uint64) error {
	_, err := tx.Exec(ctx, pg.queries.DeleteWant, id)
	return err
}

func (pg *Pg) ReplaceWantTags(ctx context.Context, tx pgx.Tx, id uint64, tags []string) error {
	if _, err := tx.Exec(ctx, pg.queries.DeleteWantTags, id); err != nil {
		return err
	}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx, pg.queries.InsertWantTag, id, tag); err != nil {
			return err
		}
	}
	return nil
}
