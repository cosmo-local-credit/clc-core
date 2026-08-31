package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	notificationRegistrationResponse struct {
		CanonicalLinkedWalletAddresses []string `json:"canonicalLinkedWalletAddresses"`
		DeviceID                       string   `json:"deviceId"`
		InboxSyncCursor                string   `json:"inboxSyncCursor"`
		NotificationSettings           struct {
			Enabled bool `json:"enabled"`
		} `json:"notificationSettings"`
		Status             string `json:"status"`
		SubscriptionStatus string `json:"subscriptionStatus"`
	}
)

func (a *API) notificationVAPIDPublicKeyHandler(c *echo.Context) error {
	if a.notifications == nil {
		return notificationUnavailable(c)
	}
	return c.JSON(http.StatusOK, map[string]string{
		"publicKey":  a.notifications.PublicKey(),
		"keyVersion": a.notifications.KeyVersion(),
	})
}

func (a *API) notificationDeviceRegisterHandler(c *echo.Context) error {
	if a.notifications == nil {
		return notificationUnavailable(c)
	}

	var req apiresp.RegisterNotificationDeviceRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	userID, callerAddress, _ := getUserClaims(c)
	if err := validateNotificationWalletClaims(
		callerAddress,
		req.CurrentWalletAddress,
		req.LinkedWalletAddresses,
	); err != nil {
		return notificationValidationError(c, err.Error())
	}

	keyVersion := strings.TrimSpace(req.VAPIDKeyVersion)
	if keyVersion == "" {
		keyVersion = a.notifications.KeyVersion()
	}
	if keyVersion != a.notifications.KeyVersion() {
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeConflict,
			Description: "the push subscription uses an obsolete VAPID key",
		})
	}

	permissionState := strings.TrimSpace(req.PermissionState)
	if permissionState == "" {
		permissionState = "granted"
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = a.store.UpsertNotificationDevice(ctx, tx, store.NotificationDevice{
		DeviceID:             req.DeviceID,
		Endpoint:             req.PushSubscription.Endpoint,
		ExpirationTimeMS:     req.PushSubscription.ExpirationTime,
		P256dh:               req.PushSubscription.Keys.P256dh,
		Auth:                 req.PushSubscription.Keys.Auth,
		VAPIDKeyVersion:      keyVersion,
		Platform:             req.Platform,
		AppVersion:           req.AppVersion,
		ServiceWorkerVersion: req.ServiceWorkerVersion,
		PermissionState:      permissionState,
		SubscriptionStatus:   "active",
	}, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusConflict, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeConflict,
				Description: "device registration belongs to another user session",
			})
		}
		return handlePostgresError(c, err)
	}

	wallets, err := a.store.LinkNotificationWallet(ctx, tx, req.DeviceID, userID, callerAddress)
	if err != nil {
		return handlePostgresError(c, err)
	}
	cursor, err := a.store.NotificationInboxCursor(ctx, tx, req.DeviceID, userID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	response := notificationRegistrationResponse{
		CanonicalLinkedWalletAddresses: wallets,
		DeviceID:                       req.DeviceID,
		InboxSyncCursor:                strconv.FormatUint(cursor, 10),
		Status:                         "registered",
		SubscriptionStatus:             "active",
	}
	response.NotificationSettings.Enabled = permissionState == "granted"
	return c.JSON(http.StatusOK, response)
}

func (a *API) notificationLinkWalletHandler(c *echo.Context) error {
	var req apiresp.LinkNotificationWalletRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	userID, callerAddress, _ := getUserClaims(c)
	if !sameNotificationWallet(req.WalletAddress, callerAddress) {
		return notificationValidationError(c, "walletAddress must match the authenticated wallet")
	}

	wallets, err := a.updateNotificationWalletLink(c, req.DeviceID, userID, callerAddress, true)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"linkedWalletAddresses": wallets})
}

func (a *API) notificationUnlinkWalletHandler(c *echo.Context) error {
	var req apiresp.LinkNotificationWalletRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	userID, callerAddress, _ := getUserClaims(c)
	if !sameNotificationWallet(req.WalletAddress, callerAddress) {
		return notificationValidationError(c, "walletAddress must match the authenticated wallet")
	}

	wallets, err := a.updateNotificationWalletLink(c, req.DeviceID, userID, callerAddress, false)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"linkedWalletAddresses": wallets})
}

