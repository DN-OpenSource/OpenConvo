package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

const memberColumns = `user_id, channel_role, invited, invite_accepted_at,
	invite_rejected_at, banned, ban_expires, shadow_banned, hidden,
	hide_messages_before, pinned_at, archived_at, custom, created_at`

func scanMember(row interface{ Scan(...any) error }) (*domain.Member, error) {
	var m domain.Member
	var custom []byte
	if err := row.Scan(&m.UserID, &m.ChannelRole, &m.Invited, &m.InviteAcceptedAt,
		&m.InviteRejectedAt, &m.Banned, &m.BanExpires, &m.ShadowBanned, &m.Hidden,
		&m.HideMessagesBefore, &m.PinnedAt, &m.ArchivedAt, &custom, &m.CreatedAt); err != nil {
		return nil, err
	}
	if len(custom) > 0 && string(custom) != "{}" {
		if err := json.Unmarshal(custom, &m.Custom); err != nil {
			return nil, fmt.Errorf("store: member custom: %w", err)
		}
	}
	return &m, nil
}

// AddMember inserts a membership row (idempotent) and seeds the read state.
// Returns the member and whether the row was newly created.
func AddMember(ctx context.Context, q Querier, appID, channelType, channelID string, m *domain.Member) (*domain.Member, bool, error) {
	if m.ChannelRole == "" {
		m.ChannelRole = domain.ChannelRoleMember
	}
	custom, err := customJSON(m.Custom)
	if err != nil {
		return nil, false, fmt.Errorf("store: member custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		INSERT INTO channel_members (app_id, channel_type, channel_id, user_id,
			channel_role, invited, hide_messages_before, custom)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (app_id, channel_type, channel_id, user_id) DO NOTHING
		RETURNING `+memberColumns,
		appID, channelType, channelID, m.UserID, m.ChannelRole, m.Invited, m.HideMessagesBefore, custom)
	saved, err := scanMember(row)
	if err != nil {
		existing, gerr := GetMember(ctx, q, appID, channelType, channelID, m.UserID)
		if gerr != nil {
			return nil, false, gerr
		}
		return existing, false, nil
	}
	_, err = q.Exec(ctx, `
		INSERT INTO reads (app_id, channel_type, channel_id, user_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (app_id, channel_type, channel_id, user_id) DO NOTHING`,
		appID, channelType, channelID, m.UserID)
	if err != nil {
		return nil, false, fmt.Errorf("store: seed read state: %w", err)
	}
	return saved, true, nil
}

// RemoveMember deletes a membership and its read state.
func RemoveMember(ctx context.Context, q Querier, appID, channelType, channelID, userID string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM channel_members WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`,
		appID, channelType, channelID, userID)
	if err != nil {
		return false, fmt.Errorf("store: remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := q.Exec(ctx, `DELETE FROM reads WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`,
		appID, channelType, channelID, userID); err != nil {
		return false, fmt.Errorf("store: remove read state: %w", err)
	}
	return true, nil
}

// GetMember fetches one membership.
func GetMember(ctx context.Context, q Querier, appID, channelType, channelID, userID string) (*domain.Member, error) {
	row := q.QueryRow(ctx, `SELECT `+memberColumns+` FROM channel_members
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`,
		appID, channelType, channelID, userID)
	m, err := scanMember(row)
	if err != nil {
		return nil, notFoundOr(err, "store: get member")
	}
	return m, nil
}

// ListMembers pages a channel's members.
func ListMembers(ctx context.Context, q Querier, appID, channelType, channelID string, limit, offset int) ([]*domain.Member, error) {
	rows, err := q.Query(ctx, `SELECT `+memberColumns+` FROM channel_members
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3
		ORDER BY created_at, user_id LIMIT $4 OFFSET $5`,
		appID, channelType, channelID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list members: %w", err)
	}
	defer rows.Close()
	var members []*domain.Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// ListMemberIDs returns all member user ids for fan-out paths.
func ListMemberIDs(ctx context.Context, q Querier, appID, channelType, channelID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT user_id FROM channel_members
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3`,
		appID, channelType, channelID)
	if err != nil {
		return nil, fmt.Errorf("store: list member ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetMemberChannelRole promotes/demotes a member.
func SetMemberChannelRole(ctx context.Context, q Querier, appID, channelType, channelID, userID, role string) error {
	tag, err := q.Exec(ctx, `UPDATE channel_members SET channel_role=$5
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`,
		appID, channelType, channelID, userID, role)
	if err != nil {
		return fmt.Errorf("store: set member role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMemberHidden hides/shows the channel for one member (SPEC.md §5.2 C11);
// clearHistory also moves the history horizon.
func SetMemberHidden(ctx context.Context, q Querier, appID, channelType, channelID, userID string, hidden, clearHistory bool) error {
	sql := `UPDATE channel_members SET hidden=$5
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`
	if hidden && clearHistory {
		sql = `UPDATE channel_members SET hidden=$5, hide_messages_before=now()
			WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`
	}
	tag, err := q.Exec(ctx, sql, appID, channelType, channelID, userID, hidden)
	if err != nil {
		return fmt.Errorf("store: set member hidden: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UnhideChannelForMembers reveals the channel again for everyone who hid it
// (auto-unhide on new message).
func UnhideChannelForMembers(ctx context.Context, q Querier, appID, channelType, channelID string) ([]string, error) {
	rows, err := q.Query(ctx, `UPDATE channel_members SET hidden=false
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND hidden
		RETURNING user_id`,
		appID, channelType, channelID)
	if err != nil {
		return nil, fmt.Errorf("store: unhide channel: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetMemberBan applies or lifts a channel-scoped ban/shadow-ban.
func SetMemberBan(ctx context.Context, q Querier, appID, channelType, channelID, userID string, banned, shadow bool, expires *time.Time) error {
	_, err := q.Exec(ctx, `UPDATE channel_members SET banned=$5, shadow_banned=$6, ban_expires=$7
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4`,
		appID, channelType, channelID, userID, banned, shadow, expires)
	if err != nil {
		return fmt.Errorf("store: set member ban: %w", err)
	}
	return nil
}

// AcceptInvite / RejectInvite update invite state (SPEC.md §5.2 C6).
func AcceptInvite(ctx context.Context, q Querier, appID, channelType, channelID, userID string) error {
	tag, err := q.Exec(ctx, `UPDATE channel_members SET invited=false, invite_accepted_at=now()
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4 AND invited`,
		appID, channelType, channelID, userID)
	if err != nil {
		return fmt.Errorf("store: accept invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RejectInvite marks an invite rejected.
func RejectInvite(ctx context.Context, q Querier, appID, channelType, channelID, userID string) error {
	tag, err := q.Exec(ctx, `UPDATE channel_members SET invited=false, invite_rejected_at=now()
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND user_id=$4 AND invited`,
		appID, channelType, channelID, userID)
	if err != nil {
		return fmt.Errorf("store: reject invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
