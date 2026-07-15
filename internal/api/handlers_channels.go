package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
	"github.com/openstream/openstream/internal/store/filters"
)

var channelFilterCompiler = &filters.Compiler{
	Fields: map[string]filters.Field{
		"type":            {Column: "channels.type", Kind: filters.Text},
		"id":              {Column: "channels.id", Kind: filters.Text},
		"cid":             {Column: "channels.cid", Kind: filters.Text},
		"team":            {Column: "channels.team", Kind: filters.Text},
		"created_by_id":   {Column: "channels.created_by", Kind: filters.Text},
		"frozen":          {Column: "channels.frozen", Kind: filters.Bool},
		"disabled":        {Column: "channels.disabled", Kind: filters.Bool},
		"member_count":    {Column: "channels.member_count", Kind: filters.Int},
		"created_at":      {Column: "channels.created_at", Kind: filters.Time},
		"updated_at":      {Column: "channels.updated_at", Kind: filters.Time},
		"last_message_at": {Column: "channels.last_message_at", Kind: filters.Time},
		"members":         {Kind: filters.MemberSubquery},
	},
	CustomColumn: "channels.custom",
	MemberSubquerySQL: `EXISTS (SELECT 1 FROM channel_members m
		WHERE m.app_id = channels.app_id AND m.channel_type = channels.type
		  AND m.channel_id = channels.id AND m.user_id = %s)`,
}

var channelSortColumns = map[string]string{
	"last_message_at": "channels.last_message_at",
	"created_at":      "channels.created_at",
	"updated_at":      "channels.updated_at",
	"member_count":    "channels.member_count",
	"cid":             "channels.cid",
}

type channelQueryRequest struct {
	Data         *channelData    `json:"data"`
	State        bool            `json:"state"`
	Watch        bool            `json:"watch"`
	Presence     bool            `json:"presence"`
	ConnectionID string          `json:"connection_id"`
	Messages     *messagePageDTO `json:"messages"`
	Members      *pageDTO        `json:"members"`
}

type channelData struct {
	Members []string       `json:"members"`
	Team    string         `json:"team"`
	Frozen  bool           `json:"frozen"`
	Custom  map[string]any `json:"-"`
}

func (d *channelData) UnmarshalJSON(data []byte) error {
	type alias channelData
	var a alias
	custom, err := unmarshalFlattenedDTO(data, &a, map[string]struct{}{
		"members": {}, "team": {}, "frozen": {},
	})
	if err != nil {
		return err
	}
	*d = channelData(a)
	d.Custom = custom
	return nil
}

type pageDTO struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type messagePageDTO struct {
	Limit           int        `json:"limit"`
	IDLT            string     `json:"id_lt"`
	IDGT            string     `json:"id_gt"`
	IDLTE           string     `json:"id_lte"`
	IDGTE           string     `json:"id_gte"`
	AroundID        string     `json:"id_around"`
	CreatedAtBefore *time.Time `json:"created_at_before"`
	CreatedAtAfter  *time.Time `json:"created_at_after"`
}

// POST /channels/{type}/{id}/query — create-or-get + state (SPEC.md §9.1).
func (s *Server) handleChannelQuery(w http.ResponseWriter, r *http.Request) {
	s.channelQuery(w, r, chi.URLParam(r, "type"), chi.URLParam(r, "id"))
}

// POST /channels/{type}/query — distinct channel by member list (C4).
func (s *Server) handleQueryDistinctChannel(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	channelType := chi.URLParam(r, "type")

	var req channelQueryRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Data == nil || len(req.Data.Members) == 0 {
		s.writeErr(w, r, apierror.Input("data.members required for distinct channels"))
		return
	}
	members := req.Data.Members
	if rc.User != nil {
		members = append(members, rc.User.ID)
	}
	s.channelQueryParsed(w, r, channelType, domain.DistinctChannelID(members), &req)
}

