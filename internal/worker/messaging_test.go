package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
)

type (
	fakeMessagingPublisher struct {
		published []event.MessagingEvent
		err       error
	}
)

func (f *fakeMessagingPublisher) PublishMessaging(_ context.Context, ev event.MessagingEvent) error {
	f.published = append(f.published, ev)
	return f.err
}

func TestMessagingEventArgsSerializeAsTheEventBody(t *testing.T) {
	messageID := int64(9)
	args := MessagingEventArgs{MessagingEvent: event.MessagingEvent{
		EventID: "0f8c0000-0000-4000-8000-000000000000", Kind: event.MessageCreated,
		OccurredAt: "2026-11-09T10:00:00Z", ConversationID: 3, ConversationKind: "helpdesk",
		MessageID: &messageID,
		Actor:     event.MessagingActor{UserID: 1, Address: "0x1"},
		Audience:  []event.MessagingAudience{{UserID: 1, Address: "0x1", Push: true}},
	}}
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"eventId":"0f8c0000-0000-4000-8000-000000000000","kind":"message.created",
		"occurredAt":"2026-11-09T10:00:00Z","conversationId":3,"conversationKind":"helpdesk",
		"messageId":9,"actor":{"userId":1,"address":"0x1"},
		"audience":[{"userId":1,"address":"0x1","push":true}]}`, string(raw))
}

func TestMessagingEventWorkerPublishesTheSameEventOnEveryAttempt(t *testing.T) {
	pub := &fakeMessagingPublisher{err: errors.New("nats down")}
	w := &MessagingEventWorker{pub: pub}
	job := &river.Job[MessagingEventArgs]{Args: MessagingEventArgs{MessagingEvent: event.MessagingEvent{
		EventID: "e1", Kind: event.ConversationRead,
	}}}

	require.Error(t, w.Work(context.Background(), job), "a failed publish is retried by River")
	pub.err = nil
	require.NoError(t, w.Work(context.Background(), job))
	require.Len(t, pub.published, 2)
	require.Equal(t, pub.published[0].EventID, pub.published[1].EventID)

	require.Error(t, (&MessagingEventWorker{}).Work(context.Background(), job), "no publisher fails rather than dropping the event")
}

func TestEnqueueMessagingEventRequiresAQueueAndAnEventID(t *testing.T) {
	require.Error(t, EnqueueMessagingEvent(context.Background(), nil, nil, event.MessagingEvent{EventID: "e1"}))
}
