package worker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/notification"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/riverqueue/river"
)

func TestMarshalNotificationPushPayloadMatchesPWAContract(t *testing.T) {
	createdAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	voucherID := "0x1234567890abcdef1234567890abcdef12345678"
	payload, err := marshalNotificationPushPayload(store.NotificationDelivery{
		NotificationID:      "9470d4a5-3be8-4db5-a997-cff45a426a9f",
		Kind:                "voucher.received",
		Title:               "Voucher received",
		Body:                "A voucher arrived in your wallet.",
		TargetURL:           "/wallet",
		WalletAddressTarget: "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Priority:            "high",
		CreatedAt:           createdAt,
		Actions:             json.RawMessage("[]"),
		VoucherID:           &voucherID,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"notificationId", "kind", "title", "body", "targetUrl",
		"walletAddressTarget", "priority", "createdAt", "voucherId",
	} {
		if _, exists := got[required]; !exists {
			t.Errorf("push payload is missing %q: %s", required, payload)
		}
	}
	if got["createdAt"] != "2026-08-26T12:00:00Z" {
		t.Fatalf("createdAt = %v", got["createdAt"])
	}
}

func TestNotificationDeliveryWorkErrorControlsRiverRetries(t *testing.T) {
	for name, test := range map[string]struct {
		result     notification.SendResult
		wantCancel bool
		wantError  bool
	}{
		"sent": {
			result: notification.SendResult{Status: "sent"},
		},
		"stale subscription is already resolved": {
			result: notification.SendResult{Status: "stale-subscription", Stale: true},
		},
		"retryable provider failure": {
			result:    notification.SendResult{Status: "push-service-error", Error: "provider down", Retryable: true},
			wantError: true,
		},
		"permanent provider rejection": {
			result:     notification.SendResult{Status: "push-service-error", Error: "bad request"},
			wantCancel: true,
			wantError:  true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := notificationDeliveryWorkError(test.result)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError = %t", err, test.wantError)
			}

			var cancelError *river.JobCancelError
			cancelled := errors.As(err, &cancelError)
			if cancelled != test.wantCancel {
				t.Fatalf("cancelled = %t, want %t (error: %v)", cancelled, test.wantCancel, err)
			}
		})
	}
}
