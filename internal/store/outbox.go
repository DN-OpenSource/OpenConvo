package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

// OutboxRow is one claimed outbox event.
type OutboxRow struct {
	ID        int64
	AppID     string
	Topic     string
	Payload   []byte
	CreatedAt time.Time
}

// InsertOutbox stages an event inside the caller's transaction — the core
// of the transactional outbox pattern (SPEC.md §2.3): the event commits
// atomically with the data mutation and the relay publishes it to the bus.
func InsertOutbox(ctx context.Context, q Querier, appID, topic string, event *domain.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("store: marshal outbox event: %w", err)
	}
	if _, err := q.Exec(ctx, `INSERT INTO outbox (app_id, topic, payload) VALUES ($1, $2, $3)`,
		appID, topic, payload); err != nil {
		return fmt.Errorf("store: insert outbox: %w", err)
	}
	// Channel-scoped events also land in the bounded event log powering
	// /sync replay (SPEC.md §8.1).
	if event.CID != "" && !event.IsUserScoped() {
		userID := ""
		if event.User != nil {
			userID = event.User.ID
		}
		if _, err := q.Exec(ctx, `INSERT INTO event_log (app_id, cid, user_id, event_id, payload) VALUES ($1, $2, $3, $4, $5)`,
			appID, event.CID, userID, event.EventID, payload); err != nil {
			return fmt.Errorf("store: insert event log: %w", err)
		}
	}
	return nil
}

// ClaimOutbox locks up to batch unpublished rows (FOR UPDATE SKIP LOCKED)
// so multiple relay workers never double-publish. Must run inside a tx.
func ClaimOutbox(ctx context.Context, q Querier, batch int) ([]OutboxRow, error) {
	rows, err := q.Query(ctx, `
		SELECT id, app_id, topic, payload, created_at FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, batch)
	if err != nil {
		return nil, fmt.Errorf("store: claim outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		if err := rows.Scan(&r.ID, &r.AppID, &r.Topic, &r.Payload, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkOutboxPublished stamps rows as delivered to the bus.
func MarkOutboxPublished(ctx context.Context, q Querier, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("store: mark outbox published: %w", err)
	}
	return nil
}

// OutboxLag returns the count of unpublished rows (relay health metric).
func OutboxLag(ctx context.Context, q Querier) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: outbox lag: %w", err)
	}
	return n, nil
}

// PruneOutbox deletes published rows older than the retention window.
func PruneOutbox(ctx context.Context, q Querier, olderThan time.Duration) error {
	_, err := q.Exec(ctx, `DELETE FROM outbox WHERE published_at IS NOT NULL AND published_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
	if err != nil {
		return fmt.Errorf("store: prune outbox: %w", err)
	}
	return nil
}

// ListEventsSince replays channel events for /sync (SPEC.md §8.1).
func ListEventsSince(ctx context.Context, q Querier, appID string, cids []string, since time.Time, limit int) ([]*domain.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := q.Query(ctx, `SELECT payload FROM event_log
		WHERE app_id=$1 AND cid = ANY($2) AND created_at > $3
		ORDER BY id LIMIT $4`,
		appID, cids, since, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list events since: %w", err)
	}
	defer rows.Close()
	var out []*domain.Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var e domain.Event
		if err := json.Unmarshal(payload, &e); err != nil {
			return nil, fmt.Errorf("store: decode event: %w", err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// PruneEventLog enforces the bounded event-log window (default 7 days).
func PruneEventLog(ctx context.Context, q Querier, olderThan time.Duration) error {
	_, err := q.Exec(ctx, `DELETE FROM event_log WHERE created_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
	if err != nil {
		return fmt.Errorf("store: prune event log: %w", err)
	}
	return nil
}
