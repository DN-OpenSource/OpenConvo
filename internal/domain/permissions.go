package domain

// Global and channel roles (SPEC.md §7.1).
const (
	RoleUser      = "user"
	RoleGuest     = "guest"
	RoleAnonymous = "anonymous"
	RoleAdmin     = "admin"
	RoleModerator = "moderator"

	ChannelRoleMember    = "channel_member"
	ChannelRoleModerator = "channel_moderator"
	ChannelRoleOwner     = "owner"
)

// Action is a permission-checked operation on a resource (SPEC.md §7.1).
type Action string

// The action catalog, matching Stream's resource/action names.
const (
	ActionCreateChannel                  Action = "CreateChannel"
	ActionReadChannel                    Action = "ReadChannel"
	ActionUpdateChannel                  Action = "UpdateChannel"
	ActionUpdateChannelFrozen            Action = "UpdateChannelFrozen"
	ActionUpdateChannelCooldown          Action = "UpdateChannelCooldown"
	ActionUpdateChannelMembers           Action = "UpdateChannelMembers"
	ActionDeleteChannel                  Action = "DeleteChannel"
	ActionFreezeChannel                  Action = "FreezeChannel"
	ActionTruncateChannel                Action = "TruncateChannel"
	ActionHideChannel                    Action = "HideChannel"
	ActionMuteChannel                    Action = "MuteChannel"
	ActionAddLinks                       Action = "AddLinks"
	ActionCreateMessage                  Action = "CreateMessage"
	ActionUpdateOwnMessage               Action = "UpdateOwnMessage"
	ActionUpdateAnyMessage               Action = "UpdateAnyMessage"
	ActionDeleteOwnMessage               Action = "DeleteOwnMessage"
	ActionDeleteAnyMessage               Action = "DeleteAnyMessage"
	ActionPinMessage                     Action = "PinMessage"
	ActionCreateReply                    Action = "CreateReply"
	ActionQuoteMessage                   Action = "QuoteMessage"
	ActionCreateReaction                 Action = "CreateReaction"
	ActionDeleteOwnReaction              Action = "DeleteOwnReaction"
	ActionDeleteAnyReaction              Action = "DeleteAnyReaction"
	ActionUploadAttachment               Action = "UploadAttachment"
	ActionDeleteOwnAttachment            Action = "DeleteOwnAttachment"
	ActionDeleteAnyAttachment            Action = "DeleteAnyAttachment"
	ActionUseCommands                    Action = "UseCommands"
	ActionSendCustomEvent                Action = "SendCustomEvent"
	ActionSendTypingEvent                Action = "SendTypingEvent"
	ActionSendLinks                      Action = "SendLinks"
	ActionSkipSlowMode                   Action = "SkipSlowMode"
	ActionSkipMessageModeration          Action = "SkipMessageModeration"
	ActionBanUser                        Action = "BanUser"
	ActionBanChannelMember               Action = "BanChannelMember"
	ActionMuteUser                       Action = "MuteUser"
	ActionFlagMessage                    Action = "FlagMessage"
	ActionFlagUser                       Action = "FlagUser"
	ActionReadMessageFlags               Action = "ReadMessageFlags"
	ActionRunMessageAction               Action = "RunMessageAction"
	ActionMarkRead                       Action = "MarkRead"
	ActionSendPoll                       Action = "SendPoll"
	ActionCastPollVote                   Action = "CastPollVote"
	ActionUpdateOwnUser                  Action = "UpdateOwnUser"
	ActionSearchMessages                 Action = "SearchMessages"
	ActionJoinChannel                    Action = "JoinChannel"
	ActionLeaveChannel                   Action = "LeaveChannel"
	ActionCreateDistinctChannelForOthers Action = "CreateDistinctChannelForOthers"
	ActionUpdateChannelMember            Action = "UpdateChannelMember"
	ActionRemoveOwnChannelMembership     Action = "RemoveOwnChannelMembership"
	ActionConnectEvents                  Action = "ConnectEvents"
	ActionReadEvents                     Action = "ReadEvents"
)

