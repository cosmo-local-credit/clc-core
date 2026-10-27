package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	Offering struct {
		ID                    uint64    `db:"id"               json:"id"`
		SourceID              *uint64   `db:"source_id"        json:"sourceId"`
		VoucherID             uint64    `db:"voucher_id"       json:"voucherId"`
		VoucherAddress        string    `db:"voucher_address"  json:"voucherAddress"`
		VoucherName           *string   `db:"voucher_name"     json:"voucherName"`
		VoucherSymbol         *string   `db:"voucher_symbol"   json:"voucherSymbol"`
		VoucherUOA            *string   `db:"voucher_uoa"      json:"voucherUoa"`
		PriceInUnitOfAccount  *string   `db:"price_in_unit_of_account" json:"priceInUnitOfAccount"`
		VoucherDecimals       *int32    `db:"voucher_decimals"      json:"voucherDecimals"`
		VoucherImageURL       *string   `db:"voucher_image_url"     json:"voucherImageUrl"`
		VoucherLocationName   *string   `db:"voucher_location_name" json:"voucherLocationName"`
		VoucherGeoLat         *float64  `db:"voucher_geo_lat"       json:"voucherGeoLat"`
		VoucherGeoLng         *float64  `db:"voucher_geo_lng"       json:"voucherGeoLng"`
		SellerAddress         string    `db:"seller_address"   json:"sellerAddress"`
		CommodityName         string    `db:"commodity_name"   json:"commodityName"`
		Description           string    `db:"description"      json:"description"`
		Quantity              string    `db:"quantity"         json:"quantity"`
		Unit                  *string   `db:"unit"             json:"unit"`
		Frequency             *string   `db:"frequency"        json:"frequency"`
		Price                 *string   `db:"price"            json:"price"`
		Available             bool      `db:"available"        json:"available"`
		LocationName          *string   `db:"location_name"    json:"locationName"`
		GeoLat                *float64  `db:"geo_lat"          json:"geoLat"`
		GeoLng                *float64  `db:"geo_lng"          json:"geoLng"`
		ImageURL              *string   `db:"image_url"               json:"imageUrl"`
		EffectiveLocationName *string   `db:"effective_location_name" json:"effectiveLocationName"`
		EffectiveGeoLat       *float64  `db:"effective_geo_lat"       json:"effectiveGeoLat"`
		EffectiveGeoLng       *float64  `db:"effective_geo_lng"       json:"effectiveGeoLng"`
		DistanceMeters        *float64  `db:"distance_meters"         json:"distanceMeters"`
		Tags                  []string  `db:"tags"             json:"tags"`
		CreatedAt             time.Time `db:"created_at"       json:"createdAt"`
		UpdatedAt             time.Time `db:"updated_at"       json:"updatedAt"`
	}

	// OfferingWrite is the five product fields a caller owns.
	//
	// Absent on purpose: voucher_id (the immutable relationship, taken from the
	// route), seller_address (server-selected from the locked voucher owner),
	// source_id (import provenance), and quantity/frequency/location/geo (legacy
	// import columns this product does not manage). A caller cannot reach any of
	// them through this type, which is why the handler never has to remember not
	// to trust them.
	OfferingWrite struct {
		CommodityName string
		Description   string
		Unit          string
		// Plain decimal text all the way to NUMERIC. Never a float64: the column
		// holds values float64 silently rounds.
		Price    string
		ImageURL *string
	}

	// VoucherOfferingParent is the locked voucher a create authorizes against.
	VoucherOfferingParent struct {
		ID           uint64  `db:"id"`
		OwnerAddress *string `db:"owner_address"`
		Active       bool    `db:"active"`
	}

	// OfferingParent is the locked offering plus the voucher facts that decide
	// whether the caller may write it.
	OfferingParent struct {
		ID           uint64  `db:"id"`
		VoucherID    uint64  `db:"voucher_id"`
		Available    bool    `db:"available"`
		OwnerAddress *string `db:"owner_address"`
		Active       bool    `db:"active"`
	}
)

