package jetstream

import (
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type JetStreamOpts struct {
	Endpoints []string
}

func NewJetStream(o JetStreamOpts) (*nats.Conn, jetstream.JetStream, error) {
	if len(o.Endpoints) == 0 {
		return nil, nil, errors.New("jetstream endpoints are required")
	}

	natsConn, err := nats.Connect(
		strings.Join(o.Endpoints, ","),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, nil, err
	}

	js, err := jetstream.New(natsConn)
	if err != nil {
		return nil, nil, err
	}

	return natsConn, js, nil
}
