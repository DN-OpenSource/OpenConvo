// Package state holds ephemeral cluster state (SPEC.md §2.1 Redis box):
// the presence connection registry, watcher counts and rate-limit counters.
// Two implementations ship: Redis for clustered deployments and an
// in-memory store for single-binary mode and tests.
package state

import (
	"context"
	"time"
)

// State is the ephemeral-state interface shared by Redis and memory
// implementations.
type State interface {
	// AddConnection registers a live WS connection; first reports whether
	// this is the user's first active connection (presence online edge).
	AddConnection(ctx context.Context, appID, userID, connID string) (first bool, err error)
	// RemoveConnection drops a connection; last reports whether the user
	// has no remaining connections (presence offline edge).
	RemoveConnection(ctx context.Context, appID, userID, connID string) (last bool, err error)
	// TouchConnection refreshes a connection's liveness TTL (heartbeat).
	TouchConnection(ctx context.Context, appID, userID, connID string) error
	// OnlineUsers reports which of userIDs currently have connections.
	OnlineUsers(ctx context.Context, appID string, userIDs []string) (map[string]bool, error)

	// AdjustWatchers moves a channel's watcher count and returns the new
	// value (never negative).
	AdjustWatchers(ctx context.Context, appID, cid string, delta int) (int, error)
	// WatcherCount reads a channel's watcher count.
	WatcherCount(ctx context.Context, appID, cid string) (int, error)

	// RateAllow implements a fixed-window rate limit for key. It returns
	// whether the request is admitted, the remaining budget and the window
	// reset time.
	RateAllow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, remaining int, reset time.Time, err error)

	Close() error
}
