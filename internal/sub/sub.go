package sub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/pub"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/riverqueue/river"
)

type (
	SubObts struct {
		ActivateDivviSubmissions bool
		Store                    store.Store
		JS                       jetstream.JetStream
		ConsumerID               string
		Pub                      *pub.Pub
		Provider                 *ethutils.Provider
		Logg                     *slog.Logger
		QueueClient              *river.Client[pgx.Tx]
		UsersIndex               common.Address
	}

	Sub struct {
		activateDivviSubmissions bool
		store                    store.Store
		js                       jetstream.JetStream
		jsIter                   jetstream.MessagesContext
		pub                      *pub.Pub
		provider                 *ethutils.Provider
		logg                     *slog.Logger
		queueClient              *river.Client[pgx.Tx]
		usersIndex               common.Address
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

	consumer, err := stream.CreateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       o.ConsumerID,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: pullSubject,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		if !errors.Is(err, jetstream.ErrConsumerExists) {
			return nil, err
		}
		consumer, err = stream.Consumer(ctx, o.ConsumerID)
		if err != nil {
			return nil, err
		}
	}

	iter, err := consumer.Messages(
		jetstream.WithMessagesErrOnMissingHeartbeat(false),
		jetstream.PullMaxMessages(10),
	)
	if err != nil {
		return nil, err
	}

	return &Sub{
		activateDivviSubmissions: o.ActivateDivviSubmissions,
		store:                    o.Store,
		js:                       o.JS,
		jsIter:                   iter,
		pub:                      o.Pub,
		logg:                     o.Logg,
		provider:                 o.Provider,
		queueClient:              o.QueueClient,
		usersIndex:               o.UsersIndex,
	}, nil
}

func (s *Sub) Close() {
	s.logg.Debug("sub: closing js sub iterator")
	s.jsIter.Stop()
}

func (s *Sub) Process() {
	s.logg.Debug("sub: starting js sub iterator processor")
	for {
		msg, err := s.jsIter.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				s.logg.Debug("sub: iterator closed")
				return
			} else {
				s.logg.Debug("sub: unknown iterator error", "error", err)
				continue
			}
		}

		if err := s.processEvent(context.Background(), msg.Subject(), msg.Data()); err != nil {
			s.logg.Error("jetstream: router: error processing nats message", "error", err)
			msg.Nak()
		} else {
			msg.Ack()
		}
	}
}
