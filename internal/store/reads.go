package store

import (
	"context"
	"fmt"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

// ReadState is one member's raw read-state row.
type ReadState struct {
	UserID            string
	LastRead          time.Time
	LastReadMessageID string
	UnreadMessages    int
}

// MarkRead sets a member's read pointer to now (SPEC.md §5.3 U14).
func MarkRead(ctx context.Context, q Querier, appID, channelType, channelID, userID, lastReadMessageID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO reads (app_id, channel_type, channel_id, user_id, last_read, last_read_message_id, unread_messages)
		VALUES ($1, $2, $3, $4, now(), $5, 0)
		ON CONFLICT (app_id, channel_type, channel_id, user_id) DO UPDATE SET
			last_read = now(), last_read_message_id = EXCLUDED.last_read_message_id,
			unread_messages = 0, updated_at = now()`,
		appID, channelType, channelID, userID, lastReadMessageID)
	if err != nil {
		return fmt.Errorf("store: mark read: %w", err)
	}
	return nil
}

// MarkUnreadFrom rewinds the read pointer to just before a message and
// recounts unread messages from there (SPEC.md §5.3 U14 mark-unread).
func MarkUnreadFrom(ctx context.Context, q Querier, appID, channelType, channelID, userID, messageID string) (int, error) {
	var at time.Time
	err := q.QueryRow(ctx, `SELECT created_at FROM messages WHERE app_id=$1 AND id=$2 ORDER BY created_at DESC LIMIT 1`,
		appID, messageID).Scan(&at)
	if err != nil {
		return 0, notFoundOr(err, "store: mark unread anchor")
	}
	var unread int
	err = q.QueryRow(ctx, `SELECT count(*) FROM messages
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3
		  AND created_at >= $4 AND deleted_at IS NULL AND NOT silent
		  AND (parent_id IS NULL OR show_in_channel)`,
		appID, channelType, channelID, at).Scan(&unread)
	if err != nil {
		return 0, fmt.Errorf("store: count unread: %w", err)
	}
	_, err = q.Exec(ctx, `
		INSERT INTO reads (app_id, channel_type, channel_id, user_id, last_read, last_read_message_id, unread_messages)
		VALUES ($1, $2, $3, $4, $5, '', $6)
		ON CONFLICT (app_id, channel_type, channel_id, user_id) DO UPDATE SET
			last_read = $5, last_read_message_id = '', unread_messages = $6, updated_at = now()`,
		appID, channelType, channelID, userID, at.Add(-time.Microsecond), unread)
	if err != nil {
		return 0, fmt.Errorf("store: mark unread: %w", err)
	}
	return unread, nil
}

// IncrementUnread bumps unread counters for every member except the sender
// (and except members currently shadow-receiving the sender's messages).
func IncrementUnread(ctx context.Context, q Querier, appID, channelType, channelID, senderID string) error {
	_, err := q.Exec(ctx, `UPDATE reads SET unread_messages = unread_messages + 1, updated_at = now()
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id != $4`,
		appID, channelType, channelID, senderID)
	if err != nil {
		return fmt.Errorf("store: increment unread: %w", err)
	}
	return nil
}

// GetChannelReads returns all members' read state for a channel.
func GetChannelReads(ctx context.Context, q Querier, appID, channelType, channelID string) ([]ReadState, error) {
	rows, err := q.Query(ctx, `SELECT user_id, last_read, last_read_message_id, unread_messages
		FROM reads WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
		appID, channelType, channelID)
	if err != nil {
		return nil, fmt.Errorf("store: channel reads: %w", err)
	}
	defer rows.Close()
	var out []ReadState
	for rows.Next() {
		var r ReadState
		if err := rows.Scan(&r.UserID, &r.LastRead, &r.LastReadMessageID, &r.UnreadMessages); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRead returns one member's read state.
func GetRead(ctx context.Context, q Querier, appID, channelType, channelID, userID string) (*ReadState, error) {
	var r ReadState
	r.UserID = userID
	err := q.QueryRow(ctx, `SELECT last_read, last_read_message_id, unread_messages
		FROM reads WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`,
		appID, channelType, channelID, userID).Scan(&r.LastRead, &r.LastReadMessageID, &r.UnreadMessages)
	if err != nil {
		return nil, notFoundOr(err, "store: get read")
	}
	return &r, nil
}

// UnreadChannel is one channel's contribution to the unread summary.
type UnreadChannel struct {
	ChannelType    string
	ChannelID      string
	UnreadMessages int
	LastRead       time.Time
}

// UnreadSummary aggregates unread state for a user (SPEC.md §5.3 U15).
func UnreadSummary(ctx context.Context, q Querier, appID, userID string) (total int, channels []UnreadChannel, err error) {
	rows, err := q.Query(ctx, `SELECT r.channel_type, r.channel_id, r.unread_messages, r.last_read
		FROM reads r
		JOIN channels c ON c.app_id = r.app_id AND c.type = r.channel_type AND c.id = r.channel_id
		WHERE r.app_id=$1 AND r.user_id=$2 AND r.unread_messages > 0 AND c.deleted_at IS NULL
		ORDER BY r.unread_messages DESC`,
		appID, userID)
	if err != nil {
		return 0, nil, fmt.Errorf("store: unread summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u UnreadChannel
		if err := rows.Scan(&u.ChannelType, &u.ChannelID, &u.UnreadMessages, &u.LastRead); err != nil {
			return 0, nil, err
		}
		total += u.UnreadMessages
		channels = append(channels, u)
	}
	return total, channels, rows.Err()
}

// ReadsToDomain joins read states with user objects.
func ReadsToDomain(states []ReadState, users map[string]*domain.User) []*domain.Read {
	out := make([]*domain.Read, 0, len(states))
	for _, s := range states {
		u := users[s.UserID]
		if u == nil {
			u = &domain.User{ID: s.UserID}
		}
		out = append(out, &domain.Read{
			User:              u,
			LastRead:          s.LastRead,
			UnreadMessages:    s.UnreadMessages,
			LastReadMessageID: s.LastReadMessageID,
		})
	}
	return out
}
