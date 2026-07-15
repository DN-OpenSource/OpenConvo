package domain

import (
	"encoding/json"
	"time"
)

// Message types (SPEC.md §5.1 M2).
const (
	MessageTypeRegular   = "regular"
	MessageTypeReply     = "reply"
	MessageTypeSystem    = "system"
	MessageTypeEphemeral = "ephemeral"
	MessageTypeError     = "error"
	MessageTypeDeleted   = "deleted"
)

// App is a tenant on the cluster (SPEC.md §4.2 apps).
type App struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	APIKey    string      `json:"api_key"`
	APISecret string      `json:"-"`
	Settings  AppSettings `json:"settings"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// AppSettings are per-app toggles stored as JSONB.
type AppSettings struct {
	MultiTenantEnabled       bool       `json:"multi_tenant_enabled,omitempty"`
	DisableAuthChecks        bool       `json:"disable_auth_checks,omitempty"`
	RevokeTokensIssuedBefore *time.Time `json:"revoke_tokens_issued_before,omitempty"`
	UploadAllowedMimeTypes   []string   `json:"upload_allowed_mime_types,omitempty"`
	UploadBlockedMimeTypes   []string   `json:"upload_blocked_mime_types,omitempty"`
}

// User is a chat user (SPEC.md §4.2 users).
type User struct {
	ID                       string         `json:"id"`
	Name                     string         `json:"name,omitempty"`
	Image                    string         `json:"image,omitempty"`
	Role                     string         `json:"role"`
	Teams                    []string       `json:"teams,omitempty"`
	Online                   bool           `json:"online"`
	Invisible                bool           `json:"invisible,omitempty"`
	Banned                   bool           `json:"banned,omitempty"`
	BanExpires               *time.Time     `json:"ban_expires,omitempty"`
	DeactivatedAt            *time.Time     `json:"deactivated_at,omitempty"`
	DeletedAt                *time.Time     `json:"deleted_at,omitempty"`
	LastActive               *time.Time     `json:"last_active,omitempty"`
	RevokeTokensIssuedBefore *time.Time     `json:"revoke_tokens_issued_before,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
	Custom                   map[string]any `json:"-"`
}

func (u User) MarshalJSON() ([]byte, error) {
	type alias User
	return marshalFlattened((*alias)(&u), u.Custom)
}

func (u *User) UnmarshalJSON(data []byte) error {
	type alias User
	var a alias
	custom, err := unmarshalFlattened(data, &a)
	if err != nil {
		return err
	}
	*u = User(a)
	u.Custom = custom
	return nil
}

// OwnUser is the connected user's self view delivered in health.check
// (SPEC.md §8.1): the base user plus private state.
type OwnUser struct {
	User
	TotalUnreadCount int            `json:"total_unread_count"`
	UnreadChannels   int            `json:"unread_channels"`
	Mutes            []*Mute        `json:"mutes"`
	ChannelMutes     []*ChannelMute `json:"channel_mutes"`
	Devices          []*Device      `json:"devices"`
}

func (o OwnUser) MarshalJSON() ([]byte, error) {
	base, err := o.User.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	extra := map[string]any{
		"total_unread_count": o.TotalUnreadCount,
		"unread_channels":    o.UnreadChannels,
		"mutes":              emptyIfNil(o.Mutes),
		"channel_mutes":      emptyIfNil(o.ChannelMutes),
		"devices":            emptyIfNil(o.Devices),
	}
	for k, v := range extra {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = raw
	}
	return json.Marshal(m)
}

