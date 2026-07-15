package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

type banRequest struct {
	TargetUserID string `json:"target_user_id"`
	Type         string `json:"type"` // optional channel scope
	ID           string `json:"id"`
	Timeout      int    `json:"timeout"` // minutes; 0 = permanent
	Reason       string `json:"reason"`
	Shadow       bool   `json:"shadow"`
	IPBan        bool   `json:"ip_ban"` // accepted, not yet enforced
}

// POST /moderation/ban — global or channel ban, timed, shadow (SPEC.md §11.3).
func (s *Server) handleBan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req banRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.TargetUserID == "" {
		s.writeErr(w, r, apierror.Input("target_user_id required"))
		return
	}
	if req.TargetUserID == rc.actorID() {
		s.writeErr(w, r, apierror.Input("cannot ban yourself"))
		return
	}

	channelScoped := req.Type != "" && req.ID != ""
	if channelScoped {
		cc, err := s.loadChannel(ctx, rc, req.Type, req.ID)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		if err := cc.require(domain.ActionBanChannelMember, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	} else if !rc.Server {
		// Global bans need the global BanUser action (admins/moderators).
		perm := domain.PermissionContext{User: rc.User, Config: domain.ChannelTypeConfig{Grants: domain.DefaultGrants()}}
		if !domain.Allowed(perm, domain.ActionBanUser, false) {
			s.writeErr(w, r, apierror.NotAllowed("BanUser requires moderator or admin role"))
			return
		}
	}

	var expires *time.Time
	if req.Timeout > 0 {
		t := time.Now().Add(time.Duration(req.Timeout) * time.Minute)
		expires = &t
	}
	err := s.Store.InTx(ctx, func(tx store.Tx) error {
		ban := store.BanRecord{
			TargetUserID: req.TargetUserID,
			ChannelType:  req.Type,
			ChannelID:    req.ID,
			BannedBy:     rc.actorID(),
			Reason:       req.Reason,
			Shadow:       req.Shadow,
			Expires:      expires,
		}
		if err := store.UpsertBan(ctx, tx, rc.App.ID, ban); err != nil {
			return err
		}
		if channelScoped {
			if err := store.SetMemberBan(ctx, tx, rc.App.ID, req.Type, req.ID, req.TargetUserID, !req.Shadow, req.Shadow, expires); err != nil {
				return err
			}
		} else if !req.Shadow {
			if err := store.SetUserBanned(ctx, tx, rc.App.ID, req.TargetUserID, true, expires); err != nil {
				return err
			}
		}
		if err := store.InsertAudit(ctx, tx, rc.App.ID, rc.actorID(), "ban", "user", req.TargetUserID, req.Reason,
			map[string]any{"shadow": req.Shadow, "channel_type": req.Type, "channel_id": req.ID, "timeout_minutes": req.Timeout}); err != nil {
			return err
		}
		if !req.Shadow {
			ev := newEvent(domain.EventUserBanned, req.Type, req.ID)
			ev.User = &domain.User{ID: req.TargetUserID}
			ev.Reason = req.Reason
			return s.emit(ctx, tx, rc.App.ID, ev)
		}
		return nil
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"banned": true})
}

