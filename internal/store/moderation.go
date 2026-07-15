package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

// BanRecord is a stored ban row (channel_type/channel_id empty = global).
type BanRecord struct {
	TargetUserID string
	ChannelType  string
	ChannelID    string
	BannedBy     string
	Reason       string
	Shadow       bool
	Expires      *time.Time
	CreatedAt    time.Time
}

// UpsertBan records a ban (SPEC.md §11.3).
func UpsertBan(ctx context.Context, q Querier, appID string, b BanRecord) error {
	_, err := q.Exec(ctx, `
		INSERT INTO bans (app_id, target_user_id, channel_type, channel_id, banned_by, reason, shadow, expires)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (app_id, target_user_id, channel_type, channel_id) DO UPDATE SET
			banned_by=EXCLUDED.banned_by, reason=EXCLUDED.reason,
			shadow=EXCLUDED.shadow, expires=EXCLUDED.expires, created_at=now()`,
		appID, b.TargetUserID, b.ChannelType, b.ChannelID, b.BannedBy, b.Reason, b.Shadow, b.Expires)
	if err != nil {
		return fmt.Errorf("store: upsert ban: %w", err)
	}
	return nil
}

// DeleteBan lifts a ban.
func DeleteBan(ctx context.Context, q Querier, appID, targetUserID, channelType, channelID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM bans WHERE app_id=$1 AND target_user_id=$2 AND channel_type=$3 AND channel_id=$4`,
		appID, targetUserID, channelType, channelID)
	if err != nil {
		return fmt.Errorf("store: delete ban: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetActiveBan returns the effective ban for a user in a channel scope:
// a global ban wins over a channel ban; expired bans are ignored.
func GetActiveBan(ctx context.Context, q Querier, appID, userID, channelType, channelID string) (*BanRecord, error) {
	rows, err := q.Query(ctx, `SELECT target_user_id, channel_type, channel_id, banned_by, reason, shadow, expires, created_at
		FROM bans
		WHERE app_id=$1 AND target_user_id=$2
		  AND (expires IS NULL OR expires > now())
		  AND ((channel_type='' AND channel_id='') OR (channel_type=$3 AND channel_id=$4))
		ORDER BY (channel_type = '') DESC LIMIT 1`,
		appID, userID, channelType, channelID)
	if err != nil {
		return nil, fmt.Errorf("store: get ban: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var b BanRecord
	if err := rows.Scan(&b.TargetUserID, &b.ChannelType, &b.ChannelID, &b.BannedBy, &b.Reason, &b.Shadow, &b.Expires, &b.CreatedAt); err != nil {
		return nil, err
	}
	return &b, nil
}

// QueryBans lists active bans, optionally scoped to a channel.
func QueryBans(ctx context.Context, q Querier, appID, channelType, channelID string, limit, offset int) ([]BanRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	sql := `SELECT target_user_id, channel_type, channel_id, banned_by, reason, shadow, expires, created_at
		FROM bans WHERE app_id=$1 AND (expires IS NULL OR expires > now())`
	args := []any{appID}
	if channelType != "" {
		sql += ` AND channel_type=$2 AND channel_id=$3`
		args = append(args, channelType, channelID)
	}
	sql += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d OFFSET %d`, limit, offset)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query bans: %w", err)
	}
	defer rows.Close()
	var out []BanRecord
	for rows.Next() {
		var b BanRecord
		if err := rows.Scan(&b.TargetUserID, &b.ChannelType, &b.ChannelID, &b.BannedBy, &b.Reason, &b.Shadow, &b.Expires, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ExpireBans clears expired denormalized ban flags (worker sweep) and
// returns affected user ids.
func ExpireBans(ctx context.Context, q Querier) error {
	if _, err := q.Exec(ctx, `DELETE FROM bans WHERE expires IS NOT NULL AND expires <= now()`); err != nil {
		return fmt.Errorf("store: expire bans: %w", err)
	}
	if _, err := q.Exec(ctx, `UPDATE users SET banned=false, ban_expires=NULL, updated_at=now()
		WHERE banned AND ban_expires IS NOT NULL AND ban_expires <= now()`); err != nil {
		return fmt.Errorf("store: expire user bans: %w", err)
	}
	if _, err := q.Exec(ctx, `UPDATE channel_members SET banned=false, shadow_banned=false, ban_expires=NULL
		WHERE (banned OR shadow_banned) AND ban_expires IS NOT NULL AND ban_expires <= now()`); err != nil {
		return fmt.Errorf("store: expire member bans: %w", err)
	}
	return nil
}

// UpsertMute mutes a target user for a user (SPEC.md §11.3).
func UpsertMute(ctx context.Context, q Querier, appID, userID, targetID string, expires *time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO mutes (app_id, user_id, target_user_id, expires)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (app_id, user_id, target_user_id) DO UPDATE SET expires=EXCLUDED.expires, updated_at=now()`,
		appID, userID, targetID, expires)
	if err != nil {
		return fmt.Errorf("store: upsert mute: %w", err)
	}
	return nil
}

// DeleteMute unmutes.
func DeleteMute(ctx context.Context, q Querier, appID, userID, targetID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM mutes WHERE app_id=$1 AND user_id=$2 AND target_user_id=$3`,
		appID, userID, targetID)
	if err != nil {
		return fmt.Errorf("store: delete mute: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMutes returns a user's active mutes.
func ListMutes(ctx context.Context, q Querier, appID, userID string) ([]*domain.Mute, error) {
	rows, err := q.Query(ctx, `SELECT target_user_id, expires, created_at, updated_at FROM mutes
		WHERE app_id=$1 AND user_id=$2 AND (expires IS NULL OR expires > now())`,
		appID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list mutes: %w", err)
	}
	defer rows.Close()
	var out []*domain.Mute
	for rows.Next() {
		var m domain.Mute
		var targetID string
		if err := rows.Scan(&targetID, &m.Expires, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Target = &domain.User{ID: targetID}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// UpsertChannelMute mutes a channel for a user (SPEC.md §5.2 C10).
func UpsertChannelMute(ctx context.Context, q Querier, appID, userID, channelType, channelID string, expires *time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO channel_mutes (app_id, user_id, channel_type, channel_id, expires)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (app_id, user_id, channel_type, channel_id) DO UPDATE SET expires=EXCLUDED.expires, updated_at=now()`,
		appID, userID, channelType, channelID, expires)
	if err != nil {
		return fmt.Errorf("store: upsert channel mute: %w", err)
	}
	return nil
}

// DeleteChannelMute unmutes a channel.
func DeleteChannelMute(ctx context.Context, q Querier, appID, userID, channelType, channelID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM channel_mutes WHERE app_id=$1 AND user_id=$2 AND channel_type=$3 AND channel_id=$4`,
		appID, userID, channelType, channelID)
	if err != nil {
		return fmt.Errorf("store: delete channel mute: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListChannelMutes returns a user's active channel mutes.
func ListChannelMutes(ctx context.Context, q Querier, appID, userID string) ([]*domain.ChannelMute, error) {
	rows, err := q.Query(ctx, `SELECT channel_type, channel_id, expires, created_at, updated_at FROM channel_mutes
		WHERE app_id=$1 AND user_id=$2 AND (expires IS NULL OR expires > now())`,
		appID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list channel mutes: %w", err)
	}
	defer rows.Close()
	var out []*domain.ChannelMute
	for rows.Next() {
		var m domain.ChannelMute
		var ct, cid string
		if err := rows.Scan(&ct, &cid, &m.Expires, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.CID = domain.CID(ct, cid)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// InsertFlag records a moderation report (SPEC.md §11.4); duplicate open
// flags by the same reporter are idempotent.
func InsertFlag(ctx context.Context, q Querier, appID string, f *domain.Flag) (*domain.Flag, error) {
	custom, err := customJSON(f.Custom)
	if err != nil {
		return nil, fmt.Errorf("store: flag custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		INSERT INTO flags (app_id, created_by, target_message_id, target_user_id, reason, custom)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (app_id, created_by, target_message_id, target_user_id) WHERE reviewed_at IS NULL
		DO UPDATE SET reason=EXCLUDED.reason, custom=EXCLUDED.custom
		RETURNING id, created_by, target_message_id, target_user_id, reason, reviewed_at, reviewed_by, review_result, created_at`,
		appID, f.CreatedByID, f.TargetMessageID, f.TargetUserID, f.Reason, custom)
	var saved domain.Flag
	if err := row.Scan(&saved.ID, &saved.CreatedByID, &saved.TargetMessageID, &saved.TargetUserID,
		&saved.Reason, &saved.ReviewedAt, &saved.ReviewedBy, &saved.ReviewResult, &saved.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: insert flag: %w", err)
	}
	return &saved, nil
}

// DeleteFlag removes an open flag (unflag).
func DeleteFlag(ctx context.Context, q Querier, appID, createdBy, targetMessageID, targetUserID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM flags WHERE app_id=$1 AND created_by=$2 AND target_message_id=$3 AND target_user_id=$4 AND reviewed_at IS NULL`,
		appID, createdBy, targetMessageID, targetUserID)
	if err != nil {
		return fmt.Errorf("store: delete flag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFlagQueue returns unreviewed flags, oldest first (review queue).
func ListFlagQueue(ctx context.Context, q Querier, appID string, limit, offset int) ([]*domain.Flag, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := q.Query(ctx, `SELECT id, created_by, target_message_id, target_user_id, reason, reviewed_at, reviewed_by, review_result, created_at
		FROM flags WHERE app_id=$1 AND reviewed_at IS NULL
		ORDER BY created_at LIMIT $2 OFFSET $3`, appID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: flag queue: %w", err)
	}
	defer rows.Close()
	var out []*domain.Flag
	for rows.Next() {
		var f domain.Flag
		if err := rows.Scan(&f.ID, &f.CreatedByID, &f.TargetMessageID, &f.TargetUserID,
			&f.Reason, &f.ReviewedAt, &f.ReviewedBy, &f.ReviewResult, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

// ReviewFlag records the moderation decision on a flag.
func ReviewFlag(ctx context.Context, q Querier, appID, flagID, reviewerID, result string) error {
	tag, err := q.Exec(ctx, `UPDATE flags SET reviewed_at=now(), reviewed_by=$3, review_result=$4
		WHERE app_id=$1 AND id=$2 AND reviewed_at IS NULL`,
		appID, flagID, reviewerID, result)
	if err != nil {
		return fmt.Errorf("store: review flag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertBlocklist creates/updates a named blocklist (SPEC.md §11.1).
func UpsertBlocklist(ctx context.Context, q Querier, appID string, b *domain.Blocklist) error {
	_, err := q.Exec(ctx, `
		INSERT INTO blocklists (app_id, name, mode, behavior, words)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (app_id, name) DO UPDATE SET
			mode=EXCLUDED.mode, behavior=EXCLUDED.behavior, words=EXCLUDED.words, updated_at=now()`,
		appID, b.Name, b.Mode, b.Behavior, b.Words)
	if err != nil {
		return fmt.Errorf("store: upsert blocklist: %w", err)
	}
	return nil
}

// GetBlocklist fetches one blocklist.
func GetBlocklist(ctx context.Context, q Querier, appID, name string) (*domain.Blocklist, error) {
	var b domain.Blocklist
	err := q.QueryRow(ctx, `SELECT name, mode, behavior, words, created_at, updated_at
		FROM blocklists WHERE app_id=$1 AND name=$2`, appID, name).
		Scan(&b.Name, &b.Mode, &b.Behavior, &b.Words, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, notFoundOr(err, "store: get blocklist")
	}
	return &b, nil
}

// ListBlocklists returns all blocklists for an app.
func ListBlocklists(ctx context.Context, q Querier, appID string) ([]*domain.Blocklist, error) {
	rows, err := q.Query(ctx, `SELECT name, mode, behavior, words, created_at, updated_at
		FROM blocklists WHERE app_id=$1 ORDER BY name`, appID)
	if err != nil {
		return nil, fmt.Errorf("store: list blocklists: %w", err)
	}
	defer rows.Close()
	var out []*domain.Blocklist
	for rows.Next() {
		var b domain.Blocklist
		if err := rows.Scan(&b.Name, &b.Mode, &b.Behavior, &b.Words, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// DeleteBlocklist removes a blocklist.
func DeleteBlocklist(ctx context.Context, q Querier, appID, name string) error {
	tag, err := q.Exec(ctx, `DELETE FROM blocklists WHERE app_id=$1 AND name=$2`, appID, name)
	if err != nil {
		return fmt.Errorf("store: delete blocklist: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InsertAudit writes one moderation audit row (SPEC.md §11.6).
func InsertAudit(ctx context.Context, q Querier, appID, actorID, action, targetType, targetID, reason string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte("{}")
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_log (app_id, actor_id, action, target_type, target_id, reason, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		appID, actorID, action, targetType, targetID, reason, raw)
	if err != nil {
		return fmt.Errorf("store: insert audit: %w", err)
	}
	return nil
}

// AuditEntry is one audit-log row.
type AuditEntry struct {
	ID         int64          `json:"id"`
	ActorID    string         `json:"actor_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Reason     string         `json:"reason,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// ListAudit pages the audit log, newest first (server-only endpoint).
func ListAudit(ctx context.Context, q Querier, appID string, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := q.Query(ctx, `SELECT id, actor_id, action, target_type, target_id, reason, detail, created_at
		FROM audit_log WHERE app_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		appID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var detail []byte
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.TargetType, &e.TargetID, &e.Reason, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detail, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}