func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Channel is a conversation container (SPEC.md §4.2 channels).
type Channel struct {
	ID              string             `json:"id"`
	Type            string             `json:"type"`
	CID             string             `json:"cid"`
	CreatedByID     string             `json:"-"`
	CreatedBy       *User              `json:"created_by,omitempty"`
	Team            string             `json:"team,omitempty"`
	Frozen          bool               `json:"frozen"`
	Disabled        bool               `json:"disabled"`
	Cooldown        int                `json:"cooldown,omitempty"`
	MemberCount     int                `json:"member_count"`
	LastMessageAt   *time.Time         `json:"last_message_at,omitempty"`
	TruncatedAt     *time.Time         `json:"truncated_at,omitempty"`
	Config          *ChannelTypeConfig `json:"config,omitempty"`
	OwnCapabilities []string           `json:"own_capabilities,omitempty"`
	Hidden          bool               `json:"hidden,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	DeletedAt       *time.Time         `json:"deleted_at,omitempty"`
	Custom          map[string]any     `json:"-"`
}

func (c Channel) MarshalJSON() ([]byte, error) {
	type alias Channel
	return marshalFlattened((*alias)(&c), c.Custom)
}

func (c *Channel) UnmarshalJSON(data []byte) error {
	type alias Channel
	var a alias
	custom, err := unmarshalFlattened(data, &a)
	if err != nil {
		return err
	}
	*c = Channel(a)
	c.Custom = custom
	return nil
}

// Member is a user's membership in a channel (SPEC.md §4.2 channel_members).
type Member struct {
	UserID             string         `json:"user_id"`
	User               *User          `json:"user,omitempty"`
	ChannelRole        string         `json:"channel_role"`
	Invited            bool           `json:"invited,omitempty"`
	InviteAcceptedAt   *time.Time     `json:"invite_accepted_at,omitempty"`
	InviteRejectedAt   *time.Time     `json:"invite_rejected_at,omitempty"`
	Banned             bool           `json:"banned,omitempty"`
	BanExpires         *time.Time     `json:"ban_expires,omitempty"`
	ShadowBanned       bool           `json:"shadow_banned,omitempty"`
	Hidden             bool           `json:"-"`
	HideMessagesBefore *time.Time     `json:"-"`
	PinnedAt           *time.Time     `json:"pinned_at,omitempty"`
	ArchivedAt         *time.Time     `json:"archived_at,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	Custom             map[string]any `json:"-"`
}

func (m Member) MarshalJSON() ([]byte, error) {
	type alias Member
	return marshalFlattened((*alias)(&m), m.Custom)
}

func (m *Member) UnmarshalJSON(data []byte) error {
	type alias Member
	var a alias
	custom, err := unmarshalFlattened(data, &a)
	if err != nil {
		return err
	}
	*m = Member(a)
	m.Custom = custom
	return nil
}

// Message is a chat message (SPEC.md §4.2 messages).
type Message struct {
	ID              string            `json:"id"`
	CID             string            `json:"cid,omitempty"`
	ChannelType     string            `json:"-"`
	ChannelID       string            `json:"-"`
	UserID          string            `json:"-"`
	User            *User             `json:"user,omitempty"`
	Text            string            `json:"text"`
	HTML            string            `json:"html,omitempty"`
	Type            string            `json:"type"`
	ParentID        string            `json:"parent_id,omitempty"`
	ShowInChannel   bool              `json:"show_in_channel,omitempty"`
	QuotedMessageID string            `json:"quoted_message_id,omitempty"`
	QuotedMessage   *Message          `json:"quoted_message,omitempty"`
	ReplyCount      int               `json:"reply_count"`
	ReactionCounts  map[string]int    `json:"reaction_counts"`
	ReactionScores  map[string]int    `json:"reaction_scores"`
	LatestReactions []*Reaction       `json:"latest_reactions"`
	OwnReactions    []*Reaction       `json:"own_reactions,omitempty"`
	MentionedUsers  []*User           `json:"mentioned_users"`
	Attachments     []Attachment      `json:"attachments"`
	Silent          bool              `json:"silent,omitempty"`
	Pinned          bool              `json:"pinned,omitempty"`
	PinnedByID      string            `json:"-"`
	PinnedBy        *User             `json:"pinned_by,omitempty"`
	PinnedAt        *time.Time        `json:"pinned_at,omitempty"`
	PinExpires      *time.Time        `json:"pin_expires,omitempty"`
	Shadowed        bool              `json:"shadowed,omitempty"`
	PollID          string            `json:"poll_id,omitempty"`
	I18n            map[string]string `json:"i18n,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	DeletedAt       *time.Time        `json:"deleted_at,omitempty"`
	Custom          map[string]any    `json:"-"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	type alias Message
	if m.ReactionCounts == nil {
		m.ReactionCounts = map[string]int{}
	}
	if m.ReactionScores == nil {
		m.ReactionScores = map[string]int{}
	}
	m.LatestReactions = emptyIfNil(m.LatestReactions)
	m.MentionedUsers = emptyIfNil(m.MentionedUsers)
	m.Attachments = emptyIfNil(m.Attachments)
	return marshalFlattened((*alias)(&m), m.Custom)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type alias Message
	var a alias
	custom, err := unmarshalFlattened(data, &a)
	if err != nil {
		return err
	}
	*m = Message(a)
	m.Custom = custom
	return nil
}

