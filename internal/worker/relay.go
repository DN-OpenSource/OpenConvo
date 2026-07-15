package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/openstream/openstream/internal/bus"
	"github.com/openstream/openstream/internal/store"
)

var (
	relayPublished = promauto.NewCounter(prometheus.CounterOpts{
		Name: "openstream_outbox_published_total",
		Help: "Outbox events published to the bus.",
	})
	relayErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "openstream_outbox_errors_total",
		Help: "Outbox relay errors.",
	})
	relayLag = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "openstream_outbox_lag",
		Help: "Unpublished outbox rows.",
	})
)

// Relay moves committed outbox rows onto the event bus (SPEC.md §2.3).
// Multiple relays are safe: rows are claimed FOR UPDATE SKIP LOCKED.
type Relay struct {
	Store    *store.Store
	Bus      bus.Bus
	Batch    int
	Interval time.Duration
	Log      *slog.Logger
}

// Run polls until ctx is cancelled.
func (r *Relay) Run(ctx context.Context) {
	if r.Batch <= 0 {
		r.Batch = 100
	}
	if r.Interval <= 0 {
		r.Interval = 200 * time.Millisecond
	}
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Drain everything available before sleeping again.
			for {
				n, err := r.relayOnce(ctx)
				if err != nil {
					if ctx.Err() == nil {
						relayErrors.Inc()
						r.Log.Error("outbox relay", "error", err)
					}
					break
				}
				if n < r.Batch {
					break
				}
			}
			if lag, err := store.OutboxLag(ctx, r.Store.Pool); err == nil {
				relayLag.Set(float64(lag))
			}
		}
	}
}

// relayOnce claims one batch, publishes and marks it, all in one tx so a
// crash between publish and mark can only cause redelivery (at-least-once),
// never loss.
func (r *Relay) relayOnce(ctx context.Context) (int, error) {
	var count int
	err := r.Store.InTx(ctx, func(tx store.Tx) error {
		rows, err := store.ClaimOutbox(ctx, tx, r.Batch)
		if err != nil {
			return err
		}
		count = len(rows)
		if count == 0 {
			return nil
		}
		ids := make([]int64, 0, count)
		for _, row := range rows {
			if err := r.Bus.Publish(ctx, row.Topic, row.Payload); err != nil {
				// Publish failure: stop here; unmarked rows retry next tick.
				break
			}
			ids = append(ids, row.ID)
		}
		relayPublished.Add(float64(len(ids)))
		return store.MarkOutboxPublished(ctx, tx, ids)
	})
	return count, err
}
