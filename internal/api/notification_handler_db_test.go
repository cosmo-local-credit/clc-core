package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/google/uuid"
)

// End-to-end registration coverage through the real route table, user JWT,
// handler, store, and PostgreSQL. Skipped unless CORE_TEST_DSN is set:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test -run TestNotificationRegistrationLifecycleThroughHTTP -v ./internal/api/
func TestNotificationRegistrationLifecycleThroughHTTP(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live notification API tests")
	}

	logg := slog.New(slog.DiscardHandler)
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 logg,
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	if err != nil {
		t.Fatalf("connect notification test store: %v", err)
	}

	testID := strings.ReplaceAll(uuid.NewString(), "-", "")
	walletAddress := "0x" + testID + "00000000"
	deviceID := "notification-api-" + testID

	ctx := context.Background()
	tx, err := pgStore.Pool().Begin(ctx)
	if err != nil {
		pgStore.Pool().Close()
		t.Fatalf("begin user fixture: %v", err)
	}
	user, err := pgStore.InsertUser(ctx, tx, store.User{
		EthereumAddress: walletAddress,
		Name:            "Notification API test user",
		Email:           testID + "@notifications.clc.test",
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		pgStore.Pool().Close()
		t.Fatalf("insert user fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		pgStore.Pool().Close()
		t.Fatalf("commit user fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pgStore.Pool().Exec(context.Background(),
			"DELETE FROM notification_devices WHERE device_id = $1", deviceID)
		_, _ = pgStore.Pool().Exec(context.Background(),
			"DELETE FROM users WHERE id = $1", user.ID)
		pgStore.Pool().Close()
	})

	a, signingKey, service := newConfiguredNotificationTestRouterWithStore(t, pgStore)
	token := notificationSessionToken(t, signingKey, user.ID, walletAddress)
	firstEndpoint := "https://push.example.test/subscriptions/" + testID + "/first"
	secondEndpoint := "https://push.example.test/subscriptions/" + testID + "/rotated"

	first := postNotificationRegistration(
		t, a, token, deviceID, walletAddress, firstEndpoint, service.KeyVersion(),
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first registration status = %d, want 200: %s", first.Code, first.Body.String())
	}
	var firstResponse notificationRegistrationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatalf("decode first registration: %v", err)
	}
	if firstResponse.DeviceID != deviceID || firstResponse.SubscriptionStatus != "active" {
		t.Fatalf("first registration response = %+v", firstResponse)
	}
	if len(firstResponse.CanonicalLinkedWalletAddresses) != 1 ||
		!strings.EqualFold(firstResponse.CanonicalLinkedWalletAddresses[0], walletAddress) {
		t.Fatalf("linked wallets = %#v", firstResponse.CanonicalLinkedWalletAddresses)
	}

	// A browser subscription can rotate while our IndexedDB device ID remains
	// stable. The authenticated owner must be able to replace the endpoint.
	second := postNotificationRegistration(
		t, a, token, deviceID, walletAddress, secondEndpoint, service.KeyVersion(),
	)
	if second.Code != http.StatusOK {
		t.Fatalf("rotated registration status = %d, want 200: %s", second.Code, second.Body.String())
	}

	var (
		storedEndpoint string
		storedStatus   string
		storedVersion  string
		deviceCount    int
	)
	if err := pgStore.Pool().QueryRow(ctx, `
		SELECT endpoint, subscription_status, vapid_key_version
		FROM notification_devices
		WHERE device_id = $1`, deviceID).Scan(
		&storedEndpoint,
		&storedStatus,
		&storedVersion,
	); err != nil {
		t.Fatalf("read registered device: %v", err)
	}
	if storedEndpoint != secondEndpoint || storedStatus != "active" || storedVersion != service.KeyVersion() {
		t.Fatalf("stored device = endpoint %q, status %q, version %q",
			storedEndpoint, storedStatus, storedVersion)
	}
	if err := pgStore.Pool().QueryRow(ctx,
		"SELECT COUNT(*) FROM notification_devices WHERE device_id = $1", deviceID,
	).Scan(&deviceCount); err != nil {
		t.Fatalf("count registered devices: %v", err)
	}
	if deviceCount != 1 {
		t.Fatalf("device rows = %d, want one upserted row", deviceCount)
	}

	var linkedWallet string
	if err := pgStore.Pool().QueryRow(ctx, `
		SELECT wallet_address
		FROM notification_device_wallets
		WHERE device_id = $1 AND user_id = $2`, deviceID, user.ID,
	).Scan(&linkedWallet); err != nil {
		t.Fatalf("read wallet link: %v", err)
	}
	if !strings.EqualFold(linkedWallet, walletAddress) {
		t.Fatalf("linked wallet = %q, want %q", linkedWallet, walletAddress)
	}
}

func postNotificationRegistration(
	t *testing.T,
	a *API,
	token string,
	deviceID string,
	walletAddress string,
	endpoint string,
	vapidKeyVersion string,
) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(apiresp.RegisterNotificationDeviceRequest{
		AppVersion:           "web-test",
		CurrentWalletAddress: walletAddress,
		DeviceID:             deviceID,
		PermissionState:      "granted",
		Platform:             "test",
		PushSubscription: &apiresp.NotificationPushSubscription{
			Endpoint: endpoint,
			Keys: apiresp.NotificationSubscriptionKeys{
				Auth:   "auth-test",
				P256dh: "p256dh-test",
			},
		},
		ServiceWorkerVersion: "sw-test",
		VAPIDKeyVersion:      vapidKeyVersion,
	})
	if err != nil {
		t.Fatalf("encode registration: %v", err)
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
	return res
}
