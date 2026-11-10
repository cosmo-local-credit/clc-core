package pub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type (
	PubOpts struct {
		PersistDuration time.Duration
		JS              jetstream.JetStream
		NatsConn        *nats.Conn
	}

	Pub struct {
		js       jetstream.JetStream
		natsConn *nats.Conn
	}
)

const (
	pushStream = "CUSTODIAL"

	messagingDuplicateWindow = 10 * time.Minute
)

var (
	pushStreamSubjects = []string{
		"CUSTODIAL.*",
	}
)

func NewPub(o PubOpts) *Pub {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _ = o.JS.CreateStream(ctx, jetstream.StreamConfig{
		Name:       pushStream,
		Subjects:   pushStreamSubjects,
		MaxAge:     o.PersistDuration,
		Storage:    jetstream.FileStorage,
		Duplicates: time.Minute,
	})

	// Messaging events for clc-app-notifications. The duplicate window covers
	// the outbox job's early retries; anything later is deduplicated by the
	// consumer on eventId. CreateOrUpdate so a changed config reaches an
	// existing stream; a failure here surfaces as a failed publish, which the
	// job retries.
	_, _ = o.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       event.MessagingStream,
		Subjects:   []string{event.MessagingSubjectPrefix + ">"},
		MaxAge:     o.PersistDuration,
		Storage:    jetstream.FileStorage,
		Duplicates: messagingDuplicateWindow,
	})

	return &Pub{
		js:       o.JS,
		natsConn: o.NatsConn,
	}
}

func (p *Pub) Close() {
	if p.natsConn != nil {
		p.natsConn.Close()
	}
}

func (p *Pub) Send(ctx context.Context, payload event.Event) error {
	data, err := payload.Serialize()
	if err != nil {
		return err
	}

	_, err = p.js.Publish(
		ctx,
		fmt.Sprintf("%s.%s", pushStream, payload.TrackingID),
		data,
	)
	if err != nil {
		return err
	}

	return nil
}

// PublishMessaging publishes one messaging event on MESSAGING.<kind>, with
// Nats-Msg-Id set to its eventId so JetStream drops a retry inside the
// duplicate window. Unlike Send, the caller is the outbox worker and an error
// is returned for River to retry.
func (p *Pub) PublishMessaging(ctx context.Context, payload event.MessagingEvent) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = p.js.Publish(
		ctx,
		event.MessagingSubjectPrefix+payload.Kind,
		data,
		jetstream.WithMsgID(payload.EventID),
		jetstream.WithExpectStream(event.MessagingStream),
	)
	return err
}
