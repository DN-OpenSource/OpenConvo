package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

const messageColumns = `id, channel_type, channel_id, user_id, text, html, type,
	COALESCE(parent_id,''), show_in_channel, COALESCE(quoted_message_id,''),
	reply_count, reaction_counts, reaction_scores, mentioned_users, attachments,
	silent, pinned, COALESCE(pinned_by,''), pinned_at, pin_expires, shadowed,
	COALESCE(poll_id::text,''), i18n, custom, created_at, updated_at, deleted_at`

func scanMessage(row interface{ Scan(...any) error }) (*domain.Message, error) {
	var m domain.Message
	var counts, scores, attachments, custom, i18n []byte
	var mentioned []string
	if err := row.Scan(&m.ID, &m.ChannelType, &m.ChannelID, &m.UserID, &m.Text,
		&m.HTML, &m.Type, &m.ParentID, &m.ShowInChannel, &m.QuotedMessageID,
		&m.ReplyCount, &counts, &scores, &mentioned, &attachments, &m.Silent,
		&m.Pinned, &m.PinnedByID, &m.PinnedAt, &m.PinExpires, &m.Shadowed,
		&m.PollID, &i18n, &custom, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt); err != nil {
		return nil, err
	}
	m.CID = domain.CID(m.ChannelType, m.ChannelID)
	for _, id := range mentioned {
		m.MentionedUsers = append(m.MentionedUsers, &domain.User{ID: id})
	}
	if err := json.Unmarshal(counts, &m.ReactionCounts); err != nil {
		return nil, fmt.Errorf("store: reaction counts: %w", err)
	}
	if err := json.Unmarshal(scores, &m.ReactionScores); err != nil {
		return nil, fmt.Errorf("store: reaction scores: %w", err)
	}
	if len(attachments) > 0 && string(attachments) != "[]" {
		if err := json.Unmarshal(attachments, &m.Attachments); err != nil {
			return nil, fmt.Errorf("store: attachments: %w", err)
		}
	}
	if len(i18n) > 0 {
		if err := json.Unmarshal(i18n, &m.I18n); err != nil {
			return nil, fmt.Errorf("store: i18n: %w", err)
		}
	}
	if len(custom) > 0 && string(custom) != "{}" {
		if err := json.Unmarshal(custom, &m.Custom); err != nil {
			return nil, fmt.Errorf("store: message custom: %w", err)
		}
	}
	return &m, nil
}

func mentionedIDs(m *domain.Message) []string {
	ids := make([]string, 0, len(m.MentionedUsers))
	for _, u := range m.MentionedUsers {
		ids = append(ids, u.ID)
	}
	return ids
}

// InsertMessage persists a new message. Idempotency (SPEC.md §5.1 M19) is
// enforced with a guarded insert on the (app_id, id) lookup index: a global
// unique constraint is impossible on the partitioned table, so a concurrent
// duplicate of the same client id is resolved by returning the stored row.
func InsertMessage(ctx context.Context, q Querier, appID string, m *domain.Message) (*domain.Message, bool, error) {
	counts, _ := json.Marshal(map[string]int{})
	scores, _ := json.Marshal(map[string]int{})
	attachments, err := json.Marshal(m.Attachments)
	if err != nil {
		return nil, false, fmt.Errorf("store: attachments: %w", err)
	}
	custom, err := customJSON(m.Custom)
	if err != nil {
		return nil, false, fmt.Errorf("store: message custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		INSERT INTO messages (app_id, id, channel_type, channel_id, user_id,
			text, html, type, parent_id, show_in_channel, quoted_message_id,
			reaction_counts, reaction_scores, mentioned_users, attachments,
			silent, shadowed, custom)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9,''), $10, NULLIF($11,''),
			$12, $13, $14, $15, $16, $17, $18
		WHERE NOT EXISTS (SELECT 1 FROM messages WHERE app_id=$1 AND id=$2)
		RETURNING `+messageColumns,
		appID, m.ID, m.ChannelType, m.ChannelID, m.UserID, m.Text, m.HTML,
		m.Type, m.ParentID, m.ShowInChannel, m.QuotedMessageID, counts, scores,
		mentionedIDs(m), attachments, m.Silent, m.Shadowed, custom)
	saved, err := scanMessage(row)
	if err == nil {
		return saved, true, nil
	}
	existing, gerr := GetMessage(ctx, q, appID, m.ID)
	if gerr != nil {
		return nil, false, fmt.Errorf("store: insert message: %w", err)
	}
	return existing, false, nil
}

// GetMessage fetches a message by id.
func GetMessage(ctx context.Context, q Querier, appID, id string) (*domain.Message, error) {
	row := q.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE app_id=$1 AND id=$2
		ORDER BY created_at DESC LIMIT 1`, appID, id)
	m, err := scanMessage(row)
	if err != nil {
		return nil, notFoundOr(err, "store: get message")
	}
	return m, nil
}

