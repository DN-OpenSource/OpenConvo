package bus

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// NATS implements Bus over core NATS (at-most-once, low latency — used for
// realtime fan-out; durability comes from the transactional outbox +
// event_log, so JetStream persistence is not required on the hot path).
type NATS struct {
	conn *nats.Conn
}

// NewNATS connects to a NATS server.
func NewNATS(url string) (*NATS, error) {
	conn, err := nats.Connect(url,
		nats.Name("openstream"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("bus: nats connect: %w", err)
	}
	return &NATS{conn: conn}, nil
}

// Publish sends payload to a subject.
func (n *NATS) Publish(_ context.Context, topic string, payload []byte) error {
	if err := n.conn.Publish(topic, payload); err != nil {
		return fmt.Errorf("bus: publish %s: %w", topic, err)
	}
	return nil
}

// Subscribe registers a handler, optionally in a queue group.
func (n *NATS) Subscribe(pattern, queue string, handler Handler) (Subscription, error) {
	cb := func(msg *nats.Msg) { handler(msg.Subject, msg.Data) }
	var sub *nats.Subscription
	var err error
	if queue != "" {
		sub, err = n.conn.QueueSubscribe(pattern, queue, cb)
	} else {
		sub, err = n.conn.Subscribe(pattern, cb)
	}
	if err != nil {
		return nil, fmt.Errorf("bus: subscribe %s: %w", pattern, err)
	}
	return natsSub{sub}, nil
}

type natsSub struct{ *nats.Subscription }

func (s natsSub) Unsubscribe() error { return s.Subscription.Unsubscribe() }

// Close drains and closes the connection.
func (n *NATS) Close() error {
	if err := n.conn.Drain(); err != nil {
		n.conn.Close()
		return fmt.Errorf("bus: drain: %w", err)
	}
	return nil
}
