package domain

import (
	"encoding/json"
	"time"
)

// Channel-scoped event types (SPEC.md §8.2).
const (
	EventMessageNew        = "message.new"
	EventMessageUpdated    = "message.updated"
	EventMessageDeleted    = "message.deleted"
	EventMessageRead       = "message.read"
	EventMessageUndeleted  = "message.undeleted"
	EventReactionNew       = "reaction.new"
	EventReactionUpdated   = "reaction.updated"
	EventReactionDeleted   = "reaction.deleted"
	EventTypingStart       = "typing.start"
	EventTypingStop        = "typing.stop"
	EventMemberAdded       = "member.added"
	EventMemberUpdated     = "member.updated"
	EventMemberRemoved     = "member.removed"
	EventChannelCreated    = "channel.created"
	EventChannelUpdated    = "channel.updated"
	EventChannelDeleted    = "channel.deleted"
	EventChannelTruncated  = "channel.truncated"
	EventChannelFrozen     = "channel.frozen"
	EventChannelUnfrozen   = "channel.unfrozen"
	EventChannelHidden     = "channel.hidden"
	EventChannelVisible    = "channel.visible"
	EventUserWatchingStart = "user.watching.start"
	EventUserWatchingStop  = "user.watching.stop"
)

// User-scoped notification event types, delivered to all of a user's
// connections (SPEC.md §8.2).
const (
	EventNotificationMessageNew          = "notification.message_new"
	EventNotificationMarkRead            = "notification.mark_read"
	EventNotificationMarkUnread          = "notification.mark_unread"
	EventNotificationAddedToChannel      = "notification.added_to_channel"
	EventNotificationRemovedFromChannel  = "notification.removed_from_channel"
	EventNotificationInvited             = "notification.invited"
	EventNotificationInviteAccepted      = "notification.invite_accepted"
	EventNotificationInviteRejected      = "notification.invite_rejected"
	EventNotificationMutesUpdated        = "notification.mutes_updated"
	EventNotificationChannelMutesUpdated = "notification.channel_mutes_updated"
	EventNotificationChannelDeleted      = "notification.channel_deleted"
	EventNotificationChannelTruncated    = "notification.channel_truncated"
	EventUserPresenceChanged             = "user.presence.changed"
	EventUserUpdated                     = "user.updated"
	EventUserBanned                      = "user.banned"
	EventUserUnbanned                    = "user.unbanned"
	EventHealthCheck                     = "health.check"
	EventConnectionRecovered             = "connection.recovered"
)

// Event is the wire envelope for every realtime event (SPEC.md §8).
// Unset fields are omitted so each event type carries only its payload.
type Event struct {
	// EventID is a ULID for ordering and /sync deduplication.
	EventID     string `json:"event_id,omitempty"`
	Type        string `json:"type"`
	CID         string `json:"cid,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`
	ChannelID   string `json:"channel_id,omitempty"`

	User     *User     `json:"user,omitempty"`
	Me       *OwnUser  `json:"me,omitempty"`
	Message  *Message  `json:"message,omitempty"`
	Channel  *Channel  `json:"channel,omitempty"`
	Member   *Member   `json:"member,omitempty"`
	Reaction *Reaction `json:"reaction,omitempty"`

	Reason       string `json:"reason,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	ParentID     string `json:"parent_id,omitempty"`
	WatcherCount int    `json:"watcher_count,omitempty"`

	TotalUnreadCount int `json:"total_unread_count,omitempty"`
	UnreadChannels   int `json:"unread_channels,omitempty"`
	UnreadMessages   int `json:"unread_messages,omitempty"`

	CreatedAt time.Time      `json:"created_at"`
	Custom    map[string]any `json:"-"`
}

func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	return marshalFlattened((*alias)(&e), e.Custom)
}

func (e *Event) UnmarshalJSON(data []byte) error {
	type alias Event
	var a alias
	custom, err := unmarshalFlattened(data, &a)
	if err != nil {
		return err
	}
	*e = Event(a)
	e.Custom = custom
	return nil
}

// IsUserScoped reports whether the event targets a user's connections
// instead of channel watchers.
func (e Event) IsUserScoped() bool {
	switch e.Type {
	case EventNotificationMessageNew, EventNotificationMarkRead,
		EventNotificationMarkUnread, EventNotificationAddedToChannel,
		EventNotificationRemovedFromChannel, EventNotificationInvited,
		EventNotificationInviteAccepted, EventNotificationInviteRejected,
		EventNotificationMutesUpdated, EventNotificationChannelMutesUpdated,
		EventNotificationChannelDeleted, EventNotificationChannelTruncated,
		EventUserUpdated, EventUserBanned, EventUserUnbanned,
		EventHealthCheck, EventConnectionRecovered:
		return true
	}
	return false
}

// Encode renders the event to JSON, panicking never: encoding errors return
// a minimal error payload instead (events are server-constructed).
func (e Event) Encode() []byte {
	data, err := json.Marshal(e)
	if err != nil {
		fallback, _ := json.Marshal(map[string]string{"type": e.Type})
		return fallback
	}
	return data
}
