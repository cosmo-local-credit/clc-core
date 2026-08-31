package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	maxNotificationInboxPageSize = 500
)

type (
	NotificationDevice struct {
		DeviceID             string
		Endpoint             string
		ExpirationTimeMS     *int64
		P256dh               string
		Auth                 string
		VAPIDKeyVersion      string
		Platform             string
		AppVersion           string
		ServiceWorkerVersion string
		PermissionState      string
		SubscriptionStatus   string
	}

	NotificationEvent struct {
		NotificationID      string
		SourceKey           string
		Kind                string
		Title               string
		Body                string
		TargetURL           string
		WalletAddressTarget string
		Priority            string
		CreatedAt           time.Time
		ExpiresAt           *time.Time
		Actions             json.RawMessage
		VoucherID           *string
		SwapID              *string
		OffRampReference    *string
	}

	NotificationDeliveryRef struct {
		RecordID string `db:"record_id"`
		Revision uint64 `db:"revision"`
		DeviceID string `db:"device_id"`
	}

	NotificationDelivery struct {
		RecordID            string          `db:"record_id"`
		Revision            uint64          `db:"revision"`
		NotificationID      string          `db:"notification_id"`
		DeviceID            string          `db:"device_id"`
		Endpoint            string          `db:"endpoint"`
		Auth                string          `db:"auth_secret"`
		P256dh              string          `db:"p256dh"`
		Kind                string          `db:"kind"`
		Title               string          `db:"title"`
		Body                string          `db:"body"`
		TargetURL           string          `db:"target_url"`
		WalletAddressTarget string          `db:"wallet_address_target"`
		Priority            string          `db:"priority"`
		CreatedAt           time.Time       `db:"created_at"`
		ExpiresAt           *time.Time      `db:"expires_at"`
		Actions             json.RawMessage `db:"actions"`
		VoucherID           *string         `db:"voucher_id"`
		SwapID              *string         `db:"swap_id"`
		OffRampReference    *string         `db:"offramp_reference"`
	}

	NotificationInboxMessage struct {
		RecordID            string          `db:"record_id"             json:"recordId"`
		Revision            uint64          `db:"revision"              json:"revision"`
		DeviceID            string          `db:"device_id"             json:"deviceId"`
		PushStatus          string          `db:"push_status"           json:"pushStatus"`
		PushStatusCode      *int            `db:"push_status_code"      json:"pushStatusCode,omitempty"`
		PushError           *string         `db:"push_error"            json:"pushError,omitempty"`
		SentAt              time.Time       `db:"sent_at"              json:"sentAt"`
		ReceivedAt          *time.Time      `db:"received_at"          json:"receivedAt,omitempty"`
		ShownAt             *time.Time      `db:"shown_at"             json:"shownAt,omitempty"`
		ClickedAt           *time.Time      `db:"clicked_at"           json:"clickedAt,omitempty"`
		ActionID            *string         `db:"action_id"            json:"actionId,omitempty"`
		NotificationID      string          `db:"notification_id"      json:"notificationId"`
		Kind                string          `db:"kind"                 json:"kind"`
		Title               string          `db:"title"                json:"title"`
		Body                string          `db:"body"                 json:"body"`
		CreatedAt           time.Time       `db:"created_at"           json:"createdAt"`
		ExpiresAt           *time.Time      `db:"expires_at"           json:"expiresAt,omitempty"`
		Priority            string          `db:"priority"             json:"priority"`
		TargetURL           string          `db:"target_url"           json:"targetUrl"`
		WalletAddressTarget string          `db:"wallet_address_target" json:"walletAddressTarget,omitempty"`
		VoucherID           *string         `db:"voucher_id"           json:"voucherId,omitempty"`
		SwapID              *string         `db:"swap_id"              json:"swapId,omitempty"`
		OffRampReference    *string         `db:"offramp_reference"    json:"offRampReference,omitempty"`
		Actions             json.RawMessage `db:"actions"              json:"actions,omitempty"`
		DeletedAt           *time.Time      `db:"deleted_at"           json:"-"`
	}

	NotificationDeliveryAck struct {
		DeviceID             string
		NotificationID       string
		ReceivedAt           *time.Time
		ShownAt              *time.Time
		Platform             string
		ServiceWorkerVersion string
	}

	NotificationClickAck struct {
		DeviceID       string
		NotificationID string
		ActionID       string
		TargetURL      string
		ClickedAt      time.Time
	}

	NotificationDeliveryResult struct {
		RecordID   string
		Status     string
		StatusCode *int
		Error      string
		Stale      bool
	}
)

func (pg *Pg) UpsertNotificationDevice(
	ctx context.Context,
	tx pgx.Tx,
	device NotificationDevice,
	callerUserID uint64,
) error {
	return tx.QueryRow(
		ctx,
		pg.queries.UpsertNotificationDevice,
		device.DeviceID,
		device.Endpoint,
		device.ExpirationTimeMS,
		device.P256dh,
		device.Auth,
		device.VAPIDKeyVersion,
		nullableString(device.Platform),
		nullableString(device.AppVersion),
		nullableString(device.ServiceWorkerVersion),
		device.PermissionState,
		device.SubscriptionStatus,
		callerUserID,
	).Scan(&device.DeviceID)
}

func (pg *Pg) LinkNotificationWallet(
	ctx context.Context,
	tx pgx.Tx,
	deviceID string,
	userID uint64,
	walletAddress string,
) ([]string, error) {
	tag, err := tx.Exec(ctx, pg.queries.LinkNotificationWallet, deviceID, userID, walletAddress)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return pg.listNotificationWallets(ctx, tx, deviceID, userID)
}