// Reaction is a message reaction with optional score (SPEC.md §5.1 M7).
type Reaction struct {
	MessageID string         `json:"message_id"`
	Type      string         `json:"type"`
	Score     int            `json:"score"`
	UserID    string         `json:"user_id"`
	User      *User          `json:"user,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Custom    map[string]any `json:"-"`
}

func (r Reaction) MarshalJSON() ([]byte, error) {
	type alias Reaction
	return marshalFlattened((*alias)(&r), r.Custom)
}

func (r *Reaction) UnmarshalJSON(data []byte) error {
	type alias Reaction
	var a alias
	custom, err := unmarshalFlattened(data, &a)
	if err != nil {
		return err
	}
	*r = Reaction(a)
	r.Custom = custom
	return nil
}

// Attachment is a message attachment; the shape is intentionally loose and
// fully extensible (SPEC.md §5.1 M8).
type Attachment struct {
	Type        string         `json:"type,omitempty"`
	Title       string         `json:"title,omitempty"`
	TitleLink   string         `json:"title_link,omitempty"`
	Text        string         `json:"text,omitempty"`
	ImageURL    string         `json:"image_url,omitempty"`
	ThumbURL    string         `json:"thumb_url,omitempty"`
	AssetURL    string         `json:"asset_url,omitempty"`
	OGScrapeURL string         `json:"og_scrape_url,omitempty"`
	MimeType    string         `json:"mime_type,omitempty"`
	FileSize    int64          `json:"file_size,omitempty"`
	Custom      map[string]any `json:"-"`
}

func (a Attachment) MarshalJSON() ([]byte, error) {
	type alias Attachment
	return marshalFlattened((*alias)(&a), a.Custom)
}

func (a *Attachment) UnmarshalJSON(data []byte) error {
	type alias Attachment
	var al alias
	custom, err := unmarshalFlattened(data, &al)
	if err != nil {
		return err
	}
	*a = Attachment(al)
	a.Custom = custom
	return nil
}

// Read is a member's read state in a channel (SPEC.md §5.3 U14).
type Read struct {
	User              *User     `json:"user"`
	LastRead          time.Time `json:"last_read"`
	UnreadMessages    int       `json:"unread_messages"`
	LastReadMessageID string    `json:"last_read_message_id,omitempty"`
}

// Device is a registered push target (SPEC.md §5.3 U11).
type Device struct {
	ID               string    `json:"id"`
	PushProvider     string    `json:"push_provider"`
	PushProviderName string    `json:"push_provider_name,omitempty"`
	UserID           string    `json:"user_id"`
	Disabled         bool      `json:"disabled,omitempty"`
	DisabledReason   string    `json:"disabled_reason,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// Mute is a user-level mute (SPEC.md §11).
type Mute struct {
	User      *User      `json:"user,omitempty"`
	Target    *User      `json:"target"`
	Expires   *time.Time `json:"expires,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ChannelMute is a per-user channel mute (SPEC.md §5.2 C10).
type ChannelMute struct {
	User      *User      `json:"user,omitempty"`
	Channel   *Channel   `json:"channel,omitempty"`
	CID       string     `json:"cid"`
	Expires   *time.Time `json:"expires,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Ban records a global or channel ban (SPEC.md §11).
type Ban struct {
	User      *User      `json:"user"`
	BannedBy  *User      `json:"banned_by,omitempty"`
	Channel   *Channel   `json:"channel,omitempty"`
	CID       string     `json:"cid,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Shadow    bool       `json:"shadow,omitempty"`
	Expires   *time.Time `json:"expires,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Flag is a moderation report on a message or user (SPEC.md §11).
type Flag struct {
	ID              string         `json:"id"`
	CreatedByID     string         `json:"-"`
	CreatedBy       *User          `json:"user,omitempty"`
	TargetMessageID string         `json:"target_message_id,omitempty"`
	TargetUserID    string         `json:"target_user_id,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	ReviewedAt      *time.Time     `json:"reviewed_at,omitempty"`
	ReviewedBy      string         `json:"reviewed_by,omitempty"`
	ReviewResult    string         `json:"review_result,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	Custom          map[string]any `json:"-"`
}

// Blocklist is a named word list with a matching mode and behavior
// (SPEC.md §11.1).
type Blocklist struct {
	Name      string    `json:"name"`
	Mode      string    `json:"mode"`     // exact | wildcard | regex
	Behavior  string    `json:"behavior"` // block | flag | shadow_block
	Words     []string  `json:"words"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
