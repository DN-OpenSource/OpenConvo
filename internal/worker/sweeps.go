package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/openstream/openstream/internal/store"
)

// Sweeper runs periodic maintenance: expired ban cleanup, pin expiry,
// event-log/outbox retention and message-partition maintenance
// (SPEC.md §2.2 worker responsibilities).
type Sweeper struct {
	Store    *store.Store
	Interval time.Duration
	// EventLogRetention bounds the /sync replay window (SPEC.md §8.1).
	EventLogRetention time.Duration
	Log               *slog.Logger
}

// Run sweeps until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = time.Minute
	}
	if s.EventLogRetention <= 0 {
		s.EventLogRetention = 7 * 24 * time.Hour
	}
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) {
	run := func(name string, fn func() error) {
		if err := fn(); err != nil && ctx.Err() == nil {
			s.Log.Error("sweep", "job", name, "error", err)
		}
	}
	pool := s.Store.Pool
	run("expire_bans", func() error { return store.ExpireBans(ctx, pool) })
	run("expire_pins", func() error {
		_, err := pool.Exec(ctx, `UPDATE messages SET pinned=false, updated_at=now()
			WHERE pinned AND pin_expires IS NOT NULL AND pin_expires <= now()`)
		return err
	})
	run("prune_event_log", func() error { return store.PruneEventLog(ctx, pool, s.EventLogRetention) })
	run("prune_outbox", func() error { return store.PruneOutbox(ctx, pool, 24*time.Hour) })
	run("message_partitions", func() error { return store.EnsureMessagePartitions(ctx, pool) })
}
