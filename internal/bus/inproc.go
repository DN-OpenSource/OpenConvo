package bus

import (
	"context"
	"sync"
	"sync/atomic"
)

// InProc is the in-process Bus for single-binary mode and tests. Delivery
// is asynchronous (per-subscriber goroutine with a bounded queue) to match
// NATS semantics; slow subscribers drop oldest events rather than blocking
// publishers.
type InProc struct {
	mu     sync.RWMutex
	subs   map[int64]*inprocSub
	nextID atomic.Int64
	closed bool
}

type inprocSub struct {
	bus     *InProc
	id      int64
	pattern string
	queue   string
	handler Handler
	ch      chan inprocMsg
	done    chan struct{}
}

type inprocMsg struct {
	topic   string
	payload []byte
}

// NewInProc creates an in-process bus.
func NewInProc() *InProc {
	return &InProc{subs: map[int64]*inprocSub{}}
}

// Publish delivers to every matching subscription; queue groups deliver to
// one member (round-robin by subscription id order is acceptable here —
// single process).
func (b *InProc) Publish(_ context.Context, topic string, payload []byte) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	seenQueues := map[string]bool{}
	for _, sub := range b.subs {
		if !MatchPattern(sub.pattern, topic) {
			continue
		}
		if sub.queue != "" {
			key := sub.queue + "|" + sub.pattern
			if seenQueues[key] {
				continue
			}
			seenQueues[key] = true
		}
		select {
		case sub.ch <- inprocMsg{topic: topic, payload: payload}:
		default:
			// Bounded queue full: drop oldest, enqueue newest.
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- inprocMsg{topic: topic, payload: payload}:
			default:
			}
		}
	}
	return nil
}

// Subscribe registers a handler for a pattern.
func (b *InProc) Subscribe(pattern, queue string, handler Handler) (Subscription, error) {
	sub := &inprocSub{
		bus:     b,
		id:      b.nextID.Add(1),
		pattern: pattern,
		queue:   queue,
		handler: handler,
		ch:      make(chan inprocMsg, 1024),
		done:    make(chan struct{}),
	}
	b.mu.Lock()
	b.subs[sub.id] = sub
	b.mu.Unlock()
	go sub.run()
	return sub, nil
}

func (s *inprocSub) run() {
	for {
		select {
		case msg := <-s.ch:
			s.handler(msg.topic, msg.payload)
		case <-s.done:
			return
		}
	}
}

// Unsubscribe removes the subscription.
func (s *inprocSub) Unsubscribe() error {
	s.bus.mu.Lock()
	if _, ok := s.bus.subs[s.id]; ok {
		delete(s.bus.subs, s.id)
		close(s.done)
	}
	s.bus.mu.Unlock()
	return nil
}

// Close shuts the bus down.
func (b *InProc) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for id, sub := range b.subs {
		close(sub.done)
		delete(b.subs, id)
	}
	return nil
}
