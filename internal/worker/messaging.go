package worker

import (
	"context"
	"errors"
	"time"

	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const (
	// messagingEventMaxAttempts retries a failed publish with River's default
	// attempt^4-second backoff, about four hours in all, before the job is
	// discarded. Unlike the relay jobs a retry is always safe: every attempt
	// carries the same eventId.
	messagingEventMaxAttempts = 10
)

type (
	// MessagingEventArgs is the outbox row for one messaging event. It is
	// inserted in the transaction that made the state change, so a rollback
	// drops both and a commit guarantees the publish. The JSON is the event
	// body itself.
	MessagingEventArgs struct {
		event.MessagingEvent
	}

	messagingPublisher interface {
		PublishMessaging(context.Context, event.MessagingEvent) error
	}

	MessagingEventWorker struct {
		river.WorkerDefaults[MessagingEventArgs]

		pub messagingPublisher
	}
)

func (MessagingEventArgs) Kind() string { return "MESSAGING_EVENT" }

// EnqueueMessagingEvent inserts the event's outbox job in tx. The eventId must
// already be set: retries reuse it.
func EnqueueMessagingEvent(ctx context.Context, queue *river.Client[pgx.Tx], tx pgx.Tx, ev event.MessagingEvent) error {
	if queue == nil {
		return errors.New("messaging outbox: no queue client")
	}
	if ev.EventID == "" {
		return errors.New("messaging outbox: event has no eventId")
	}
	if ev.OccurredAt == "" {
		ev.OccurredAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := queue.InsertTx(ctx, tx, MessagingEventArgs{MessagingEvent: ev},
		&river.InsertOpts{MaxAttempts: messagingEventMaxAttempts})
	return err
}

func (w *MessagingEventWorker) Work(ctx context.Context, job *river.Job[MessagingEventArgs]) error {
	if w.pub == nil {
		return errors.New("messaging publisher is not configured")
	}
	return w.pub.PublishMessaging(ctx, job.Args.MessagingEvent)
}