// AllActions enumerates the catalog for capability computation and
// dashboard editors.
var AllActions = []Action{
	ActionCreateChannel, ActionReadChannel, ActionUpdateChannel,
	ActionUpdateChannelFrozen, ActionUpdateChannelCooldown,
	ActionUpdateChannelMembers, ActionDeleteChannel, ActionFreezeChannel,
	ActionTruncateChannel, ActionHideChannel, ActionMuteChannel,
	ActionCreateMessage, ActionUpdateOwnMessage, ActionUpdateAnyMessage,
	ActionDeleteOwnMessage, ActionDeleteAnyMessage, ActionPinMessage,
	ActionCreateReply, ActionQuoteMessage, ActionCreateReaction,
	ActionDeleteOwnReaction, ActionDeleteAnyReaction, ActionUploadAttachment,
	ActionDeleteOwnAttachment, ActionDeleteAnyAttachment, ActionUseCommands,
	ActionSendCustomEvent, ActionSendTypingEvent, ActionSendLinks,
	ActionSkipSlowMode, ActionSkipMessageModeration, ActionBanUser,
	ActionBanChannelMember, ActionMuteUser, ActionFlagMessage, ActionFlagUser,
	ActionReadMessageFlags, ActionRunMessageAction, ActionMarkRead,
	ActionSendPoll, ActionCastPollVote, ActionUpdateOwnUser,
	ActionSearchMessages, ActionJoinChannel, ActionLeaveChannel,
	ActionCreateDistinctChannelForOthers, ActionUpdateChannelMember,
	ActionRemoveOwnChannelMembership, ActionConnectEvents, ActionReadEvents,
}

// capabilityNames maps actions to the kebab-case own_capabilities strings
// clients consume (SPEC.md §5.2 C17).
var capabilityNames = map[Action]string{
	ActionReadChannel:           "read-channel",
	ActionUpdateChannel:         "update-channel",
	ActionUpdateChannelFrozen:   "update-channel-frozen",
	ActionUpdateChannelCooldown: "set-channel-cooldown",
	ActionUpdateChannelMembers:  "update-channel-members",
	ActionDeleteChannel:         "delete-channel",
	ActionFreezeChannel:         "freeze-channel",
	ActionTruncateChannel:       "truncate-channel",
	ActionHideChannel:           "hide-channel",
	ActionMuteChannel:           "mute-channel",
	ActionCreateMessage:         "send-message",
	ActionUpdateOwnMessage:      "update-own-message",
	ActionUpdateAnyMessage:      "update-any-message",
	ActionDeleteOwnMessage:      "delete-own-message",
	ActionDeleteAnyMessage:      "delete-any-message",
	ActionPinMessage:            "pin-message",
	ActionCreateReply:           "send-reply",
	ActionQuoteMessage:          "quote-message",
	ActionCreateReaction:        "send-reaction",
	ActionDeleteOwnReaction:     "delete-own-reaction",
	ActionDeleteAnyReaction:     "delete-any-reaction",
	ActionUploadAttachment:      "upload-file",
	ActionUseCommands:           "use-commands",
	ActionSendCustomEvent:       "send-custom-events",
	ActionSendTypingEvent:       "send-typing-events",
	ActionSendLinks:             "send-links",
	ActionSkipSlowMode:          "skip-slow-mode",
	ActionBanUser:               "ban-user",
	ActionBanChannelMember:      "ban-channel-members",
	ActionMuteUser:              "mute-user",
	ActionFlagMessage:           "flag-message",
	ActionFlagUser:              "flag-user",
	ActionReadMessageFlags:      "read-message-flags",
	ActionRunMessageAction:      "run-message-action",
	ActionMarkRead:              "read-events",
	ActionSendPoll:              "send-poll",
	ActionCastPollVote:          "cast-poll-vote",
	ActionSearchMessages:        "search-messages",
	ActionJoinChannel:           "join-channel",
	ActionLeaveChannel:          "leave-channel",
	ActionConnectEvents:         "connect-events",
}

// Grant is one permission policy row: role is the map key in
// ChannelTypeConfig.Grants; Owner restricts the grant to resources the
// acting user owns; Allow=false is an explicit deny that overrides allows
// (SPEC.md §7.1).
type Grant struct {
	Action Action `json:"action"`
	Owner  bool   `json:"owner,omitempty"`
	Allow  bool   `json:"allow"`
}

// PermissionContext carries everything the engine needs for one check.
type PermissionContext struct {
	User    *User
	Member  *Member  // nil when the user is not a channel member
	Channel *Channel // nil for channel-creation checks
	Config  ChannelTypeConfig
	// Server is true for api_secret-signed (server-token) calls, which
	// bypass permission checks entirely (SPEC.md §10).
	Server bool
}

