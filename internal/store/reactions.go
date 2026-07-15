package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openstream/openstream/internal/domain"
)

const reactionColumns = `message_id, user_id, type, score, custom, created_at, updated_at`

func scanReaction(row interface{ Scan(...any) error }) (*domain.Reaction, error) {
	var r domain.Reaction
	var custom []byte
	if err := row.Scan(&r.MessageID, &r.UserID, &r.Type, &r.Score, &custom, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if len(custom) > 0 && string(custom) != "{}" {
		if err := json.Unmarshal(custom, &r.Custom); err != nil {
			return nil, fmt.Errorf("store: reaction custom: %w", err)
		}
	}
	return &r, nil
}

// LockMessageReactions serializes reaction mutations per message with a
// transaction-scoped advisory lock — without it, two concurrent reactors
// each aggregate before the other commits and the last denormalized write
// loses updates. Must run inside the same tx as the mutation.
func LockMessageReactions(ctx context.Context, q Querier, appID, messageID string) error {
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 42))`,
		appID+":"+messageID); err != nil {
		return fmt.Errorf("store: lock reactions: %w", err)
	}
	return nil
}

// UpsertReaction adds or updates a reaction (SPEC.md §5.1 M7). With
// enforceUnique the user's other reaction types on the message are removed
// first. Scores accumulate (clap x5) when the same type is sent repeatedly.
func UpsertReaction(ctx context.Context, q Querier, appID string, r *domain.Reaction, enforceUnique bool) (*domain.Reaction, error) {
	if r.Score <= 0 {
		r.Score = 1
	}
	if enforceUnique {
		if _, err := q.Exec(ctx, `DELETE FROM reactions WHERE app_id=$1 AND message_id=$2 AND user_id=$3 AND type != $4`,
			appID, r.MessageID, r.UserID, r.Type); err != nil {
			return nil, fmt.Errorf("store: enforce unique reaction: %w", err)
		}
	}
	custom, err := customJSON(r.Custom)
	if err != nil {
		return nil, fmt.Errorf("store: reaction custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		INSERT INTO reactions (app_id, message_id, user_id, type, score, custom)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (app_id, message_id, user_id, type) DO UPDATE SET
			score = reactions.score + EXCLUDED.score,
			custom = EXCLUDED.custom,
			updated_at = now()
		RETURNING `+reactionColumns,
		appID, r.MessageID, r.UserID, r.Type, r.Score, custom)
	saved, err := scanReaction(row)
	if err != nil {
		return nil, fmt.Errorf("store: upsert reaction: %w", err)
	}
	return saved, nil
}

// DeleteReaction removes one reaction type for a user.
func DeleteReaction(ctx context.Context, q Querier, appID, messageID, userID, reactionType string) (*domain.Reaction, error) {
	row := q.QueryRow(ctx, `DELETE FROM reactions
		WHERE app_id=$1 AND message_id=$2 AND user_id=$3 AND type=$4
		RETURNING `+reactionColumns,
		appID, messageID, userID, reactionType)
	r, err := scanReaction(row)
	if err != nil {
		return nil, notFoundOr(err, "store: delete reaction")
	}
	return r, nil
}

// ListReactions pages a message's reactions, newest first.
func ListReactions(ctx context.Context, q Querier, appID, messageID string, limit, offset int) ([]*domain.Reaction, error) {
	if limit <= 0 || limit > 300 {
		limit = 25
	}
	rows, err := q.Query(ctx, `SELECT `+reactionColumns+` FROM reactions
		WHERE app_id=$1 AND message_id=$2
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		appID, messageID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list reactions: %w", err)
	}
	defer rows.Close()
	var out []*domain.Reaction
	for rows.Next() {
		r, err := scanReaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListOwnReactions returns the viewer's reactions on a message.
func ListOwnReactions(ctx context.Context, q Querier, appID, messageID, userID string) ([]*domain.Reaction, error) {
	rows, err := q.Query(ctx, `SELECT `+reactionColumns+` FROM reactions
		WHERE app_id=$1 AND message_id=$2 AND user_id=$3 ORDER BY created_at DESC`,
		appID, messageID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: own reactions: %w", err)
	}
	defer rows.Close()
	var out []*domain.Reaction
	for rows.Next() {
		r, err := scanReaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AggregateReactions recomputes counts/scores for a message; run in the
// same transaction as the reaction mutation so the denormalized maps on the
// message row can never drift (SPEC.md Prompt 10 "no lost updates").
func AggregateReactions(ctx context.Context, q Querier, appID, messageID string) (counts, scores map[string]int, err error) {
	rows, err := q.Query(ctx, `SELECT type, count(*), COALESCE(sum(score),0) FROM reactions
		WHERE app_id=$1 AND message_id=$2 GROUP BY type`, appID, messageID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: aggregate reactions: %w", err)
	}
	defer rows.Close()
	counts = map[string]int{}
	scores = map[string]int{}
	for rows.Next() {
		var typ string
		var count, score int
		if err := rows.Scan(&typ, &count, &score); err != nil {
			return nil, nil, err
		}
		counts[typ] = count
		scores[typ] = score
	}
	return counts, scores, rows.Err()
}
