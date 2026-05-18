package sub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/pub"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/nats-io/nats.go/jetstream"
)

type (
	SubObts struct {
		Store       store.Store
		JS          jetstream.JetStream
		ConsumerID  string
		SkipBacklog bool
		Pub         *pub.Pub
		Logg        *slog.Logger
	}

	Sub struct {
		store  store.Store
		js     jetstream.JetStream
		jsIter jetstream.MessagesContext
		pub    *pub.Pub
		logg   *slog.Logger
		done   chan struct{}
	}
)

const (
	pullStream  = "TRACKER"
	pullSubject = "TRACKER.*"
)

func NewSub(o SubObts) (*Sub, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := o.JS.Stream(ctx, pullStream)
	if err != nil {
		return nil, err
	}

	// Dev-only escape hatch
	if o.SkipBacklog {
		if err := stream.DeleteConsumer(ctx, o.ConsumerID); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
			return nil, err
		}
		o.Logg.Warn("Sub: skip_backlog enabled, dropped durable consumer", "consumer", o.ConsumerID)
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       o.ConsumerID,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: pullSubject,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return nil, err
	}

	iter, err := consumer.Messages(
		jetstream.WithMessagesErrOnMissingHeartbeat(false),
		jetstream.PullMaxMessages(10),
	)
	if err != nil {
		return nil, err
	}

	return &Sub{
		store:  o.Store,
		js:     o.JS,
		jsIter: iter,
		pub:    o.Pub,
		logg:   o.Logg,
		done:   make(chan struct{}),
	}, nil
}

func (s *Sub) Close(ctx context.Context) error {
	s.logg.Debug("Sub: draining js sub iterator")
	s.jsIter.Drain()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.logg.Warn("Sub: drain deadline exceeded, forcing stop")
		s.jsIter.Stop()
		<-s.done
		return ctx.Err()
	}
}

func (s *Sub) Process() {
	defer close(s.done)
	s.logg.Debug("Sub: starting js sub iterator processor")
	for {
		msg, err := s.jsIter.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				s.logg.Debug("Sub: iterator closed")
				return
			}
			s.logg.Error("Sub: iterator error, stopping", "error", err)
			return
		}

		s.logg.Debug("Processing nats message", "subject", msg.Subject())
		if err := s.processEvent(context.Background(), msg.Subject(), msg.Data()); err != nil {
			s.logg.Error("Jetstream: router: error processing nats message", "error", err)
			msg.Nak()
		} else {
			msg.Ack()
		}
	}
}
