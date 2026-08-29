package api

// NotificationPushSubscription is the JSON shape returned by the browser's
// PushSubscription.toJSON(). The endpoint and keys are opaque provider values;
// clc-core persists them and uses them only for encrypted Web Push delivery.
type NotificationPushSubscription struct {
	Endpoint       string                       `json:"endpoint" validate:"required,startswith=https://,max=2048"`
	ExpirationTime *int64                       `json:"expirationTime,omitempty" validate:"omitempty,gte=0"`
	Keys           NotificationSubscriptionKeys `json:"keys" validate:"required"`
}

type NotificationSubscriptionKeys struct {
	Auth   string `json:"auth" validate:"required,max=4096"`
	P256dh string `json:"p256dh" validate:"required,max=4096"`
}

type RegisterNotificationDeviceRequest struct {
	AppVersion            string                        `json:"appVersion,omitempty" validate:"omitempty,max=128"`
	CurrentWalletAddress  string                        `json:"currentWalletAddress,omitempty"`
	DeviceID              string                        `json:"deviceId" validate:"required,max=128"`
	LinkedWalletAddresses []string                      `json:"linkedWalletAddresses,omitempty" validate:"max=16"`
	PermissionState       string                        `json:"permissionState,omitempty" validate:"omitempty,oneof=default denied granted unavailable"`
	Platform              string                        `json:"platform,omitempty" validate:"omitempty,max=64"`
	PushSubscription      *NotificationPushSubscription `json:"pushSubscription" validate:"required"`
	ServiceWorkerVersion  string                        `json:"serviceWorkerVersion,omitempty" validate:"omitempty,max=128"`
	VAPIDKeyVersion       string                        `json:"vapidKeyVersion,omitempty" validate:"omitempty,max=128"`
}

type LinkNotificationWalletRequest struct {
	DeviceID      string `json:"deviceId" validate:"required,max=128"`
	WalletAddress string `json:"walletAddress" validate:"required"`
}

type DisableNotificationDeviceRequest struct {
	DeviceID             string `json:"deviceId" validate:"required,max=128"`
	SubscriptionEndpoint string `json:"subscriptionEndpoint,omitempty" validate:"omitempty,startswith=https://,max=2048"`
}

type SendNotificationDeliveryAckRequest struct {
	DeviceID             string `json:"deviceId" validate:"required,max=128"`
	NotificationID       string `json:"notificationId" validate:"required,uuid"`
	Platform             string `json:"platform,omitempty" validate:"omitempty,max=64"`
	ReceivedAt           string `json:"receivedAt,omitempty"`
	ServiceWorkerVersion string `json:"serviceWorkerVersion,omitempty" validate:"omitempty,max=128"`
	ShownAt              string `json:"shownAt,omitempty"`
	WalletAddressTarget  string `json:"walletAddressTarget,omitempty"`
}

type SendNotificationClickAckRequest struct {
	ActionID       string `json:"actionId,omitempty" validate:"omitempty,max=64"`
	DeviceID       string `json:"deviceId" validate:"required,max=128"`
	NotificationID string `json:"notificationId" validate:"required,uuid"`
	TargetURL      string `json:"targetUrl,omitempty" validate:"omitempty,max=2048"`
	Timestamp      string `json:"timestamp,omitempty"`
}

type SyncNotificationInboxRequest struct {
	DeviceID              string   `json:"deviceId" validate:"required,max=128"`
	LastSyncCursor        string   `json:"lastSyncCursor,omitempty" validate:"omitempty,numeric"`
	LinkedWalletAddresses []string `json:"linkedWalletAddresses,omitempty" validate:"max=16"`
}
