package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/notification"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type (
	NotificationDeliveryArgs struct {
		RecordID string `json:"recordId"`
	}

	NotificationDeliveryWorker struct {
		river.WorkerDefaults[NotificationDeliveryArgs]

		wc *WorkerContainer
	}

	notificationPushPayload struct {
		Actions             json.RawMessage `json:"actions,omitempty"`
		Body                string          `json:"body"`
		CreatedAt           string          `json:"createdAt"`
		ExpiresAt           *string         `json:"expiresAt,omitempty"`
		Kind                string          `json:"kind"`
		NotificationID      string          `json:"notificationId"`
		OffRampReference    *string         `json:"offRampReference,omitempty"`
		Priority            string          `json:"priority"`
		SwapID              *string         `json:"swapId,omitempty"`
		TargetURL           string          `json:"targetUrl"`
		Title               string          `json:"title"`
		VoucherID           *string         `json:"voucherId,omitempty"`
		WalletAddressTarget string          `json:"walletAddressTarget,omitempty"`
	}
)

func (NotificationDeliveryArgs) Kind() string { return "NOTIFICATION_DELIVERY" }

func (w *NotificationDeliveryWorker) Work(
	ctx context.Context,
	job *river.Job[NotificationDeliveryArgs],
) error {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	delivery, err := w.wc.store.GetNotificationDelivery(ctx, tx, job.Args.RecordID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return river.JobCancel(err)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if w.wc.notificationSender == nil {
		if err := w.recordNotificationResult(ctx, delivery.RecordID, notification.SendResult{
			Status: "send-error",
			Error:  "notifications are disabled",
		}); err != nil {
			return err
		}
		return river.JobCancel(errors.New("notifications are disabled"))
	}

	if delivery.ExpiresAt != nil && !delivery.ExpiresAt.After(time.Now()) {
		if err := w.recordNotificationResult(ctx, delivery.RecordID, notification.SendResult{
			Status: "send-error",
			Error:  "notification expired before delivery",
		}); err != nil {
			return err
		}
		return nil
	}

	payload, err := marshalNotificationPushPayload(delivery)
	if err != nil {
		return river.JobCancel(err)
	}
	result := w.wc.notificationSender.Send(ctx, notification.Message{
		Endpoint: delivery.Endpoint,
		Auth:     delivery.Auth,
		P256dh:   delivery.P256dh,
		Payload:  payload,
		Priority: delivery.Priority,
		// Web Push limits Topic to 32 URL-safe characters. A UUID without
		// hyphens is exactly 32 and lets providers collapse pending retries.
		Topic: strings.ReplaceAll(delivery.NotificationID, "-", ""),
	})
	if err := w.recordNotificationResult(ctx, delivery.RecordID, result); err != nil {
		return err
	}

	return notificationDeliveryWorkError(result)
}

// notificationDeliveryWorkError translates a provider outcome into River's
// retry contract. A normal error consumes another attempt, JobCancel stops
// permanently rejected deliveries, and sent/stale outcomes are complete.
func notificationDeliveryWorkError(result notification.SendResult) error {
	if result.Retryable {
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return errors.New("push provider requested a retry")
	}
	if result.Status != "sent" && !result.Stale {
		if result.Error != "" {
			return river.JobCancel(errors.New(result.Error))
		}
		return river.JobCancel(errors.New("push provider permanently rejected delivery"))
	}
	return nil
}

func (w *NotificationDeliveryWorker) recordNotificationResult(
	ctx context.Context,
	recordID string,
	result notification.SendResult,
) error {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var statusCode *int
	if result.StatusCode != 0 {
		statusCode = &result.StatusCode
	}
	if err := w.wc.store.RecordNotificationDeliveryResult(ctx, tx, store.NotificationDeliveryResult{
		RecordID:   recordID,
		Status:     result.Status,
		StatusCode: statusCode,
		Error:      result.Error,
		Stale:      result.Stale,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func marshalNotificationPushPayload(delivery store.NotificationDelivery) ([]byte, error) {
	var expiresAt *string
	if delivery.ExpiresAt != nil {
		formatted := delivery.ExpiresAt.UTC().Format(time.RFC3339Nano)
		expiresAt = &formatted
	}
	payload := notificationPushPayload{
		Actions:             delivery.Actions,
		Body:                delivery.Body,
		CreatedAt:           delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:           expiresAt,
		Kind:                delivery.Kind,
		NotificationID:      delivery.NotificationID,
		OffRampReference:    delivery.OffRampReference,
		Priority:            delivery.Priority,
		SwapID:              delivery.SwapID,
		TargetURL:           delivery.TargetURL,
		Title:               delivery.Title,
		VoucherID:           delivery.VoucherID,
		WalletAddressTarget: delivery.WalletAddressTarget,
	}
	return json.Marshal(payload)
}
