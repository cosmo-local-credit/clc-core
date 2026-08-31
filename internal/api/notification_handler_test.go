package api

import (
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/cosmo-local-credit/clc-core/internal/notification"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/golang-jwt/jwt/v5"
)

const (
	notificationTestWallet = "0x1234567890abcdef1234567890abcdef12345678"
)

func TestNotificationRoutesAreRegistered(t *testing.T) {
	a := newTestRouter(t)
	want := map[string]bool{
		"GET /api/notifications/vapid-public-key":       false,
		"POST /api/notifications/devices/register":      false,
		"POST /api/notifications/devices/link-wallet":   false,
		"POST /api/notifications/devices/unlink-wallet": false,
		"POST /api/notifications/devices/disable":       false,
		"POST /api/notifications/acks/delivery":         false,
		"POST /api/notifications/acks/click":            false,
		"POST /api/notifications/inbox/sync":            false,
	}

	for _, route := range a.router.Router().Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("notification route %s is not registered", route)
		}
	}
}

func TestNotificationRouteAuthenticationBoundary(t *testing.T) {
	a := newTestRouter(t)

	publicVAPID := httptest.NewRequest(http.MethodGet, "/api/notifications/vapid-public-key", nil)
	publicVAPIDResponse := httptest.NewRecorder()
	a.router.ServeHTTP(publicVAPIDResponse, publicVAPID)
	if publicVAPIDResponse.Code == http.StatusUnauthorized || publicVAPIDResponse.Code == http.StatusNotFound {
		t.Fatalf("VAPID discovery must be public and registered, got %d", publicVAPIDResponse.Code)
	}

	register := httptest.NewRequest(
		http.MethodPost,
		"/api/notifications/devices/register",
		strings.NewReader(`{}`),
	)
	register.Header.Set("Content-Type", "application/json")
	registerResponse := httptest.NewRecorder()
	a.router.ServeHTTP(registerResponse, register)
	if registerResponse.Code != http.StatusUnauthorized {
		t.Fatalf("device registration without a user session = %d, want 401", registerResponse.Code)
	}

	ack := httptest.NewRequest(
		http.MethodPost,
		"/api/notifications/acks/delivery",
		strings.NewReader(`{}`),
	)
	ack.Header.Set("Content-Type", "application/json")
	ackResponse := httptest.NewRecorder()
	a.router.ServeHTTP(ackResponse, ack)
	if ackResponse.Code == http.StatusUnauthorized || ackResponse.Code == http.StatusNotFound {
		t.Fatalf("delivery ack must be public and registered, got %d", ackResponse.Code)
	}
}

func TestNotificationVAPIDDiscoveryReturnsConfiguredKey(t *testing.T) {
	a, _, service := newConfiguredNotificationTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/notifications/vapid-public-key", nil)
	res := httptest.NewRecorder()

	a.router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["publicKey"] != service.PublicKey() || body["keyVersion"] != service.KeyVersion() {
		t.Fatalf("response = %#v", body)
	}
}

func TestNotificationRegistrationRejectsUnsafeClaimsBeforeDatabase(t *testing.T) {
	a, signingKey, _ := newConfiguredNotificationTestRouter(t)
	token := notificationSessionToken(t, signingKey, 7, notificationTestWallet)

	for name, test := range map[string]struct {
		currentWalletAddress string
		vapidKeyVersion      string
		wantDescription      string
		wantStatus           int
	}{
		"obsolete VAPID key": {
			currentWalletAddress: notificationTestWallet,
			vapidKeyVersion:      "obsolete",
			wantDescription:      "the push subscription uses an obsolete VAPID key",
			wantStatus:           http.StatusConflict,
		},
		"different wallet": {
			currentWalletAddress: "0x9999999999999999999999999999999999999999",
			vapidKeyVersion:      "v1",
			wantDescription:      "currentWalletAddress must match the authenticated wallet",
			wantStatus:           http.StatusBadRequest,
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(apiresp.RegisterNotificationDeviceRequest{
				CurrentWalletAddress: test.currentWalletAddress,
				DeviceID:             "notification-test-device",
				PermissionState:      "granted",
				PushSubscription: &apiresp.NotificationPushSubscription{
					Endpoint: "https://push.example.test/subscriptions/device",
					Keys: apiresp.NotificationSubscriptionKeys{
						Auth:   "auth",
						P256dh: "p256dh",
					},
				},
				VAPIDKeyVersion: test.vapidKeyVersion,
			})
			if err != nil {
				t.Fatalf("encode request: %v", err)
			}

			req := httptest.NewRequest(
				http.MethodPost,
				"/api/notifications/devices/register",
				strings.NewReader(string(body)),
			)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			a.router.ServeHTTP(res, req)

			if res.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", res.Code, test.wantStatus, res.Body.String())
			}
			var response apiresp.ErrResponse
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Description != test.wantDescription {
				t.Fatalf("description = %q, want %q", response.Description, test.wantDescription)
			}
		})
	}
}

func newConfiguredNotificationTestRouter(
	t *testing.T,
) (*API, ed25519.PrivateKey, *notification.Service) {
	return newConfiguredNotificationTestRouterWithStore(t, nil)
}

func newConfiguredNotificationTestRouterWithStore(
	t *testing.T,
	notificationStore store.Store,
) (*API, ed25519.PrivateKey, *notification.Service) {
	t.Helper()

	verificationKey, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate API signing key: %v", err)
	}
	vapidPrivateKey, vapidPublicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate VAPID keys: %v", err)
	}
	service, err := notification.New(notification.Config{
		PublicKey:  vapidPublicKey,
		PrivateKey: vapidPrivateKey,
		Subscriber: "mailto:ops@example.com",
		KeyVersion: "v1",
	})
	if err != nil {
		t.Fatalf("create notification service: %v", err)
	}

	return New(APIOpts{
		CORS:          []string{"https://app.example"},
		Logg:          slog.New(slog.DiscardHandler),
		Notifications: service,
		SigningKey:    signingKey,
		Store:         notificationStore,
		VerifyingKey:  verificationKey,
	}), signingKey, service
}

func notificationSessionToken(
	t *testing.T,
	signingKey ed25519.PrivateKey,
	userID uint64,
	walletAddress string,
) string {
	t.Helper()

	claims := UserJWTClaims{
		UserID:          userID,
		EthereumAddress: walletAddress,
		Role:            "USER",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(signingKey)
	if err != nil {
		t.Fatalf("sign notification session token: %v", err)
	}
	return token
}
