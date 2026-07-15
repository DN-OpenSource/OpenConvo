package state

import (
	"context"
	"testing"
	"time"
)

func TestMemoryPresenceEdges(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	first, err := m.AddConnection(ctx, "app", "u1", "c1")
	if err != nil || !first {
		t.Fatalf("first connection edge: %v %v", first, err)
	}
	first, _ = m.AddConnection(ctx, "app", "u1", "c2")
	if first {
		t.Fatal("second connection must not be a presence edge")
	}

	online, err := m.OnlineUsers(ctx, "app", []string{"u1", "u2"})
	if err != nil {
		t.Fatal(err)
	}
	if !online["u1"] || online["u2"] {
		t.Fatalf("online = %v", online)
	}

	last, _ := m.RemoveConnection(ctx, "app", "u1", "c1")
	if last {
		t.Fatal("still one connection left")
	}
	last, _ = m.RemoveConnection(ctx, "app", "u1", "c2")
	if !last {
		t.Fatal("last connection must report offline edge")
	}
}

func TestMemoryDeadConnectionEviction(t *testing.T) {
	m := NewMemory()
	m.connTTL = 10 * time.Millisecond
	ctx := context.Background()

	if _, err := m.AddConnection(ctx, "app", "u1", "c1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	online, _ := m.OnlineUsers(ctx, "app", []string{"u1"})
	if online["u1"] {
		t.Fatal("dead connection must be evicted after TTL")
	}

	// A heartbeat keeps it alive.
	if _, err := m.AddConnection(ctx, "app", "u2", "c1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Millisecond)
	if err := m.TouchConnection(ctx, "app", "u2", "c1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Millisecond)
	online, _ = m.OnlineUsers(ctx, "app", []string{"u2"})
	if !online["u2"] {
		t.Fatal("heartbeated connection must stay online")
	}
}

func TestMemoryWatchers(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	n, _ := m.AdjustWatchers(ctx, "app", "messaging:general", 1)
	if n != 1 {
		t.Fatalf("n = %d", n)
	}
	n, _ = m.AdjustWatchers(ctx, "app", "messaging:general", 2)
	if n != 3 {
		t.Fatalf("n = %d", n)
	}
	n, _ = m.AdjustWatchers(ctx, "app", "messaging:general", -5)
	if n != 0 {
		t.Fatalf("watcher count must clamp at 0, got %d", n)
	}
}

func TestMemoryRateLimit(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		allowed, remaining, _, err := m.RateAllow(ctx, "k", 3, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("request %d should be allowed: %v", i, err)
		}
		if remaining != 3-i-1 {
			t.Fatalf("remaining = %d at i=%d", remaining, i)
		}
	}
	allowed, _, reset, _ := m.RateAllow(ctx, "k", 3, time.Minute)
	if allowed {
		t.Fatal("4th request must be limited")
	}
	if time.Until(reset) <= 0 {
		t.Fatal("reset must be in the future")
	}

	// Window expiry re-admits.
	allowed, _, _, _ = m.RateAllow(ctx, "short", 1, 10*time.Millisecond)
	if !allowed {
		t.Fatal("first must pass")
	}
	allowed, _, _, _ = m.RateAllow(ctx, "short", 1, 10*time.Millisecond)
	if allowed {
		t.Fatal("second must fail")
	}
	time.Sleep(15 * time.Millisecond)
	allowed, _, _, _ = m.RateAllow(ctx, "short", 1, 10*time.Millisecond)
	if !allowed {
		t.Fatal("must re-admit after window reset")
	}
}
