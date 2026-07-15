package domain

import "time"

// Automod modes and behaviors (SPEC.md §6.1; the AI classifier value is
// reserved for v2 and intentionally absent).
const (
	AutomodDisabled = "disabled"
	AutomodSimple   = "simple"

	AutomodBehaviorFlag        = "flag"
	AutomodBehaviorBlock       = "block"
	AutomodBehaviorShadowBlock = "shadow_block"
)

// MaxCustomChannelTypes limits custom channel types per app (SPEC.md §5.2 C3).
const MaxCustomChannelTypes = 50

// ChannelTypeConfig carries every per-type feature flag from SPEC.md §6.1
// plus the permission grants evaluated by the permission engine.
type ChannelTypeConfig struct {
	Name string `json:"name"`

	TypingEvents      bool `json:"typing_events"`
	ReadEvents        bool `json:"read_events"`
	ConnectEvents     bool `json:"connect_events"`
	CustomEvents      bool `json:"custom_events"`
	Reactions         bool `json:"reactions"`
	Replies           bool `json:"replies"`
	Quotes            bool `json:"quotes"`
	Search            bool `json:"search"`
	Mutes             bool `json:"mutes"`
	Uploads           bool `json:"uploads"`
	URLEnrichment     bool `json:"url_enrichment"`
	PushNotifications bool `json:"push_notifications"`

	MessageRetention               string   `json:"message_retention"` // "infinite" or Go duration
	MaxMessageLength               int      `json:"max_message_length"`
	Automod                        string   `json:"automod"`
	AutomodBehavior                string   `json:"automod_behavior"`
	Blocklist                      string   `json:"blocklist,omitempty"`
	Commands                       []string `json:"commands"`
	MarkMessagesPending            bool     `json:"mark_messages_pending"`
	Polls                          bool     `json:"polls"`
	SkipLastMsgUpdateForSystemMsgs bool     `json:"skip_last_msg_update_for_system_msgs"`

	// Grants configure the permission policy per role (SPEC.md §7.1).
	Grants map[string][]Grant `json:"grants,omitempty"`

	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// RetentionDuration parses MessageRetention; ok is false for "infinite".
func (c ChannelTypeConfig) RetentionDuration() (time.Duration, bool) {
	if c.MessageRetention == "" || c.MessageRetention == "infinite" {
		return 0, false
	}
	d, err := time.ParseDuration(c.MessageRetention)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// CommandEnabled reports whether a slash command is enabled for the type.
func (c ChannelTypeConfig) CommandEnabled(name string) bool {
	for _, cmd := range c.Commands {
		if cmd == "all" || cmd == name {
			return true
		}
	}
	return false
}

// baseConfig returns the messaging-style default flag set (SPEC.md §6.1
// "Default (messaging)" column).
func baseConfig(name string) ChannelTypeConfig {
	return ChannelTypeConfig{
		Name:              name,
		TypingEvents:      true,
		ReadEvents:        true,
		ConnectEvents:     true,
		CustomEvents:      true,
		Reactions:         true,
		Replies:           true,
		Quotes:            true,
		Search:            true,
		Mutes:             true,
		Uploads:           true,
		URLEnrichment:     true,
		PushNotifications: true,
		MessageRetention:  "infinite",
		MaxMessageLength:  5000,
		Automod:           AutomodDisabled,
		AutomodBehavior:   AutomodBehaviorFlag,
		Commands:          []string{"all"},
		Grants:            DefaultGrants(),
	}
}

// BuiltinChannelTypes returns the five built-in channel types with their
// Stream-parity default configurations (SPEC.md §6.2).
func BuiltinChannelTypes() map[string]ChannelTypeConfig {
	messaging := baseConfig("messaging")
	messaging.Automod = AutomodSimple

	livestream := baseConfig("livestream")
	livestream.URLEnrichment = false
	livestream.ReadEvents = false
	// Permissive send for any authenticated user; anonymous read.
	livestream.Grants = mergeGrants(DefaultGrants(), map[string][]Grant{
		RoleUser:      {{Action: ActionCreateMessage, Allow: true}, {Action: ActionCreateReaction, Allow: true}},
		RoleGuest:     {{Action: ActionCreateMessage, Allow: true}, {Action: ActionCreateReaction, Allow: true}},
		RoleAnonymous: {{Action: ActionReadChannel, Allow: true}},
	})

	team := baseConfig("team")

	gaming := baseConfig("gaming")
	gaming.ReadEvents = false
	gaming.Uploads = false

	commerce := baseConfig("commerce")

	return map[string]ChannelTypeConfig{
		"messaging":  messaging,
		"livestream": livestream,
		"team":       team,
		"gaming":     gaming,
		"commerce":   commerce,
	}
}

func mergeGrants(base map[string][]Grant, extra map[string][]Grant) map[string][]Grant {
	out := make(map[string][]Grant, len(base))
	for role, grants := range base {
		out[role] = append([]Grant(nil), grants...)
	}
	for role, grants := range extra {
		out[role] = append(out[role], grants...)
	}
	return out
}