// effectiveRoles returns the roles to evaluate, most specific first:
// owner > channel_moderator > channel_member > global role.
func (pc PermissionContext) effectiveRoles() []string {
	var roles []string
	if pc.Channel != nil && pc.User != nil && pc.Channel.CreatedByID == pc.User.ID {
		roles = append(roles, ChannelRoleOwner)
	}
	if pc.Member != nil {
		if pc.Member.ChannelRole == ChannelRoleModerator {
			roles = append(roles, ChannelRoleModerator)
		}
		roles = append(roles, ChannelRoleMember)
	}
	if pc.User != nil && pc.User.Role != "" {
		roles = append(roles, pc.User.Role)
	}
	return roles
}

// Allowed evaluates the permission policy for action. ownsResource must be
// true when the target (message, reaction, attachment) belongs to the acting
// user; owner-scoped grants only match then. Explicit deny at a more
// specific level overrides allows at less specific levels; the default is
// deny.
func Allowed(pc PermissionContext, action Action, ownsResource bool) bool {
	if pc.Server {
		return true
	}
	if pc.User == nil {
		return false
	}
	if pc.User.Role == RoleAdmin {
		return true
	}
	if pc.User.Banned {
		switch action {
		case ActionReadChannel, ActionMarkRead:
		default:
			return false
		}
	}
	for _, role := range pc.effectiveRoles() {
		decided, allow := evaluateRole(pc.Config.Grants[role], action, ownsResource)
		if decided {
			return allow
		}
	}
	return false
}

// evaluateRole scans one role's grants: owner-scoped rows are more specific
// than generic rows, and deny overrides allow at equal specificity.
func evaluateRole(grants []Grant, action Action, ownsResource bool) (decided, allow bool) {
	var ownerDecided, ownerAllow, genericDecided, genericAllow bool
	for _, g := range grants {
		if g.Action != action {
			continue
		}
		if g.Owner {
			if !ownsResource {
				continue
			}
			if !g.Allow {
				return true, false
			}
			ownerDecided, ownerAllow = true, true
		} else {
			if !g.Allow {
				genericDecided, genericAllow = true, false
				continue
			}
			if !genericDecided {
				genericDecided, genericAllow = true, true
			}
		}
	}
	if ownerDecided {
		return true, ownerAllow
	}
	return genericDecided, genericAllow
}

// featureFlagAllows gates actions behind channel-type feature flags
// (SPEC.md §6.1) and channel state (frozen).
func featureFlagAllows(pc PermissionContext, action Action) bool {
	cfg := pc.Config
	switch action {
	case ActionCreateReaction, ActionDeleteOwnReaction, ActionDeleteAnyReaction:
		if !cfg.Reactions {
			return false
		}
	case ActionCreateReply:
		if !cfg.Replies {
			return false
		}
	case ActionQuoteMessage:
		if !cfg.Quotes {
			return false
		}
	case ActionUploadAttachment:
		if !cfg.Uploads {
			return false
		}
	case ActionSearchMessages:
		if !cfg.Search {
			return false
		}
	case ActionMuteChannel:
		if !cfg.Mutes {
			return false
		}
	case ActionSendTypingEvent:
		if !cfg.TypingEvents {
			return false
		}
	case ActionSendCustomEvent:
		if !cfg.CustomEvents {
			return false
		}
	case ActionMarkRead, ActionReadEvents:
		if !cfg.ReadEvents {
			return false
		}
	case ActionSendPoll, ActionCastPollVote:
		if !cfg.Polls {
			return false
		}
	case ActionConnectEvents:
		if !cfg.ConnectEvents {
			return false
		}
	}
	if pc.Channel != nil && pc.Channel.Frozen {
		switch action {
		case ActionCreateMessage, ActionCreateReply, ActionCreateReaction,
			ActionSendTypingEvent, ActionSendCustomEvent, ActionUploadAttachment,
			ActionSendPoll, ActionCastPollVote:
			return false
		}
	}
	return true
}

// AllowedWithFlags combines the permission policy with channel-type feature
// flags; API mutations use this entry point.
func AllowedWithFlags(pc PermissionContext, action Action, ownsResource bool) bool {
	if pc.Server {
		return true
	}
	if !featureFlagAllows(pc, action) {
		return false
	}
	return Allowed(pc, action, ownsResource)
}