func (pg *Pg) ListOfferings(ctx context.Context, tx pgx.Tx, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListOfferings, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListOfferingsNext(ctx context.Context, tx pgx.Tx, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListOfferingsNext, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListOfferingsPrevious(ctx context.Context, tx pgx.Tx, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListOfferingsPrev, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListVoucherOfferings(ctx context.Context, tx pgx.Tx, voucherAddress string, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListVoucherOfferings, voucherAddress, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListVoucherOfferingsNext(ctx context.Context, tx pgx.Tx, voucherAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListVoucherOfferingsNext, voucherAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListVoucherOfferingsPrevious(ctx context.Context, tx pgx.Tx, voucherAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListVoucherOfferingsPrev, voucherAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

// ListPoolOfferings returns every offering the pool's allowed vouchers publish.
// A pool page needs them all, which the pool-joined queries return in one page
// rather than one request per voucher.
func (pg *Pg) ListPoolOfferings(ctx context.Context, tx pgx.Tx, poolAddress string, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListPoolOfferings, poolAddress, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListPoolOfferingsNext(ctx context.Context, tx pgx.Tx, poolAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListPoolOfferingsNext, poolAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListPoolOfferingsPrevious(ctx context.Context, tx pgx.Tx, poolAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListPoolOfferingsPrev, poolAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

// GetVoucherOffering returns one publicly visible offering under its voucher.
// Filters availability and voucher activity exactly like the public list, so an
// unlisted row is a 404 rather than a readable object with `available: false`.
func (pg *Pg) GetVoucherOffering(ctx context.Context, tx pgx.Tx, voucherAddress string, id uint64) (*Offering, error) {
	var offering Offering
	if err := pgxscan.Get(ctx, tx, &offering, pg.queries.GetVoucherOffering, voucherAddress, id); err != nil {
		return nil, err
	}
	return &offering, nil
}

// GetOfferingForMutation reads a row back after a write, ignoring availability
// and voucher activity.
//
// A soft-deleted row is still the caller's, and so is one on a voucher an
// operator retired between the edit and the read-back; the public statement would
// answer no rows for both and turn a successful write into a 404.
func (pg *Pg) GetOfferingForMutation(ctx context.Context, tx pgx.Tx, id uint64) (*Offering, error) {
	var offering Offering
	if err := pgxscan.Get(ctx, tx, &offering, pg.queries.GetOfferingForMutation, id); err != nil {
		return nil, err
	}
	return &offering, nil
}

// LockVoucherForOfferingCreate holds the voucher row through the caller's
// transaction and reports the owner and activity the authorization decision
// needs. Deciding from this row rather than from a client-supplied voucher is
// what makes the check unspoofable.
func (pg *Pg) LockVoucherForOfferingCreate(ctx context.Context, tx pgx.Tx, voucherAddress string) (*VoucherOfferingParent, error) {
	var parent VoucherOfferingParent
	if err := pgxscan.Get(ctx, tx, &parent, pg.queries.LockVoucherForOfferingCreate, voucherAddress); err != nil {
		return nil, err
	}
	return &parent, nil
}

// LockOfferingVoucherOwner holds the offering row and reports its parent
// voucher's owner and activity. Returns pgx.ErrNoRows when the id does not exist
// or belongs to a different voucher than the one named in the route — the two are
// the same 404 to a caller, and distinguishing them would disclose that an id
// exists elsewhere.
func (pg *Pg) LockOfferingVoucherOwner(ctx context.Context, tx pgx.Tx, id uint64, voucherAddress string) (*OfferingParent, error) {
	var parent OfferingParent
	if err := pgxscan.Get(ctx, tx, &parent, pg.queries.LockOfferingVoucherOwner, id, voucherAddress); err != nil {
		return nil, err
	}
	return &parent, nil
}

// CreateOffering inserts one offering against an already-locked voucher.
//
// sellerAddress is chosen by the handler from the locked voucher owner, or from
// the authenticated administrator when the voucher has no owner yet. It is never a
// request field.
func (pg *Pg) CreateOffering(
	ctx context.Context,
	tx pgx.Tx,
	voucherID uint64,
	sellerAddress string,
	offering OfferingWrite,
) (uint64, error) {
	var id uint64
	err := tx.QueryRow(
		ctx,
		pg.queries.CreateOffering,
		voucherID,
		sellerAddress,
		offering.CommodityName,
		offering.Description,
		offering.Unit,
		offering.Price,
		offering.ImageURL,
	).Scan(&id)
	return id, err
}

// UpdateOffering replaces the product fields of an already-locked offering.
func (pg *Pg) UpdateOffering(ctx context.Context, tx pgx.Tx, id uint64, offering OfferingWrite) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.UpdateOffering,
		id,
		offering.CommodityName,
		offering.Description,
		offering.Unit,
		offering.Price,
		offering.ImageURL,
	)
	return err
}

// DeleteOffering unlists an offering and returns its id. The row survives so the
// Sarafu source_id stays reconcilable and the audit trail is intact.
func (pg *Pg) DeleteOffering(ctx context.Context, tx pgx.Tx, id uint64) (uint64, error) {
	var deleted uint64
	err := tx.QueryRow(ctx, pg.queries.DeleteOffering, id).Scan(&deleted)
	return deleted, err
}