// GetMessagesByIDs fetches a message set within one channel.
func GetMessagesByIDs(ctx context.Context, q Querier, appID, channelType, channelID string, ids []string) ([]*domain.Message, error) {
	rows, err := q.Query(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND id = ANY($4)
		ORDER BY created_at`, appID, channelType, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("store: get messages: %w", err)
	}
	defer rows.Close()
	return collectMessages(rows)
}

// MessagePage describes channel-history pagination (SPEC.md §5.2 C9).
type MessagePage struct {
	Limit           int
	IDLT            string // strictly before this message id
	IDGT            string // strictly after this message id
	IDLTE           string
	IDGTE           string
	AroundID        string
	CreatedAtBefore *time.Time
	CreatedAtAfter  *time.Time
	// ViewerID filters shadowed messages (visible only to their author) and
	// respects the member's hide_messages_before horizon when set.
	ViewerID           string
	HideMessagesBefore *time.Time
	ParentID           string // when set, page thread replies instead
}

func (p MessagePage) anchor(ctx context.Context, q Querier, appID, id string) (time.Time, error) {
	var at time.Time
	err := q.QueryRow(ctx, `SELECT created_at FROM messages WHERE app_id=$1 AND id=$2 ORDER BY created_at DESC LIMIT 1`,
		appID, id).Scan(&at)
	if err != nil {
		return at, notFoundOr(err, "store: pagination anchor")
	}
	return at, nil
}

// ListMessages pages messages for a channel or thread, newest window
// returned in ascending order.
func ListMessages(ctx context.Context, q Querier, appID, channelType, channelID string, p MessagePage) ([]*domain.Message, error) {
	if p.Limit <= 0 || p.Limit > 300 {
		p.Limit = 25
	}
	args := []any{appID, channelType, channelID}
	bind := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	where := "app_id=$1 AND channel_type=$2 AND channel_id=$3"
	if p.ParentID != "" {
		where += " AND parent_id=" + bind(p.ParentID)
	} else {
		where += " AND (parent_id IS NULL OR show_in_channel)"
	}
	if p.ViewerID != "" {
		where += fmt.Sprintf(" AND (NOT shadowed OR user_id=%s)", bind(p.ViewerID))
	}
	if p.HideMessagesBefore != nil {
		where += " AND created_at >= " + bind(*p.HideMessagesBefore)
	}
	if p.CreatedAtBefore != nil {
		where += " AND created_at < " + bind(*p.CreatedAtBefore)
	}
	if p.CreatedAtAfter != nil {
		where += " AND created_at > " + bind(*p.CreatedAtAfter)
	}

	type anchorSpec struct {
		id  string
		cmp string
	}
	for _, a := range []anchorSpec{{p.IDLT, "<"}, {p.IDLTE, "<="}, {p.IDGT, ">"}, {p.IDGTE, ">="}} {
		if a.id == "" {
			continue
		}
		at, err := p.anchor(ctx, q, appID, a.id)
		if err != nil {
			return nil, err
		}
		where += fmt.Sprintf(" AND (created_at, id) %s (%s, %s)", a.cmp, bind(at), bind(a.id))
	}

	if p.AroundID != "" {
		at, err := p.anchor(ctx, q, appID, p.AroundID)
		if err != nil {
			return nil, err
		}
		half := p.Limit / 2
		sql := fmt.Sprintf(`(
			SELECT %s FROM messages WHERE %s AND created_at <= %s
			ORDER BY created_at DESC, id DESC LIMIT %d
		) UNION ALL (
			SELECT %s FROM messages WHERE %s AND created_at > %s
			ORDER BY created_at, id LIMIT %d
		) ORDER BY created_at, id`,
			messageColumns, where, bind(at), half+1,
			messageColumns, where, fmt.Sprintf("$%d", len(args)), half)
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return nil, fmt.Errorf("store: list messages around: %w", err)
		}
		defer rows.Close()
		return collectMessages(rows)
	}

	// Default window: newest first, then re-sort ascending for the client.
	order := "created_at DESC, id DESC"
	if p.IDGT != "" || p.IDGTE != "" || p.CreatedAtAfter != nil {
		order = "created_at, id"
	}
	sql := fmt.Sprintf(`SELECT * FROM (
			SELECT %s FROM messages WHERE %s ORDER BY %s LIMIT %d
		) page ORDER BY created_at, id`,
		messageColumns, where, order, p.Limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list messages: %w", err)
	}
	defer rows.Close()
	return collectMessages(rows)
}

func collectMessages(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*domain.Message, error) {
	var out []*domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateMessage persists edits to text/html/attachments/custom/pin state.
func UpdateMessage(ctx context.Context, q Querier, appID string, m *domain.Message) (*domain.Message, error) {
	attachments, err := json.Marshal(m.Attachments)
	if err != nil {
		return nil, fmt.Errorf("store: attachments: %w", err)
	}
	custom, err := customJSON(m.Custom)
	if err != nil {
		return nil, fmt.Errorf("store: message custom: %w", err)
	}
	row := q.QueryRow(ctx, `
		UPDATE messages SET text=$3, html=$4, type=$5, mentioned_users=$6,
			attachments=$7, custom=$8, pinned=$9, pinned_by=NULLIF($10,''),
			pinned_at=$11, pin_expires=$12, silent=$13, updated_at=now()
		WHERE app_id=$1 AND id=$2
		RETURNING `+messageColumns,
		appID, m.ID, m.Text, m.HTML, m.Type, mentionedIDs(m), attachments,
		custom, m.Pinned, m.PinnedByID, m.PinnedAt, m.PinExpires, m.Silent)
	saved, err := scanMessage(row)
	if err != nil {
		return nil, notFoundOr(err, "store: update message")
	}
	return saved, nil
}

// SoftDeleteMessage tombstones a message (type becomes "deleted").
func SoftDeleteMessage(ctx context.Context, q Querier, appID, id string) (*domain.Message, error) {
	row := q.QueryRow(ctx, `
		UPDATE messages SET deleted_at=now(), type='deleted', text='', html='',
			attachments='[]', custom='{}', updated_at=now()
		WHERE app_id=$1 AND id=$2 AND deleted_at IS NULL
		RETURNING `+messageColumns, appID, id)
	m, err := scanMessage(row)
	if err != nil {
		return nil, notFoundOr(err, "store: soft delete message")
	}
	return m, nil
}

// HardDeleteMessage removes the row and its reactions.
func HardDeleteMessage(ctx context.Context, q Querier, appID, id string) error {
	if _, err := q.Exec(ctx, `DELETE FROM reactions WHERE app_id=$1 AND message_id=$2`, appID, id); err != nil {
		return fmt.Errorf("store: delete reactions: %w", err)
	}
	tag, err := q.Exec(ctx, `DELETE FROM messages WHERE app_id=$1 AND id=$2`, appID, id)
	if err != nil {
		return fmt.Errorf("store: hard delete message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AdjustReplyCount increments/decrements a thread parent's reply_count.
func AdjustReplyCount(ctx context.Context, q Querier, appID, parentID string, delta int) (int, error) {
	var count int
	err := q.QueryRow(ctx, `UPDATE messages SET reply_count = GREATEST(reply_count + $3, 0), updated_at=now()
		WHERE app_id=$1 AND id=$2 RETURNING reply_count`,
		appID, parentID, delta).Scan(&count)
	if err != nil {
		return 0, notFoundOr(err, "store: adjust reply count")
	}
	return count, nil
}

// SetReactionDenorm writes the denormalized reaction maps on the message.
func SetReactionDenorm(ctx context.Context, q Querier, appID, messageID string, counts, scores map[string]int) error {
	countsJSON, _ := json.Marshal(counts)
	scoresJSON, _ := json.Marshal(scores)
	_, err := q.Exec(ctx, `UPDATE messages SET reaction_counts=$3, reaction_scores=$4, updated_at=now()
		WHERE app_id=$1 AND id=$2`, appID, messageID, countsJSON, scoresJSON)
	if err != nil {
		return fmt.Errorf("store: set reaction denorm: %w", err)
	}
	return nil
}

// ListPinnedMessages returns a channel's pinned messages (SPEC.md M10).
func ListPinnedMessages(ctx context.Context, q Querier, appID, channelType, channelID string, limit int) ([]*domain.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := q.Query(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE app_id=$1 AND channel_type=$2 AND channel_id=$3 AND pinned
		  AND deleted_at IS NULL
		  AND (pin_expires IS NULL OR pin_expires > now())
		ORDER BY pinned_at DESC LIMIT $4`,
		appID, channelType, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list pinned: %w", err)
	}
	defer rows.Close()
	return collectMessages(rows)
}

// EnsureMessagePartitions materializes current and next month partitions
// (worker maintenance job).
func EnsureMessagePartitions(ctx context.Context, q Querier) error {
	if _, err := q.Exec(ctx, `SELECT ensure_message_partition(date_trunc('month', now())::date)`); err != nil {
		return fmt.Errorf("store: ensure partition: %w", err)
	}
	if _, err := q.Exec(ctx, `SELECT ensure_message_partition((date_trunc('month', now()) + interval '1 month')::date)`); err != nil {
		return fmt.Errorf("store: ensure next partition: %w", err)
	}
	return nil
}