func (pg *Pg) UnlinkNotificationWallet(
	ctx context.Context,
	tx pgx.Tx,
	deviceID string,
	userID uint64,
) ([]string, error) {
	if _, err := tx.Exec(ctx, pg.queries.UnlinkNotificationWallet, deviceID, userID); err != nil {
		return nil, err
	}
	return pg.listNotificationWallets(ctx, tx, deviceID, userID)
}

func (pg *Pg) listNotificationWallets(
	ctx context.Context,
	tx pgx.Tx,
	deviceID string,
	userID uint64,
) ([]string, error) {
	wallets := make([]string, 0)
	if err := pgxscan.Select(ctx, tx, &wallets, pg.queries.ListNotificationWallets, deviceID, userID); err != nil {
		return nil, err
	}
	return wallets, nil
}

func (pg *Pg) DisableNotificationDevice(
	ctx context.Context,
	tx pgx.Tx,
	deviceID string,
	userID uint64,
	endpoint string,
) (string, error) {
	var status string
	err := tx.QueryRow(ctx, pg.queries.DisableNotificationDevice, deviceID, userID, endpoint).Scan(&status)
	return status, err
}

func (pg *Pg) NotificationInboxCursor(
	ctx context.Context,
	tx pgx.Tx,
	deviceID string,
	userID uint64,
) (uint64, error) {
	var cursor uint64
	err := tx.QueryRow(ctx, pg.queries.NotificationInboxCursor, deviceID, userID).Scan(&cursor)
	return cursor, err
}

func (pg *Pg) SyncNotificationInbox(
	ctx context.Context,
	tx pgx.Tx,
	deviceID string,
	userID uint64,
	cursor uint64,
) ([]NotificationInboxMessage, []string, uint64, error) {
	rows := make([]NotificationInboxMessage, 0)
	if err := pgxscan.Select(
		ctx,
		tx,
		&rows,
		pg.queries.SyncNotificationInbox,
		deviceID,
		userID,
		cursor,
		maxNotificationInboxPageSize,
	); err != nil {
		return nil, nil, cursor, err
	}

	messages := make([]NotificationInboxMessage, 0, len(rows))
	deletedIDs := make([]string, 0)
	nextCursor := cursor
	for _, row := range rows {
		if row.Revision > nextCursor {
			nextCursor = row.Revision
		}
		if row.DeletedAt != nil {
			deletedIDs = append(deletedIDs, row.NotificationID)
			continue
		}
		messages = append(messages, row)
	}
	return messages, deletedIDs, nextCursor, nil
}

func (pg *Pg) AcknowledgeNotificationDelivery(
	ctx context.Context,
	tx pgx.Tx,
	ack NotificationDeliveryAck,
) (bool, error) {
	var accepted bool
	err := tx.QueryRow(
		ctx,
		pg.queries.AcknowledgeNotificationDelivery,
		ack.DeviceID,
		ack.NotificationID,
		ack.ReceivedAt,
		ack.ShownAt,
		nullableString(ack.Platform),
		nullableString(ack.ServiceWorkerVersion),
	).Scan(&accepted)
	return accepted, err
}

func (pg *Pg) AcknowledgeNotificationClick(
	ctx context.Context,
	tx pgx.Tx,
	ack NotificationClickAck,
) (bool, error) {
	var accepted bool
	err := tx.QueryRow(
		ctx,
		pg.queries.AcknowledgeNotificationClick,
		ack.DeviceID,
		ack.NotificationID,
		nullableString(ack.ActionID),
		nullableString(ack.TargetURL),
		ack.ClickedAt,
	).Scan(&accepted)
	return accepted, err
}

func (pg *Pg) CreateNotificationDeliveries(
	ctx context.Context,
	tx pgx.Tx,
	event NotificationEvent,
) ([]NotificationDeliveryRef, error) {
	if len(event.Actions) == 0 {
		event.Actions = json.RawMessage("[]")
	}

	var notificationID string
	var inserted bool
	err := tx.QueryRow(
		ctx,
		pg.queries.UpsertNotificationEvent,
		event.NotificationID,
		event.SourceKey,
		event.Kind,
		event.Title,
		event.Body,
		event.TargetURL,
		event.WalletAddressTarget,
		event.Priority,
		event.CreatedAt,
		event.ExpiresAt,
		event.Actions,
		event.VoucherID,
		event.SwapID,
		event.OffRampReference,
	).Scan(&notificationID, &inserted)
	if err != nil {
		return nil, err
	}
	if !inserted {
		return []NotificationDeliveryRef{}, nil
	}

	refs := make([]NotificationDeliveryRef, 0)
	if err := pgxscan.Select(
		ctx,
		tx,
		&refs,
		pg.queries.InsertNotificationDeliveries,
		notificationID,
		event.WalletAddressTarget,
	); err != nil {
		return nil, err
	}
	return refs, nil
}

func (pg *Pg) GetNotificationDelivery(
	ctx context.Context,
	tx pgx.Tx,
	recordID string,
) (NotificationDelivery, error) {
	var delivery NotificationDelivery
	err := pgxscan.Get(ctx, tx, &delivery, pg.queries.GetNotificationDelivery, recordID)
	return delivery, err
}

func (pg *Pg) RecordNotificationDeliveryResult(
	ctx context.Context,
	tx pgx.Tx,
	result NotificationDeliveryResult,
) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.RecordNotificationDeliveryResult,
		result.RecordID,
		result.Status,
		result.StatusCode,
		nullableString(result.Error),
		result.Stale,
	)
	return err
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