// ComputeOwnCapabilities returns the kebab-case capability list for the
// acting user on a channel (SPEC.md §5.2 C17) so UIs can render
// permission-aware without duplicating policy logic.
func ComputeOwnCapabilities(pc PermissionContext) []string {
	caps := make([]string, 0, len(capabilityNames))
	seen := make(map[string]struct{}, len(capabilityNames))
	for _, action := range AllActions {
		name := capabilityNames[action]
		if name == "" {
			continue
		}
		if !featureFlagAllows(pc, action) {
			continue
		}
		// Owner-scoped capabilities (update-own-message etc.) describe what
		// the user could do to their own resources.
		ownScoped := action == ActionUpdateOwnMessage || action == ActionDeleteOwnMessage ||
			action == ActionDeleteOwnReaction || action == ActionDeleteOwnAttachment
		if Allowed(pc, action, ownScoped) {
			if _, dup := seen[name]; !dup {
				seen[name] = struct{}{}
				caps = append(caps, name)
			}
		}
	}
	if pc.Config.TypingEvents {
		if _, ok := seen["typing-events"]; !ok && Allowed(pc, ActionSendTypingEvent, false) {
			caps = append(caps, "typing-events")
		}
	}
	return caps
}

// DefaultGrants is the built-in permission policy applied to new channel
// types (SPEC.md §7.1). Admins bypass the engine entirely.
func DefaultGrants() map[string][]Grant {
	allow := func(actions ...Action) []Grant {
		grants := make([]Grant, 0, len(actions))
		for _, a := range actions {
			grants = append(grants, Grant{Action: a, Allow: true})
		}
		return grants
	}
	allowOwn := func(actions ...Action) []Grant {
		grants := make([]Grant, 0, len(actions))
		for _, a := range actions {
			grants = append(grants, Grant{Action: a, Owner: true, Allow: true})
		}
		return grants
	}

	memberGrants := append(allow(
		ActionReadChannel, ActionCreateMessage, ActionCreateReply,
		ActionQuoteMessage, ActionCreateReaction, ActionUploadAttachment,
		ActionUseCommands, ActionSendTypingEvent, ActionSendCustomEvent,
		ActionSendLinks, ActionFlagMessage, ActionFlagUser, ActionMarkRead,
		ActionMuteChannel, ActionHideChannel, ActionSearchMessages,
		ActionCastPollVote, ActionSendPoll, ActionLeaveChannel,
		ActionRemoveOwnChannelMembership, ActionConnectEvents, ActionReadEvents,
		ActionRunMessageAction,
	), allowOwn(
		ActionUpdateOwnMessage, ActionDeleteOwnMessage,
		ActionDeleteOwnReaction, ActionDeleteOwnAttachment,
	)...)

	moderationGrants := allow(
		ActionUpdateAnyMessage, ActionDeleteAnyMessage, ActionDeleteAnyReaction,
		ActionDeleteAnyAttachment, ActionPinMessage, ActionBanChannelMember,
		ActionMuteUser, ActionReadMessageFlags, ActionSkipSlowMode,
		ActionFreezeChannel, ActionTruncateChannel, ActionUpdateChannel,
		ActionUpdateChannelFrozen, ActionUpdateChannelCooldown,
		ActionUpdateChannelMembers,
	)

	return map[string][]Grant{
		RoleUser: append(allow(
			ActionCreateChannel, ActionUpdateOwnUser, ActionSearchMessages,
			ActionFlagMessage, ActionFlagUser, ActionMuteUser,
		), allowOwn(ActionDeleteOwnMessage, ActionUpdateOwnMessage)...),
		RoleGuest:     allow(ActionReadChannel, ActionFlagMessage),
		RoleAnonymous: nil,
		RoleModerator: append(append([]Grant(nil), memberGrants...), append(moderationGrants,
			Grant{Action: ActionBanUser, Allow: true},
			Grant{Action: ActionSkipMessageModeration, Allow: true},
		)...),
		ChannelRoleMember:    memberGrants,
		ChannelRoleModerator: append(append([]Grant(nil), memberGrants...), moderationGrants...),
		ChannelRoleOwner: append(append([]Grant(nil), memberGrants...), allow(
			ActionUpdateChannel, ActionDeleteChannel, ActionUpdateChannelMembers,
			ActionFreezeChannel, ActionTruncateChannel, ActionUpdateChannelFrozen,
			ActionUpdateChannelCooldown, ActionPinMessage, ActionBanChannelMember,
			ActionSkipSlowMode,
		)...),
	}
}