func (a *API) updateNotificationWalletLink(
	c *echo.Context,
	deviceID string,
	userID uint64,
	walletAddress string,
	link bool,
) ([]string, error) {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var wallets []string
	if link {
		wallets, err = a.store.LinkNotificationWallet(ctx, tx, deviceID, userID, walletAddress)
	} else {
		wallets, err = a.store.UnlinkNotificationWallet(ctx, tx, deviceID, userID)
	}
	if err != nil {
		return nil, handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, handlePostgresError(c, err)
	}
	return wallets, nil
}

func (a *API) notificationDisableDeviceHandler(c *echo.Context) error {
	var req apiresp.DisableNotificationDeviceRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	status, err := a.store.DisableNotificationDevice(
		ctx,
		tx,
		req.DeviceID,
		userID,
		req.SubscriptionEndpoint,
	)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{
		"deviceId":           req.DeviceID,
		"subscriptionStatus": status,
	})
}

func (a *API) notificationInboxSyncHandler(c *echo.Context) error {
	var req apiresp.SyncNotificationInboxRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	cursor := uint64(0)
	var err error
	if req.LastSyncCursor != "" {
		cursor, err = strconv.ParseUint(req.LastSyncCursor, 10, 64)
		if err != nil {
			return notificationValidationError(c, "lastSyncCursor must be an unsigned integer")
		}
	}
	userID, callerAddress, _ := getUserClaims(c)
	if err := validateNotificationWalletClaims(callerAddress, "", req.LinkedWalletAddresses); err != nil {
		return notificationValidationError(c, err.Error())
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	messages, deletedIDs, nextCursor, err := a.store.SyncNotificationInbox(
		ctx,
		tx,
		req.DeviceID,
		userID,
		cursor,
	)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deletedIds":     deletedIDs,
		"messages":       messages,
		"nextSyncCursor": strconv.FormatUint(nextCursor, 10),
	})
}

func (a *API) notificationDeliveryAckHandler(c *echo.Context) error {
	var req apiresp.SendNotificationDeliveryAckRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	receivedAt, err := optionalNotificationTime(req.ReceivedAt)
	if err != nil {
		return notificationValidationError(c, "receivedAt must be an RFC3339 timestamp")
	}
	shownAt, err := optionalNotificationTime(req.ShownAt)
	if err != nil {
		return notificationValidationError(c, "shownAt must be an RFC3339 timestamp")
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	accepted, err := a.store.AcknowledgeNotificationDelivery(ctx, tx, store.NotificationDeliveryAck{
		DeviceID:             req.DeviceID,
		NotificationID:       req.NotificationID,
		ReceivedAt:           receivedAt,
		ShownAt:              shownAt,
		Platform:             req.Platform,
		ServiceWorkerVersion: req.ServiceWorkerVersion,
	})
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"accepted": accepted})
}

func (a *API) notificationClickAckHandler(c *echo.Context) error {
	var req apiresp.SendNotificationClickAckRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	clickedAt := time.Now().UTC()
	if req.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339, req.Timestamp)
		if err != nil {
			return notificationValidationError(c, "timestamp must be an RFC3339 timestamp")
		}
		clickedAt = parsed
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	accepted, err := a.store.AcknowledgeNotificationClick(ctx, tx, store.NotificationClickAck{
		DeviceID:       req.DeviceID,
		NotificationID: req.NotificationID,
		ActionID:       req.ActionID,
		TargetURL:      req.TargetURL,
		ClickedAt:      clickedAt,
	})
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"accepted": accepted})
}

func validateNotificationWalletClaims(callerAddress, currentAddress string, linked []string) error {
	if currentAddress != "" && !sameNotificationWallet(currentAddress, callerAddress) {
		return errors.New("currentWalletAddress must match the authenticated wallet")
	}
	for _, address := range linked {
		if !sameNotificationWallet(address, callerAddress) {
			return errors.New("linkedWalletAddresses may contain only the authenticated wallet")
		}
	}
	return nil
}

func sameNotificationWallet(candidate, callerAddress string) bool {
	return common.IsHexAddress(candidate) &&
		common.IsHexAddress(callerAddress) &&
		strings.EqualFold(common.HexToAddress(candidate).Hex(), common.HexToAddress(callerAddress).Hex())
}

func optionalNotificationTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func notificationUnavailable(c *echo.Context) error {
	return c.JSON(http.StatusServiceUnavailable, apiresp.ErrResponse{
		Ok:          false,
		Description: "notifications are not configured on this deployment",
	})
}

func notificationValidationError(c *echo.Context, description string) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeValidationFailed,
		Description: description,
	})
}
