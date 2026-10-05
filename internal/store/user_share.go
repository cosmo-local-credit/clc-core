package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	SharePermissionEmail    = 1 << 0
	SharePermissionPhone    = 1 << 1
	SharePermissionLocation = 1 << 2
	SharePermissionAll      = SharePermissionEmail | SharePermissionPhone | SharePermissionLocation
)

type (
	UserShare struct {
		ID               uint64    `db:"id"                 json:"id"`
		TxHash           string    `db:"tx_hash"            json:"txHash"`
		OwnerUserID      uint64    `db:"owner_user_id"      json:"ownerUserId"`
		AuthorizedUserID uint64    `db:"authorized_user_id" json:"authorizedUserId"`
		SharePermission  int       `db:"share_permission"   json:"permission"`
		ExtraNote        string    `db:"extra_note"         json:"note"`
		CreatedAt        time.Time `db:"created_at"         json:"createdAt"`
		UpdatedAt        time.Time `db:"updated_at"         json:"updatedAt"`
	}

	UserShareDetails struct {
		ID               uint64    `db:"id"                 json:"id"`
		OwnerUserID      uint64    `db:"owner_user_id"      json:"ownerUserId"`
		AuthorizedUserID uint64    `db:"authorized_user_id" json:"authorizedUserId"`
		SharePermission  int       `db:"share_permission"   json:"permission"`
		ExtraNote        string    `db:"extra_note"         json:"note"`
		CreatedAt        time.Time `db:"created_at"         json:"createdAt"`
		UpdatedAt        time.Time `db:"updated_at"         json:"updatedAt"`
		TxHash           string    `db:"tx_hash"            json:"txHash"`
		OwnerName        string    `db:"owner_name"         json:"ownerName"`
		Email            string    `db:"email"              json:"email,omitempty"`
		Phone            string    `db:"phone"              json:"phone,omitempty"`
		LocationName     string    `db:"location_name"      json:"locationName,omitempty"`
		GeoLat           *float64  `db:"geo_lat"            json:"lat,omitempty"`
		GeoLng           *float64  `db:"geo_lng"            json:"lng,omitempty"`
	}
)

func (pg *Pg) InsertUserShare(ctx context.Context, tx pgx.Tx, share UserShare) (uint64, error) {
	var id uint64
	if err := tx.QueryRow(
		ctx,
		pg.queries.InsertUserShare,
		share.TxHash,
		share.OwnerUserID,
		share.AuthorizedUserID,
		share.SharePermission,
		share.ExtraNote,
	).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (pg *Pg) GetUserShareByTxHash(ctx context.Context, tx pgx.Tx, txHash string, callerUserID uint64) (UserShareDetails, error) {
	var share UserShareDetails
	rows, err := tx.Query(ctx, pg.queries.GetUserShareByTxHash, txHash, callerUserID)
	if err != nil {
		return share, err
	}
	if err := pgxscan.ScanOne(&share, rows); err != nil {
		return share, err
	}
	return share, nil
}

// ListUserShareTxHashes returns, lowercased, the subset of txHashes that carry
// a private attachment the caller may read. Callers pass a page of transfer
// hashes; anything absent from the result has no attachment for this user.
func (pg *Pg) ListUserShareTxHashes(ctx context.Context, tx pgx.Tx, txHashes []string, callerUserID uint64) ([]string, error) {
	if len(txHashes) == 0 {
		return nil, nil
	}
	var hashes []string
	if err := pgxscan.Select(ctx, tx, &hashes, pg.queries.ListUserShareTxHashes, txHashes, callerUserID); err != nil {
		return nil, err
	}
	return hashes, nil
}
