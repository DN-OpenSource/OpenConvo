package state

import (
	"context"
	"sync"
	"time"
)

// Memory is the in-process State for single-binary mode and tests.
type Memory struct {
	mu       sync.Mutex
	conns    map[string]map[string]time.Time // app|user -> connID -> last heartbeat
	watchers map[string]int                  // app|cid -> count
	windows  map[string]*rateWindow
	// connTTL evicts dead connections that never said goodbye.
	connTTL time.Duration
}

type rateWindow struct {
	count int
	reset time.Time
}

// NewMemory builds an in-memory State.
func NewMemory() *Memory {
	return &Memory{
		conns:    map[string]map[string]time.Time{},
		watchers: map[string]int{},
		windows:  map[string]*rateWindow{},
		connTTL:  90 * time.Second,
	}
}

func key(appID, id string) string { return appID + "|" + id }

// AddConnection registers a connection.
func (m *Memory) AddConnection(_ context.Context, appID, userID, connID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(appID, userID)
	set, ok := m.conns[k]
	if !ok {
		set = map[string]time.Time{}
		m.conns[k] = set
	}
	m.evictDeadLocked(k)
	first := len(set) == 0
	set[connID] = time.Now()
	return first, nil
}

// RemoveConnection drops a connection.
func (m *Memory) RemoveConnection(_ context.Context, appID, userID, connID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(appID, userID)
	set := m.conns[k]
	delete(set, connID)
	if len(set) == 0 {
		delete(m.conns, k)
		return true, nil
	}
	return false, nil
}

// TouchConnection refreshes liveness.
func (m *Memory) TouchConnection(_ context.Context, appID, userID, connID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if set, ok := m.conns[key(appID, userID)]; ok {
		if _, exists := set[connID]; exists {
			set[connID] = time.Now()
		}
	}
	return nil
}

func (m *Memory) evictDeadLocked(k string) {
	cutoff := time.Now().Add(-m.connTTL)
	for connID, seen := range m.conns[k] {
		if seen.Before(cutoff) {
			delete(m.conns[k], connID)
		}
	}
}

// OnlineUsers reports presence for a user set.
func (m *Memory) OnlineUsers(_ context.Context, appID string, userIDs []string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		k := key(appID, id)
		m.evictDeadLocked(k)
		out[id] = len(m.conns[k]) > 0
	}
	return out, nil
}

// AdjustWatchers moves a watcher count.
func (m *Memory) AdjustWatchers(_ context.Context, appID, cid string, delta int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(appID, cid)
	n := m.watchers[k] + delta
	if n <= 0 {
		delete(m.watchers, k)
		return 0, nil
	}
	m.watchers[k] = n
	return n, nil
}

// WatcherCount reads a watcher count.
func (m *Memory) WatcherCount(_ context.Context, appID, cid string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watchers[key(appID, cid)], nil
}

// RateAllow implements a fixed-window limiter.
func (m *Memory) RateAllow(_ context.Context, key string, limit int, window time.Duration) (bool, int, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	w, ok := m.windows[key]
	if !ok || now.After(w.reset) {
		w = &rateWindow{reset: now.Add(window)}
		m.windows[key] = w
	}
	if w.count >= limit {
		return false, 0, w.reset, nil
	}
	w.count++
	return true, limit - w.count, w.reset, nil
}

// Close is a no-op.
func (m *Memory) Close() error { return nil }
