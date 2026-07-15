package domain

import (
	"slices"
	"testing"
)

func msgCtx(role string, member *Member, channel *Channel, cfgMut func(*ChannelTypeConfig)) PermissionContext {
	cfg := baseConfig("messaging")
	if cfgMut != nil {
		cfgMut(&cfg)
	}
	if channel == nil {
		channel = &Channel{Type: "messaging", ID: "general", CreatedByID: "owner-user"}
	}
	return PermissionContext{
		User:    &User{ID: "actor", Role: role},
		Member:  member,
		Channel: channel,
		Config:  cfg,
	}
}

func TestPermissionMatrix(t *testing.T) {
	member := &Member{UserID: "actor", ChannelRole: ChannelRoleMember}
	channelMod := &Member{UserID: "actor", ChannelRole: ChannelRoleModerator}

	tests := []struct {
		name   string
		ctx    PermissionContext
		action Action
		owns   bool
		want   bool
	}{
		// admin bypasses everything
		{"admin any action", msgCtx(RoleAdmin, nil, nil, nil), ActionDeleteAnyMessage, false, true},
		{"admin frozen channel still allowed by policy engine", PermissionContext{User: &User{ID: "a", Role: RoleAdmin}, Config: baseConfig("messaging")}, ActionUpdateChannel, false, true},

		// server bypass
		{"server token", PermissionContext{Server: true}, ActionDeleteChannel, false, true},

		// plain user, not a member
		{"non-member cannot send", msgCtx(RoleUser, nil, nil, nil), ActionCreateMessage, false, false},
		{"non-member can create channel", msgCtx(RoleUser, nil, nil, nil), ActionCreateChannel, false, true},
		{"non-member can flag", msgCtx(RoleUser, nil, nil, nil), ActionFlagMessage, false, true},

		// member basics
		{"member can send", msgCtx(RoleUser, member, nil, nil), ActionCreateMessage, false, true},
		{"member can react", msgCtx(RoleUser, member, nil, nil), ActionCreateReaction, false, true},
		{"member can reply", msgCtx(RoleUser, member, nil, nil), ActionCreateReply, false, true},
		{"member can upload", msgCtx(RoleUser, member, nil, nil), ActionUploadAttachment, false, true},
		{"member updates own message", msgCtx(RoleUser, member, nil, nil), ActionUpdateOwnMessage, true, true},
		{"member cannot update others message", msgCtx(RoleUser, member, nil, nil), ActionUpdateOwnMessage, false, false},
		{"member cannot update any message", msgCtx(RoleUser, member, nil, nil), ActionUpdateAnyMessage, false, false},
		{"member deletes own message", msgCtx(RoleUser, member, nil, nil), ActionDeleteOwnMessage, true, true},
		{"member cannot delete any message", msgCtx(RoleUser, member, nil, nil), ActionDeleteAnyMessage, false, false},
		{"member cannot ban", msgCtx(RoleUser, member, nil, nil), ActionBanChannelMember, false, false},
		{"member cannot freeze", msgCtx(RoleUser, member, nil, nil), ActionFreezeChannel, false, false},
		{"member cannot skip slow mode", msgCtx(RoleUser, member, nil, nil), ActionSkipSlowMode, false, false},

		// channel moderator
		{"channel mod deletes any message", msgCtx(RoleUser, channelMod, nil, nil), ActionDeleteAnyMessage, false, true},
		{"channel mod pins", msgCtx(RoleUser, channelMod, nil, nil), ActionPinMessage, false, true},
		{"channel mod bans channel member", msgCtx(RoleUser, channelMod, nil, nil), ActionBanChannelMember, false, true},
		{"channel mod skips slow mode", msgCtx(RoleUser, channelMod, nil, nil), ActionSkipSlowMode, false, true},
		{"channel mod cannot delete channel", msgCtx(RoleUser, channelMod, nil, nil), ActionDeleteChannel, false, false},

		// global moderator
		{"global mod bans globally", msgCtx(RoleModerator, nil, nil, nil), ActionBanUser, false, true},
		{"global mod deletes any message", msgCtx(RoleModerator, nil, nil, nil), ActionDeleteAnyMessage, false, true},

		// owner (channel creator)
		{"owner updates channel", PermissionContext{
			User:    &User{ID: "owner-user", Role: RoleUser},
			Member:  &Member{UserID: "owner-user", ChannelRole: ChannelRoleMember},
			Channel: &Channel{Type: "messaging", ID: "general", CreatedByID: "owner-user"},
			Config:  baseConfig("messaging"),
		}, ActionUpdateChannel, false, true},
		{"owner deletes channel", PermissionContext{
			User:    &User{ID: "owner-user", Role: RoleUser},
			Member:  &Member{UserID: "owner-user", ChannelRole: ChannelRoleMember},
			Channel: &Channel{Type: "messaging", ID: "general", CreatedByID: "owner-user"},
			Config:  baseConfig("messaging"),
		}, ActionDeleteChannel, false, true},

		// guest / anonymous
		{"guest reads", msgCtx(RoleGuest, nil, nil, nil), ActionReadChannel, false, true},
		{"guest cannot send", msgCtx(RoleGuest, nil, nil, nil), ActionCreateMessage, false, false},
		{"anonymous cannot read messaging", msgCtx(RoleAnonymous, nil, nil, nil), ActionReadChannel, false, false},

		// banned users lose write access
		{"banned member cannot send", PermissionContext{
			User:    &User{ID: "actor", Role: RoleUser, Banned: true},
			Member:  member,
			Channel: &Channel{Type: "messaging", ID: "general"},
			Config:  baseConfig("messaging"),
		}, ActionCreateMessage, false, false},

		// explicit deny overrides allow
		{"deny grant overrides member allow", PermissionContext{
			User:   &User{ID: "actor", Role: RoleUser},
			Member: member,
			Config: func() ChannelTypeConfig {
				cfg := baseConfig("messaging")
				cfg.Grants[ChannelRoleMember] = append(cfg.Grants[ChannelRoleMember],
					Grant{Action: ActionCreateReaction, Allow: false})
				return cfg
			}(),
			Channel: &Channel{Type: "messaging", ID: "general"},
		}, ActionCreateReaction, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Allowed(tc.ctx, tc.action, tc.owns); got != tc.want {
				t.Fatalf("Allowed(%s, owns=%v) = %v, want %v", tc.action, tc.owns, got, tc.want)
			}
		})
	}
}

