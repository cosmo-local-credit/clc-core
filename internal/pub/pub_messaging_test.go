package pub

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	internaljs "github.com/cosmo-local-credit/clc-core/internal/jetstream"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// Publishes to a real JetStream server named by CORE_TEST_NATS (skipped when
// unset), e.g. nats://127.0.0.1:4222 from `nats-server -js`. The MESSAGING
// stream is deleted afterwards.
func TestPublishMessagingDeduplicatesOnEventID(t *testing.T) {
	endpoint := os.Getenv("CORE_TEST_NATS")
	if endpoint == "" {
		t.Skip("CORE_TEST_NATS not set; skipping live JetStream test")
	}
	conn, js, err := internaljs.NewJetStream(internaljs.JetStreamOpts{Endpoints: []string{endpoint}})
	require.NoError(t, err)
	p := NewPub(PubOpts{PersistDuration: time.Hour, JS: js, NatsConn: conn})
	t.Cleanup(func() {
		_ = js.DeleteStream(context.Background(), event.MessagingStream)
		_ = js.DeleteStream(context.Background(), pushStream)
		p.Close()
	})

	ctx := context.Background()
	stream, err := js.Stream(ctx, event.MessagingStream)
	require.NoError(t, err)
	require.Equal(t, []string{"MESSAGING.>"}, stream.CachedInfo().Config.Subjects)

	messageID := int64(456)
	ev := event.MessagingEvent{
		EventID:          uuid.NewString(),
		Kind:             event.MessageCreated,
		OccurredAt:       "2026-11-09T10:00:00Z",
		ConversationID:   123,
		ConversationKind: "direct",
		MessageID:        &messageID,
		Actor:            event.MessagingActor{UserID: 1, Address: "0xAbC0000000000000000000000000000000000001"},
		Audience: []event.MessagingAudience{
			{UserID: 2, Address: "0xdEf0000000000000000000000000000000000002", Push: true},
			{UserID: 1, Address: "0xAbC0000000000000000000000000000000000001", Push: false},
		},
	}
	require.NoError(t, p.PublishMessaging(ctx, ev))
	require.NoError(t, p.PublishMessaging(ctx, ev), "a retry inside the window is accepted and dropped")

	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), info.State.Msgs)

	msg, err := stream.GetMsg(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "MESSAGING.message.created", msg.Subject)
	require.Equal(t, ev.EventID, msg.Header.Get(jetstream.MsgIDHeader))
	var body map[string]any
	require.NoError(t, json.Unmarshal(msg.Data, &body))
	require.Equal(t, ev.EventID, body["eventId"])
	require.Equal(t, "message.created", body["kind"])
	require.Equal(t, float64(123), body["conversationId"])
	require.Equal(t, float64(456), body["messageId"])
	require.Equal(t, "direct", body["conversationKind"])
	require.Len(t, body["audience"], 2)

	// messageId is omitted for every other kind; conversationId 0 is kept.
	read := ev
	read.EventID, read.Kind, read.MessageID, read.ConversationID = uuid.NewString(), event.BlockChanged, nil, 0
	require.NoError(t, p.PublishMessaging(ctx, read))
	msg, err = stream.GetMsg(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, "MESSAGING.block.changed", msg.Subject)
	body = map[string]any{}
	require.NoError(t, json.Unmarshal(msg.Data, &body))
	require.NotContains(t, body, "messageId")
	require.Equal(t, float64(0), body["conversationId"])
}
