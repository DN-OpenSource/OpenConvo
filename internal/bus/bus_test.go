package bus

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern, topic string
		want           bool
	}{
		{"evt.app1.messaging.general", "evt.app1.messaging.general", true},
		{"evt.app1.messaging.general", "evt.app1.messaging.other", false},
		{"evt.app1.*.*", "evt.app1.messaging.general", true},
		{"evt.app1.*.*", "evt.app2.messaging.general", false},
		{"evt.>", "evt.app1.messaging.general", true},
		{"evt.>", "evt", false},
		{"usr.app1.*", "usr.app1.dhiraj", true},
		{"usr.app1.*", "usr.app1.dhiraj.extra", false},
		{"evt.app1.messaging.general", "evt.app1.messaging", false},
	}
	for _, tc := range tests {
		if got := MatchPattern(tc.pattern, tc.topic); got != tc.want {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", tc.pattern, tc.topic, got, tc.want)
		}
	}
}

func TestInProcPublishSubscribe(t *testing.T) {
	b := NewInProc()
	defer func() { _ = b.Close() }()

	received := make(chan string, 10)
	sub, err := b.Subscribe("evt.app1.*.*", "", func(topic string, payload []byte) {
		received <- topic + ":" + string(payload)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := b.Publish(context.Background(), "evt.app1.messaging.general", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != "evt.app1.messaging.general:hello" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}

	// Non-matching topic is not delivered.
	if err := b.Publish(context.Background(), "evt.app2.messaging.general", []byte("nope")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		t.Fatalf("unexpected delivery: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInProcUnsubscribe(t *testing.T) {
	b := NewInProc()
	defer func() { _ = b.Close() }()

	received := make(chan struct{}, 1)
	sub, err := b.Subscribe("a.b", "", func(string, []byte) { received <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(context.Background(), "a.b", []byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
		t.Fatal("delivered after unsubscribe")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInProcConcurrentPublish(t *testing.T) {
	b := NewInProc()
	defer func() { _ = b.Close() }()

	var mu sync.Mutex
	count := 0
	_, err := b.Subscribe("load.>", "", func(string, []byte) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = b.Publish(context.Background(), "load.test", []byte("x"))
			}
		}()
	}
	wg.Wait()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		c := count
		mu.Unlock()
		if c == 500 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("delivered %d of 500", c)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTopicHelpers(t *testing.T) {
	if got := ChannelTopic("app1", "messaging", "general"); got != "evt.app1.messaging.general" {
		t.Fatalf("ChannelTopic = %q", got)
	}
	if got := UserTopic("app1", "dhiraj"); got != "usr.app1.dhiraj" {
		t.Fatalf("UserTopic = %q", got)
	}
	// Subject-unsafe characters are sanitized.
	if got := UserTopic("app1", "a.b*c"); got != "usr.app1.a_b_c" {
		t.Fatalf("sanitize = %q", got)
	}
}