func TestFeatureFlagsGateActions(t *testing.T) {
	member := &Member{UserID: "actor", ChannelRole: ChannelRoleMember}

	noReactions := msgCtx(RoleUser, member, nil, func(c *ChannelTypeConfig) { c.Reactions = false })
	if AllowedWithFlags(noReactions, ActionCreateReaction, false) {
		t.Fatal("reactions=false must gate CreateReaction")
	}
	if !AllowedWithFlags(noReactions, ActionCreateMessage, false) {
		t.Fatal("reactions=false must not gate CreateMessage")
	}

	noReplies := msgCtx(RoleUser, member, nil, func(c *ChannelTypeConfig) { c.Replies = false })
	if AllowedWithFlags(noReplies, ActionCreateReply, false) {
		t.Fatal("replies=false must gate CreateReply")
	}

	noUploads := msgCtx(RoleUser, member, nil, func(c *ChannelTypeConfig) { c.Uploads = false })
	if AllowedWithFlags(noUploads, ActionUploadAttachment, false) {
		t.Fatal("uploads=false must gate UploadAttachment")
	}

	frozen := msgCtx(RoleUser, member, &Channel{Type: "messaging", ID: "g", Frozen: true}, nil)
	if AllowedWithFlags(frozen, ActionCreateMessage, false) {
		t.Fatal("frozen channel must gate CreateMessage")
	}
	if !AllowedWithFlags(frozen, ActionReadChannel, false) {
		t.Fatal("frozen channel must still allow reads")
	}

	// Server bypasses flags too.
	if !AllowedWithFlags(PermissionContext{Server: true, Config: noReactions.Config}, ActionCreateReaction, false) {
		t.Fatal("server must bypass feature flags")
	}
}