func (s *Server) channelQuery(w http.ResponseWriter, r *http.Request, channelType, channelID string) {
	var req channelQueryRequest
	if err := decodeJSONOptional(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.channelQueryParsed(w, r, channelType, channelID, &req)
}

func (s *Server) channelQueryParsed(w http.ResponseWriter, r *http.Request, channelType, channelID string, req *channelQueryRequest) {
	ctx := r.Context()
	rc := reqCtx(ctx)

	if err := domain.ValidateChannelType(channelType); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	if err := domain.ValidateChannelID(channelID); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	cfg, err := store.GetChannelType(ctx, s.Store.Pool, rc.App.ID, channelType)
	if err != nil {
		s.writeErr(w, r, apierror.NotFound("channel type %q does not exist", channelType))
		return
	}

	// Create-or-get inside one tx (members + events only on first create).
	var channel *domain.Channel
	var created bool
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		fresh := &domain.Channel{Type: channelType, ID: channelID, CreatedByID: rc.actorID()}
		if req.Data != nil {
			fresh.Team = req.Data.Team
			fresh.Frozen = req.Data.Frozen
			fresh.Custom = req.Data.Custom
			if err := domain.ValidateCustom(&domain.Channel{}, fresh.Custom); err != nil {
				return apierror.Input("%s", err)
			}
		}
		// Team on creation defaults to the creator's first team in
		// multi-tenant apps.
		if rc.App.Settings.MultiTenantEnabled && fresh.Team == "" && rc.User != nil && len(rc.User.Teams) > 0 {
			fresh.Team = rc.User.Teams[0]
		}
		permCtx := rc.permContext(nil, nil, *cfg)
		channel, created, err = store.CreateChannel(ctx, tx, rc.App.ID, fresh)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		if !domain.AllowedWithFlags(permCtx, domain.ActionCreateChannel, true) {
			return apierror.NotAllowed("CreateChannel not allowed")
		}

		memberIDs := []string{}
		if req.Data != nil {
			memberIDs = req.Data.Members
		}
		if rc.User != nil {
			memberIDs = append(memberIDs, rc.User.ID)
		}
		for _, uid := range dedupe(memberIDs) {
			if err := domain.ValidateUserID(uid); err != nil {
				return apierror.Input("%s", err)
			}
			if _, err := store.EnsureUser(ctx, tx, rc.App.ID, uid, ""); err != nil {
				return err
			}
			role := domain.ChannelRoleMember
			if uid == rc.actorID() {
				role = domain.ChannelRoleMember // creator's owner status derives from created_by
			}
			member := &domain.Member{UserID: uid, ChannelRole: role}
			if _, _, err := store.AddMember(ctx, tx, rc.App.ID, channelType, channelID, member); err != nil {
				return err
			}
			if uid != rc.actorID() {
				ev := newEvent(domain.EventNotificationAddedToChannel, channelType, channelID)
				ev.User = &domain.User{ID: uid}
				ev.Channel = channel
				if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
					return err
				}
			}
		}
		if _, err := store.RecountMembers(ctx, tx, rc.App.ID, channelType, channelID); err != nil {
			return err
		}
		ev := newEvent(domain.EventChannelCreated, channelType, channelID)
		ev.User = rc.User
		ev.Channel = channel
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionReadChannel, false); err != nil && !created {
		s.writeErr(w, r, err)
		return
	}

	resp, err := s.buildChannelState(ctx, rc, cc, req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	// Watch registration (SPEC.md §5.2 C7): requires an active connection.
	if req.Watch && req.ConnectionID != "" && s.Realtime != nil {
		if s.Realtime.Watch(req.ConnectionID, cc.channel.CID) {
			count, _ := s.State.AdjustWatchers(ctx, rc.App.ID, cc.channel.CID, 1)
			resp["watcher_count"] = count
			if cc.cfg.ConnectEvents && rc.User != nil && !rc.User.Invisible {
				ev := newEvent(domain.EventUserWatchingStart, channelType, channelID)
				ev.User = rc.User
				ev.WatcherCount = count
				_ = s.publishEphemeral(ctx, rc.App.ID, ev)
			}
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeJSON(w, status, resp)
}

// buildChannelState assembles the channel payload with config, capabilities,
// members, messages and reads (SPEC.md §9.1 state=true).
func (s *Server) buildChannelState(ctx context.Context, rc *RequestContext, cc *channelCtx, req *channelQueryRequest) (map[string]any, error) {
	channel := cc.channel
	channel.Config = cc.cfg
	channel.OwnCapabilities = domain.ComputeOwnCapabilities(cc.perm)

	resp := map[string]any{"channel": channel}
	if channel.CreatedByID != "" {
		lookup, err := s.hydrateUsers(ctx, rc.App.ID, []string{channel.CreatedByID})
		if err != nil {
			return nil, err
		}
		channel.CreatedBy = lookup(channel.CreatedByID)
	}
	if cc.member != nil {
		resp["membership"] = cc.member
	}

	if req == nil || !req.State {
		return resp, nil
	}

	memberLimit, memberOffset := 30, 0
	if req.Members != nil {
		memberLimit, memberOffset = clampPage(req.Members.Limit, req.Members.Offset, 30, 300)
	}
	members, err := store.ListMembers(ctx, s.Store.Pool, rc.App.ID, channel.Type, channel.ID, memberLimit, memberOffset)
	if err != nil {
		return nil, err
	}
	memberIDs := make([]string, 0, len(members))
	for _, m := range members {
		memberIDs = append(memberIDs, m.UserID)
	}
	lookup, err := s.hydrateUsers(ctx, rc.App.ID, memberIDs)
	if err != nil {
		return nil, err
	}
	var online map[string]bool
	if req.Presence {
		online, err = s.State.OnlineUsers(ctx, rc.App.ID, memberIDs)
		if err != nil {
			return nil, err
		}
	}
	for _, m := range members {
		m.User = lookup(m.UserID)
		if online != nil && online[m.UserID] && !m.User.Invisible {
			m.User.Online = true
		}
	}
	if members == nil {
		members = []*domain.Member{}
	}
	resp["members"] = members

	page := store.MessagePage{Limit: 25, ViewerID: rc.actorID()}
	if req.Messages != nil {
		page.Limit = req.Messages.Limit
		page.IDLT = req.Messages.IDLT
		page.IDGT = req.Messages.IDGT
		page.IDLTE = req.Messages.IDLTE
		page.IDGTE = req.Messages.IDGTE
		page.AroundID = req.Messages.AroundID
		page.CreatedAtBefore = req.Messages.CreatedAtBefore
		page.CreatedAtAfter = req.Messages.CreatedAtAfter
	}
	if cc.member != nil && cc.member.HideMessagesBefore != nil {
		page.HideMessagesBefore = cc.member.HideMessagesBefore
	}
	messages, err := store.ListMessages(ctx, s.Store.Pool, rc.App.ID, channel.Type, channel.ID, page)
	if err != nil {
		return nil, err
	}
	if err := s.enrichMessages(ctx, rc.App.ID, rc.actorID(), messages); err != nil {
		return nil, err
	}
	if messages == nil {
		messages = []*domain.Message{}
	}
	resp["messages"] = messages

	if cc.cfg.ReadEvents {
		states, err := store.GetChannelReads(ctx, s.Store.Pool, rc.App.ID, channel.Type, channel.ID)
		if err != nil {
			return nil, err
		}
		readUserIDs := make([]string, 0, len(states))
		for _, st := range states {
			readUserIDs = append(readUserIDs, st.UserID)
		}
		readLookupUsers, err := store.GetUsers(ctx, s.Store.Pool, rc.App.ID, readUserIDs)
		if err != nil {
			return nil, err
		}
		resp["read"] = store.ReadsToDomain(states, readLookupUsers)
	}

	count, err := s.State.WatcherCount(ctx, rc.App.ID, channel.CID)
	if err == nil {
		resp["watcher_count"] = count
	}
	return resp, nil
}

// POST /channels — query channels (SPEC.md §5.2 C8).
func (s *Server) handleQueryChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		FilterConditions map[string]any `json:"filter_conditions"`
		Sort             []sortParam    `json:"sort"`
		Limit            int            `json:"limit"`
		Offset           int            `json:"offset"`
		State            bool           `json:"state"`
		Presence         bool           `json:"presence"`
		MessageLimit     int            `json:"message_limit"`
		MemberLimit      int            `json:"member_limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	whereSQL, args, err := channelFilterCompiler.Compile(req.FilterConditions, nil)
	if err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	if rc.App.Settings.MultiTenantEnabled && !rc.Server && rc.User != nil {
		args = append(args, rc.User.Teams)
		whereSQL = "(" + whereSQL + ") AND (channels.team = '' OR channels.team = ANY($" + itoa(len(args)) + "))"
	}
	orderBy, err := buildOrderBy(req.Sort, channelSortColumns, "channels.last_message_at DESC NULLS LAST")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, offset := clampPage(req.Limit, req.Offset, 20, 30)
	channels, err := store.QueryChannels(ctx, s.Store.Pool, rc.App.ID, whereSQL, args, orderBy, limit, offset, rc.actorID())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	results := make([]map[string]any, 0, len(channels))
	for _, ch := range channels {
		cc, err := s.loadChannel(ctx, rc, ch.Type, ch.ID)
		if err != nil {
			continue // skip channels that disappear or fail team checks mid-query
		}
		if !domain.AllowedWithFlags(cc.perm, domain.ActionReadChannel, false) {
			continue
		}
		stateReq := &channelQueryRequest{
			State:    req.State,
			Presence: req.Presence,
			Messages: &messagePageDTO{Limit: req.MessageLimit},
			Members:  &pageDTO{Limit: req.MemberLimit},
		}
		payload, err := s.buildChannelState(ctx, rc, cc, stateReq)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		results = append(results, payload)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channels": results})
}

// POST /members — query members of one channel (SPEC.md §9.1).
func (s *Server) handleQueryMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cc, err := s.loadChannel(ctx, rc, req.Type, req.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionReadChannel, false); err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, offset := clampPage(req.Limit, req.Offset, 30, 300)
	members, err := store.ListMembers(ctx, s.Store.Pool, rc.App.ID, req.Type, req.ID, limit, offset)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserID)
	}
	lookup, err := s.hydrateUsers(ctx, rc.App.ID, ids)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	for _, m := range members {
		m.User = lookup(m.UserID)
	}
	if members == nil {
		members = []*domain.Member{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"members": members})
}
