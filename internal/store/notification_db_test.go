package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestNotificationStoreLifecycleAndRedelivery(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	owner, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: "0x11111111111111111111111111111111111111a1",
		Name:            "Notification owner",
		Email:           "notification-owner@clc.test",
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)
	other, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: "0x22222222222222222222222222222222222222b2",
		Name:            "Other notification user",
		Email:           "notification-other@clc.test",
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)

	device := NotificationDevice{
		DeviceID:             "notification-store-test-device",
		Endpoint:             "https://push.example.test/subscription/one",
		P256dh:               "p256dh",
		Auth:                 "auth",
		VAPIDKeyVersion:      "v1",
		Platform:             "test",
		ServiceWorkerVersion: "test-v1",
		PermissionState:      "granted",
		SubscriptionStatus:   "active",
	}
	require.NoError(t, pg.UpsertNotificationDevice(ctx, tx, device, owner.ID))
	wallets, err := pg.LinkNotificationWallet(ctx, tx, device.DeviceID, owner.ID, owner.EthereumAddress)
	require.NoError(t, err)
	require.Equal(t, []string{owner.EthereumAddress}, wallets)

	hijackedDevice := device
	hijackedDevice.Endpoint = "https://push.example.test/subscription/hijacked"
	require.ErrorIs(
		t,
		pg.UpsertNotificationDevice(ctx, tx, hijackedDevice, other.ID),
		pgx.ErrNoRows,
		"another user must not replace a registered device endpoint",
	)

	cursor, err := pg.NotificationInboxCursor(ctx, tx, device.DeviceID, owner.ID)
	require.NoError(t, err)
	require.Zero(t, cursor)
	_, err = pg.NotificationInboxCursor(ctx, tx, device.DeviceID, other.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	event := NotificationEvent{
		NotificationID:      uuid.NewString(),
		SourceKey:           "notification-store-test-source",
		Kind:                "voucher.received",
		Title:               "Voucher received",
		Body:                "A voucher arrived in your wallet.",
		TargetURL:           "/wallet",
		WalletAddressTarget: owner.EthereumAddress,
		Priority:            "high",
		CreatedAt:           time.Now().UTC(),
	}
	refs, err := pg.CreateNotificationDeliveries(ctx, tx, event)
	require.NoError(t, err)
	require.Len(t, refs, 1)

	redelivered := event
	redelivered.NotificationID = uuid.NewString()
	refs, err = pg.CreateNotificationDeliveries(ctx, tx, redelivered)
	require.NoError(t, err)
	require.Empty(t, refs, "one source event must not create duplicate deliveries")

	messages, deleted, nextCursor, err := pg.SyncNotificationInbox(ctx, tx, device.DeviceID, owner.ID, 0)
	require.NoError(t, err)
	require.Empty(t, deleted)
	require.Len(t, messages, 1)
	require.Equal(t, event.NotificationID, messages[0].NotificationID)
	require.Equal(t, refsCursor(messages), nextCursor)

	receivedAt := time.Now().UTC()
	accepted, err := pg.AcknowledgeNotificationDelivery(ctx, tx, NotificationDeliveryAck{
		DeviceID:       device.DeviceID,
		NotificationID: event.NotificationID,
		ReceivedAt:     &receivedAt,
	})
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = pg.AcknowledgeNotificationClick(ctx, tx, NotificationClickAck{
		DeviceID:       device.DeviceID,
		NotificationID: event.NotificationID,
		ClickedAt:      receivedAt,
	})
	require.NoError(t, err)
	require.True(t, accepted)

	status, err := pg.DisableNotificationDevice(ctx, tx, device.DeviceID, owner.ID, device.Endpoint)
	require.NoError(t, err)
	require.Equal(t, "disabled", status)
	_, err = pg.DisableNotificationDevice(ctx, tx, device.DeviceID, other.ID, device.Endpoint)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
}

func refsCursor(messages []NotificationInboxMessage) uint64 {
	if len(messages) == 0 {
		return 0
	}
	return messages[len(messages)-1].Revision
}