func TestComputeOwnCapabilities(t *testing.T) {
	member := &Member{UserID: "actor", ChannelRole: ChannelRoleMember}
	caps := ComputeOwnCapabilities(msgCtx(RoleUser, member, nil, nil))

	for _, want := range []string{"send-message", "send-reaction", "send-reply", "upload-file", "update-own-message", "delete-own-message", "read-events", "typing-events"} {
		if !slices.Contains(caps, want) {
			t.Errorf("expected capability %q, got %v", want, caps)
		}
	}
	for _, reject := range []string{"delete-any-message", "ban-channel-members", "freeze-channel"} {
		if slices.Contains(caps, reject) {
			t.Errorf("member must not have %q", reject)
		}
	}

	// Feature flag removes capability.
	noReactions := msgCtx(RoleUser, member, nil, func(c *ChannelTypeConfig) { c.Reactions = false })
	caps = ComputeOwnCapabilities(noReactions)
	if slices.Contains(caps, "send-reaction") {
		t.Error("reactions=false must remove send-reaction capability")
	}

	// Channel moderator gains moderation capabilities.
	mod := &Member{UserID: "actor", ChannelRole: ChannelRoleModerator}
	caps = ComputeOwnCapabilities(msgCtx(RoleUser, mod, nil, nil))
	for _, want := range []string{"delete-any-message", "pin-message", "ban-channel-members", "skip-slow-mode"} {
		if !slices.Contains(caps, want) {
			t.Errorf("channel moderator missing %q", want)
		}
	}
}

func TestBuiltinChannelTypes(t *testing.T) {
	types := BuiltinChannelTypes()
	for _, name := range []string{"messaging", "livestream", "team", "gaming", "commerce"} {
		if _, ok := types[name]; !ok {
			t.Fatalf("missing built-in type %q", name)
		}
	}
	if types["messaging"].Automod != AutomodSimple {
		t.Error("messaging should default to simple automod")
	}
	if types["livestream"].URLEnrichment {
		t.Error("livestream url_enrichment should be off")
	}
	if types["livestream"].ReadEvents {
		t.Error("livestream read_events should be off")
	}
	if types["gaming"].Uploads {
		t.Error("gaming uploads should be off")
	}
	if types["gaming"].ReadEvents {
		t.Error("gaming read_events should be off")
	}

	// livestream: any authenticated user can send; anonymous can read.
	ls := types["livestream"]
	sendCtx := PermissionContext{User: &User{ID: "u", Role: RoleUser}, Config: ls, Channel: &Channel{Type: "livestream", ID: "main"}}
	if !AllowedWithFlags(sendCtx, ActionCreateMessage, false) {
		t.Error("livestream should allow non-member user sends")
	}
	anonCtx := PermissionContext{User: &User{ID: "anon", Role: RoleAnonymous}, Config: ls, Channel: &Channel{Type: "livestream", ID: "main"}}
	if !Allowed(anonCtx, ActionReadChannel, false) {
		t.Error("livestream should allow anonymous reads")
	}
}

func TestRetentionDuration(t *testing.T) {
	cfg := baseConfig("messaging")
	if _, ok := cfg.RetentionDuration(); ok {
		t.Fatal("infinite retention must return ok=false")
	}
	cfg.MessageRetention = "720h"
	d, ok := cfg.RetentionDuration()
	if !ok || d.Hours() != 720 {
		t.Fatalf("RetentionDuration = %v %v", d, ok)
	}
	cfg.MessageRetention = "garbage"
	if _, ok := cfg.RetentionDuration(); ok {
		t.Fatal("invalid retention must return ok=false")
	}
}