// DELETE /moderation/ban — lift a ban.
func (s *Server) handleUnban(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	q := r.URL.Query()
	target := q.Get("target_user_id")
	channelType, channelID := q.Get("type"), q.Get("id")
	if target == "" {
		s.writeErr(w, r, apierror.Input("target_user_id required"))
		return
	}
	if channelType != "" && channelID != "" {
		cc, err := s.loadChannel(ctx, rc, channelType, channelID)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		if err := cc.require(domain.ActionBanChannelMember, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	} else if !rc.Server {
		perm := domain.PermissionContext{User: rc.User, Config: domain.ChannelTypeConfig{Grants: domain.DefaultGrants()}}
		if !domain.Allowed(perm, domain.ActionBanUser, false) {
			s.writeErr(w, r, apierror.NotAllowed("BanUser requires moderator or admin role"))
			return
		}
	}

	err := s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.DeleteBan(ctx, tx, rc.App.ID, target, channelType, channelID); err != nil {
			return err
		}
		if channelType != "" {
			if err := store.SetMemberBan(ctx, tx, rc.App.ID, channelType, channelID, target, false, false, nil); err != nil {
				return err
			}
		} else {
			if err := store.SetUserBanned(ctx, tx, rc.App.ID, target, false, nil); err != nil {
				return err
			}
		}
		if err := store.InsertAudit(ctx, tx, rc.App.ID, rc.actorID(), "unban", "user", target, "",
			map[string]any{"channel_type": channelType, "channel_id": channelID}); err != nil {
			return err
		}
		ev := newEvent(domain.EventUserUnbanned, channelType, channelID)
		ev.User = &domain.User{ID: target}
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"banned": false})
}

// GET /moderation/banned — query active bans.
func (s *Server) handleQueryBanned(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	q := r.URL.Query()
	channelType, channelID := q.Get("type"), q.Get("id")
	if channelType != "" && channelID != "" {
		cc, err := s.loadChannel(ctx, rc, channelType, channelID)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		if err := cc.require(domain.ActionBanChannelMember, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	} else if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, offset := paginationParams(q.Get, 25, 100)
	bans, err := store.QueryBans(ctx, s.Store.Pool, rc.App.ID, channelType, channelID, limit, offset)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]*domain.Ban, 0, len(bans))
	for _, b := range bans {
		ban := &domain.Ban{
			User:      &domain.User{ID: b.TargetUserID},
			Reason:    b.Reason,
			Shadow:    b.Shadow,
			Expires:   b.Expires,
			CreatedAt: b.CreatedAt,
		}
		if b.BannedBy != "" {
			ban.BannedBy = &domain.User{ID: b.BannedBy}
		}
		if b.ChannelType != "" {
			ban.CID = domain.CID(b.ChannelType, b.ChannelID)
		}
		out = append(out, ban)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"bans": out})
}

// POST /moderation/mute — mute a user (SPEC.md §11.3).
func (s *Server) handleMuteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		TargetID string `json:"target_id"`
		Timeout  int    `json:"timeout"` // minutes
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.TargetID == "" || rc.User == nil {
		s.writeErr(w, r, apierror.Input("target_id and user context required"))
		return
	}
	if req.TargetID == rc.User.ID {
		s.writeErr(w, r, apierror.Input("cannot mute yourself"))
		return
	}
	var expires *time.Time
	if req.Timeout > 0 {
		t := time.Now().Add(time.Duration(req.Timeout) * time.Minute)
		expires = &t
	}
	err := s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.UpsertMute(ctx, tx, rc.App.ID, rc.User.ID, req.TargetID, expires); err != nil {
			return err
		}
		if err := store.InsertAudit(ctx, tx, rc.App.ID, rc.User.ID, "mute", "user", req.TargetID, "",
			map[string]any{"timeout_minutes": req.Timeout}); err != nil {
			return err
		}
		ev := newEvent(domain.EventNotificationMutesUpdated, "", "")
		ev.User = rc.User
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	mutes, err := store.ListMutes(ctx, s.Store.Pool, rc.App.ID, rc.User.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"mutes": mutes})
}

// DELETE /moderation/mute — unmute.
func (s *Server) handleUnmuteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	target := r.URL.Query().Get("target_id")
	if target == "" || rc.User == nil {
		s.writeErr(w, r, apierror.Input("target_id and user context required"))
		return
	}
	err := s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.DeleteMute(ctx, tx, rc.App.ID, rc.User.ID, target); err != nil {
			return err
		}
		if err := store.InsertAudit(ctx, tx, rc.App.ID, rc.User.ID, "unmute", "user", target, "", nil); err != nil {
			return err
		}
		ev := newEvent(domain.EventNotificationMutesUpdated, "", "")
		ev.User = rc.User
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"muted": false})
}

