package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

// channelCtx bundles everything a channel-scoped handler needs.
type channelCtx struct {
	channel *domain.Channel
	cfg     *domain.ChannelTypeConfig
	member  *domain.Member // nil when the actor is not a member
	perm    domain.PermissionContext
}

// loadChannel fetches channel + type config + the actor's membership and
// assembles the permission context. Team isolation (SPEC.md §7.2) is
// enforced here for every channel-scoped route.
func (s *Server) loadChannel(ctx context.Context, rc *RequestContext, channelType, channelID string) (*channelCtx, error) {
	cfg, err := store.GetChannelType(ctx, s.Store.Pool, rc.App.ID, channelType)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apierror.NotFound("channel type %q does not exist", channelType)
		}
		return nil, err
	}
	channel, err := store.GetChannel(ctx, s.Store.Pool, rc.App.ID, channelType, channelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apierror.NotFound("channel %s does not exist", domain.CID(channelType, channelID))
		}
		return nil, err
	}
	if channel.DeletedAt != nil {
		return nil, apierror.NotFound("channel %s was deleted", channel.CID)
	}
	if err := s.enforceTeam(rc, channel); err != nil {
		return nil, err
	}
	var member *domain.Member
	if rc.User != nil {
		m, err := store.GetMember(ctx, s.Store.Pool, rc.App.ID, channelType, channelID, rc.User.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		member = m
	}
	return &channelCtx{
		channel: channel,
		cfg:     cfg,
		member:  member,
		perm:    rc.permContext(channel, member, *cfg),
	}, nil
}

// enforceTeam applies multi-tenancy isolation: users only touch channels in
// their teams (server calls bypass; SPEC.md §7.2).
func (s *Server) enforceTeam(rc *RequestContext, channel *domain.Channel) error {
	if rc.Server || !rc.App.Settings.MultiTenantEnabled || channel.Team == "" {
		return nil
	}
	if rc.User == nil {
		return apierror.NotAllowed("channel belongs to a team")
	}
	for _, t := range rc.User.Teams {
		if t == channel.Team {
			return nil
		}
	}
	return apierror.NotAllowed("channel belongs to another team")
}

// require checks one permission and renders the standard denial.
func (cc *channelCtx) require(action domain.Action, ownsResource bool) error {
	if domain.AllowedWithFlags(cc.perm, action, ownsResource) {
		return nil
	}
	return apierror.NotAllowed("%s not allowed on %s", action, cc.channel.CID)
}

// memberBanned reports whether the actor is banned in this channel scope
// (channel ban or global ban, expiry-aware).
func (s *Server) actorBan(ctx context.Context, rc *RequestContext, cc *channelCtx) (*store.BanRecord, error) {
	if rc.Server || rc.User == nil {
		return nil, nil
	}
	return store.GetActiveBan(ctx, s.Store.Pool, rc.App.ID, rc.User.ID, cc.channel.Type, cc.channel.ID)
}

// hydrateUsers loads user objects for ids and returns a lookup that always
// resolves (missing users degrade to bare IDs, never nils).
func (s *Server) hydrateUsers(ctx context.Context, appID string, ids []string) (func(string) *domain.User, error) {
	users, err := store.GetUsers(ctx, s.Store.Pool, appID, dedupe(ids))
	if err != nil {
		return nil, err
	}
	return func(id string) *domain.User {
		if u, ok := users[id]; ok {
			return u
		}
		return &domain.User{ID: id}
	}, nil
}

func dedupe(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// enrichMessage attaches user objects (sender, mentioned, pinned_by) and the
// viewer's own reactions.
func (s *Server) enrichMessages(ctx context.Context, appID, viewerID string, msgs []*domain.Message) error {
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.UserID, m.PinnedByID)
		for _, u := range m.MentionedUsers {
			ids = append(ids, u.ID)
		}
	}
	lookup, err := s.hydrateUsers(ctx, appID, ids)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		m.User = lookup(m.UserID)
		if m.PinnedByID != "" {
			m.PinnedBy = lookup(m.PinnedByID)
		}
		for i, u := range m.MentionedUsers {
			m.MentionedUsers[i] = lookup(u.ID)
		}
		reactions, err := store.ListReactions(ctx, s.Store.Pool, appID, m.ID, 10, 0)
		if err != nil {
			return err
		}
		for _, r := range reactions {
			r.User = lookup(r.UserID)
		}
		m.LatestReactions = reactions
		if viewerID != "" {
			own, err := store.ListOwnReactions(ctx, s.Store.Pool, appID, m.ID, viewerID)
			if err != nil {
				return err
			}
			m.OwnReactions = own
		}
	}
	return nil
}

// getMessageChannel loads the channel context for a message-scoped route.
func (s *Server) getMessageChannel(ctx context.Context, rc *RequestContext, messageID string) (*domain.Message, *channelCtx, error) {
	msg, err := store.GetMessage(ctx, s.Store.Pool, rc.App.ID, messageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, apierror.NotFound("message %s does not exist", messageID)
		}
		return nil, nil, err
	}
	cc, err := s.loadChannel(ctx, rc, msg.ChannelType, msg.ChannelID)
	if err != nil {
		return nil, nil, err
	}
	return msg, cc, nil
}

// paginationParams reads limit/offset with bounds.
func paginationParams(get func(string) string, defLimit, maxLimit int) (limit, offset int) {
	limit = defLimit
	if v := get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit <= 0 || limit > maxLimit {
			limit = defLimit
		}
	}
	if v := get("offset"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &offset); err != nil || offset < 0 {
			offset = 0
		}
	}
	return limit, offset
}
