package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

const channelColumns = `type, id, cid, COALESCE(created_by,''), team, frozen, disabled,
	cooldown, member_count, last_message_at, truncated_at, custom,
	created_at, updated_at, deleted_at`

func scanChannel(row interface{ Scan(...any) error }) (*domain.Channel, error) {
	var c domain.Channel
	var custom []byte
	if err := row.Scan(&c.Type, &c.ID, &c.CID, &c.CreatedByID, &c.Team, &c.Frozen,
		&c.Disabled, &c.Cooldown, &c.MemberCount, &c.LastMessageAt, &c.TruncatedAt,
		&custom, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt); err != nil {
		return nil, err
	}
	if len(custom) > 0 && string(custom) != "{}" {
		if err := json.Unmarshal(custom, &c.Custom); err != nil {
			return nil, fmt.Errorf("store: channel custom: %w", err)
		}
	}
	return &c, nil
}

// CreateChannel inserts a channel if absent and reports whether it was
// created (create-or-get semantics, SPEC.md §5.2 C1).
func CreateChannel(ctx context.Context, q Querier, appID string, c *domain.Channel) (*domain.Channel, bool, error) {
	custom, err := customJSON(c.Custom)
	if err != nil {
		return nil, false, fmt.Errorf("store: channel custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		INSERT INTO channels (app_id, type, id, created_by, team, cooldown, custom)
		VALUES ($1, $2, $3, NULLIF($4,''), $5, $6, $7)
		ON CONFLICT (app_id, type, id) DO NOTHING
		RETURNING `+channelColumns,
		appID, c.Type, c.ID, c.CreatedByID, c.Team, c.Cooldown, custom)
	created, err := scanChannel(row)
	if err == nil {
		return created, true, nil
	}
	existing, err := GetChannel(ctx, q, appID, c.Type, c.ID)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

// GetChannel fetches one channel (soft-deleted channels included; callers
// filter on DeletedAt as needed).
func GetChannel(ctx context.Context, q Querier, appID, channelType, channelID string) (*domain.Channel, error) {
	row := q.QueryRow(ctx, `SELECT `+channelColumns+` FROM channels WHERE app_id=$1 AND type=$2 AND id=$3`,
		appID, channelType, channelID)
	c, err := scanChannel(row)
	if err != nil {
		return nil, notFoundOr(err, "store: get channel")
	}
	return c, nil
}

// UpdateChannel persists mutable channel fields (full update path).
func UpdateChannel(ctx context.Context, q Querier, appID string, c *domain.Channel) (*domain.Channel, error) {
	custom, err := customJSON(c.Custom)
	if err != nil {
		return nil, fmt.Errorf("store: channel custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		UPDATE channels SET team=$4, frozen=$5, disabled=$6, cooldown=$7,
			custom=$8, updated_at=now()
		WHERE app_id=$1 AND type=$2 AND id=$3
		RETURNING `+channelColumns,
		appID, c.Type, c.ID, c.Team, c.Frozen, c.Disabled, c.Cooldown, custom)
	saved, err := scanChannel(row)
	if err != nil {
		return nil, notFoundOr(err, "store: update channel")
	}
	return saved, nil
}

// SoftDeleteChannel tombstones a channel.
func SoftDeleteChannel(ctx context.Context, q Querier, appID, channelType, channelID string) error {
	tag, err := q.Exec(ctx, `UPDATE channels SET deleted_at=now(), updated_at=now()
		WHERE app_id=$1 AND type=$2 AND id=$3 AND deleted_at IS NULL`,
		appID, channelType, channelID)
	if err != nil {
		return fmt.Errorf("store: delete channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// HardDeleteChannel removes the channel and all dependent rows.
func HardDeleteChannel(ctx context.Context, q Querier, appID, channelType, channelID string) error {
	for _, sql := range []string{
		`DELETE FROM reactions WHERE app_id=$1 AND message_id IN (SELECT id FROM messages WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3)`,
		`DELETE FROM messages WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
		`DELETE FROM reads WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
		`DELETE FROM channel_members WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
		`DELETE FROM channel_mutes WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
		`DELETE FROM channels WHERE app_id=$1 AND type=$2 AND id=$3`,
	} {
		if _, err := q.Exec(ctx, sql, appID, channelType, channelID); err != nil {
			return fmt.Errorf("store: hard delete channel: %w", err)
		}
	}
	return nil
}

// TruncateChannel wipes messages (SPEC.md §5.2 C12): hard removes rows,
// stamps truncated_at, and resets member unread counts.
func TruncateChannel(ctx context.Context, q Querier, appID, channelType, channelID string, hard bool) (*domain.Channel, error) {
	now := time.Now().UTC()
	if hard {
		if _, err := q.Exec(ctx, `DELETE FROM messages WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
			appID, channelType, channelID); err != nil {
			return nil, fmt.Errorf("store: truncate messages: %w", err)
		}
	} else {
		if _, err := q.Exec(ctx, `UPDATE messages SET deleted_at=now(), type='deleted', updated_at=now()
			WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND deleted_at IS NULL`,
			appID, channelType, channelID); err != nil {
			return nil, fmt.Errorf("store: truncate messages: %w", err)
		}
	}
	if _, err := q.Exec(ctx, `UPDATE reads SET unread_messages=0, last_read=$4, updated_at=now()
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
		appID, channelType, channelID, now); err != nil {
		return nil, fmt.Errorf("store: truncate reads: %w", err)
	}
	row := q.QueryRow(ctx, `UPDATE channels SET truncated_at=$4, last_message_at=NULL, updated_at=now()
		WHERE app_id=$1 AND type=$2 AND id=$3 RETURNING `+channelColumns,
		appID, channelType, channelID, now)
	c, err := scanChannel(row)
	if err != nil {
		return nil, notFoundOr(err, "store: truncate channel")
	}
	return c, nil
}

// BumpLastMessageAt advances the channel ordering timestamp.
func BumpLastMessageAt(ctx context.Context, q Querier, appID, channelType, channelID string, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE channels SET last_message_at=GREATEST(COALESCE(last_message_at,'-infinity'::timestamptz),$4), updated_at=now()
		WHERE app_id=$1 AND type=$2 AND id=$3`,
		appID, channelType, channelID, at)
	if err != nil {
		return fmt.Errorf("store: bump last_message_at: %w", err)
	}
	return nil
}

// RecountMembers refreshes the denormalized member_count.
func RecountMembers(ctx context.Context, q Querier, appID, channelType, channelID string) (int, error) {
	var count int
	err := q.QueryRow(ctx, `
		UPDATE channels SET member_count = (
			SELECT count(*) FROM channel_members
			WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3
		), updated_at=now()
		WHERE app_id=$1 AND type=$2 AND id=$3
		RETURNING member_count`,
		appID, channelType, channelID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: recount members: %w", err)
	}
	return count, nil
}

// QueryChannels runs a compiled filter with a whitelisted sort. The
// requesting user id scopes hidden-channel exclusion; pass "" for
// server-side queries.
func QueryChannels(ctx context.Context, q Querier, appID, whereSQL string, args []any, orderBy string, limit, offset int, forUserID string) ([]*domain.Channel, error) {
	args = append(args, appID)
	appArg := len(args)
	hiddenClause := "TRUE"
	if forUserID != "" {
		args = append(args, forUserID)
		hiddenClause = fmt.Sprintf(`NOT EXISTS (
			SELECT 1 FROM channel_members hm
			WHERE hm.app_id = channels.app_id AND hm.channel_type = channels.type
			  AND hm.channel_id = channels.id AND hm.user_id = $%d AND hm.hidden)`, len(args))
	}
	sql := fmt.Sprintf(`SELECT %s FROM channels
		WHERE app_id = $%d AND deleted_at IS NULL AND %s AND (%s)
		ORDER BY %s LIMIT %d OFFSET %d`,
		channelColumns, appArg, hiddenClause, whereSQL, orderBy, limit, offset)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query channels: %w", err)
	}
	defer rows.Close()
	var channels []*domain.Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, rows.Err()
}