// POST /moderation/flag — report a message or user (SPEC.md §11.4).
func (s *Server) handleFlag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		TargetMessageID string         `json:"target_message_id"`
		TargetUserID    string         `json:"target_user_id"`
		Reason          string         `json:"reason"`
		Custom          map[string]any `json:"custom"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if (req.TargetMessageID == "") == (req.TargetUserID == "") {
		s.writeErr(w, r, apierror.Input("exactly one of target_message_id or target_user_id required"))
		return
	}
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	if req.TargetMessageID != "" {
		msg, cc, err := s.getMessageChannel(ctx, rc, req.TargetMessageID)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		_ = msg
		if err := cc.require(domain.ActionFlagMessage, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	flag := &domain.Flag{
		CreatedByID:     rc.User.ID,
		TargetMessageID: req.TargetMessageID,
		TargetUserID:    req.TargetUserID,
		Reason:          req.Reason,
		Custom:          req.Custom,
	}
	var saved *domain.Flag
	err := s.Store.InTx(ctx, func(tx store.Tx) error {
		var err error
		saved, err = store.InsertFlag(ctx, tx, rc.App.ID, flag)
		if err != nil {
			return err
		}
		return store.InsertAudit(ctx, tx, rc.App.ID, rc.User.ID, "flag", flagTargetType(req.TargetMessageID),
			req.TargetMessageID+req.TargetUserID, req.Reason, nil)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"flag": saved})
}

func flagTargetType(messageID string) string {
	if messageID != "" {
		return "message"
	}
	return "user"
}

// POST /moderation/unflag — withdraw a report.
func (s *Server) handleUnflag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		TargetMessageID string `json:"target_message_id"`
		TargetUserID    string `json:"target_user_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	if err := store.DeleteFlag(ctx, s.Store.Pool, rc.App.ID, rc.User.ID, req.TargetMessageID, req.TargetUserID); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"unflagged": true})
}

// GET /moderation/queue — unreviewed flags (SPEC.md §11.4).
func (s *Server) handleFlagQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	if err := s.requireModerator(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, offset := paginationParams(r.URL.Query().Get, 25, 100)
	flags, err := store.ListFlagQueue(ctx, s.Store.Pool, rc.App.ID, limit, offset)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if flags == nil {
		flags = []*domain.Flag{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"flags": flags})
}

// POST /moderation/queue/{id}/review — approve/delete/ban decisions.
func (s *Server) handleReviewFlag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	if err := s.requireModerator(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	flagID := chi.URLParam(r, "id")
	var req struct {
		Result string `json:"result"` // approved | deleted | banned
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	switch req.Result {
	case "approved", "deleted", "banned":
	default:
		s.writeErr(w, r, apierror.Input("result must be approved|deleted|banned"))
		return
	}
	err := s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.ReviewFlag(ctx, tx, rc.App.ID, flagID, rc.actorID(), req.Result); err != nil {
			return err
		}
		return store.InsertAudit(ctx, tx, rc.App.ID, rc.actorID(), "review_flag", "flag", flagID, req.Result, nil)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"reviewed": true})
}

// GET /moderation/audit — audit log (server-only, SPEC.md §11.6).
func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, offset := paginationParams(r.URL.Query().Get, 50, 100)
	entries, err := store.ListAudit(ctx, s.Store.Pool, rc.App.ID, limit, offset)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"audit": entries})
}

// requireModerator admits server tokens, admins and global moderators.
func (s *Server) requireModerator(rc *RequestContext) error {
	if rc.Server {
		return nil
	}
	if rc.User != nil && (rc.User.Role == domain.RoleAdmin || rc.User.Role == domain.RoleModerator) {
		return nil
	}
	return apierror.NotAllowed("moderator or admin role required")
}
