package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

type channelUpdateRequest struct {
	Data             *channelData `json:"data"`
	AddMembers       []string     `json:"add_members"`
	RemoveMembers    []string     `json:"remove_members"`
	Invites          []string     `json:"invites"`
	AddModerators    []string     `json:"add_moderators"`
	DemoteModerators []string     `json:"demote_moderators"`
	AcceptInvite     bool         `json:"accept_invite"`
	RejectInvite     bool         `json:"reject_invite"`
	HideHistory      bool         `json:"hide_history"`
	Cooldown         *int         `json:"cooldown"`
	Frozen           *bool        `json:"frozen"`
}

// POST /channels/{type}/{id} — full update incl. member operations
// (SPEC.md §9.1 members block).
func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")

	var req channelUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	memberOps := len(req.AddMembers)+len(req.RemoveMembers)+len(req.Invites)+
		len(req.AddModerators)+len(req.DemoteModerators) > 0
	if memberOps {
		if err := cc.require(domain.ActionUpdateChannelMembers, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	dataChanges := req.Data != nil || req.Cooldown != nil || req.Frozen != nil
	if dataChanges {
		if err := cc.require(domain.ActionUpdateChannel, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if req.Frozen != nil {
		if err := cc.require(domain.ActionUpdateChannelFrozen, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if req.Cooldown != nil {
		if err := cc.require(domain.ActionUpdateChannelCooldown, false); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}

	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		channel := cc.channel
		frozenBefore := channel.Frozen

		if req.Data != nil {
			if err := domain.ValidateCustom(&domain.Channel{}, req.Data.Custom); err != nil {
				return apierror.Input("%s", err)
			}
			if req.Data.Custom != nil {
				channel.Custom = req.Data.Custom
			}
			if req.Data.Team != "" {
				channel.Team = req.Data.Team
			}
		}
		if req.Frozen != nil {
			channel.Frozen = *req.Frozen
		}
		if req.Cooldown != nil {
			if *req.Cooldown < 0 || *req.Cooldown > 120 {
				return apierror.Input("cooldown must be 0-120 seconds")
			}
			channel.Cooldown = *req.Cooldown
		}
		if dataChanges {
			updated, err := store.UpdateChannel(ctx, tx, rc.App.ID, channel)
			if err != nil {
				return err
			}
			cc.channel = updated
			ev := newEvent(domain.EventChannelUpdated, channelType, channelID)
			ev.User = rc.User
			ev.Channel = updated
			if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
				return err
			}
			if req.Frozen != nil && *req.Frozen != frozenBefore {
				evType := domain.EventChannelFrozen
				if !*req.Frozen {
					evType = domain.EventChannelUnfrozen
				}
				fev := newEvent(evType, channelType, channelID)
				fev.Channel = updated
				if err := s.emit(ctx, tx, rc.App.ID, fev); err != nil {
					return err
				}
			}
		}

		hideBefore := (*time.Time)(nil)
		if req.HideHistory {
			now := time.Now().UTC()
			hideBefore = &now
		}
		for _, uid := range dedupe(req.AddMembers) {
			if err := s.addMemberTx(ctx, tx, rc, cc, uid, false, hideBefore); err != nil {
				return err
			}
		}
		for _, uid := range dedupe(req.Invites) {
			if err := s.addMemberTx(ctx, tx, rc, cc, uid, true, hideBefore); err != nil {
				return err
			}
		}
		for _, uid := range dedupe(req.RemoveMembers) {
			removed, err := store.RemoveMember(ctx, tx, rc.App.ID, channelType, channelID, uid)
			if err != nil {
				return err
			}
			if !removed {
				continue
			}
			ev := newEvent(domain.EventMemberRemoved, channelType, channelID)
			ev.User = &domain.User{ID: uid}
			if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
				return err
			}
			nev := newEvent(domain.EventNotificationRemovedFromChannel, channelType, channelID)
			nev.User = &domain.User{ID: uid}
			nev.Channel = cc.channel
			if err := s.emit(ctx, tx, rc.App.ID, nev); err != nil {
				return err
			}
		}
		for _, uid := range dedupe(req.AddModerators) {
			if err := store.SetMemberChannelRole(ctx, tx, rc.App.ID, channelType, channelID, uid, domain.ChannelRoleModerator); err != nil {
				return err
			}
			if err := s.emitMemberUpdated(ctx, tx, rc, cc, uid); err != nil {
				return err
			}
		}
		for _, uid := range dedupe(req.DemoteModerators) {
			if err := store.SetMemberChannelRole(ctx, tx, rc.App.ID, channelType, channelID, uid, domain.ChannelRoleMember); err != nil {
				return err
			}
			if err := s.emitMemberUpdated(ctx, tx, rc, cc, uid); err != nil {
				return err
			}
		}

		if req.AcceptInvite || req.RejectInvite {
			if rc.User == nil {
				return apierror.Input("user context required to answer invites")
			}
			if req.AcceptInvite {
				if err := store.AcceptInvite(ctx, tx, rc.App.ID, channelType, channelID, rc.User.ID); err != nil {
					return err
				}
				ev := newEvent(domain.EventNotificationInviteAccepted, channelType, channelID)
				ev.User = rc.User
				ev.Channel = cc.channel
				if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
					return err
				}
			} else {
				if err := store.RejectInvite(ctx, tx, rc.App.ID, channelType, channelID, rc.User.ID); err != nil {
					return err
				}
				ev := newEvent(domain.EventNotificationInviteRejected, channelType, channelID)
				ev.User = rc.User
				ev.Channel = cc.channel
				if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
					return err
				}
			}
		}

		if memberOps {
			if _, err := store.RecountMembers(ctx, tx, rc.App.ID, channelType, channelID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	cc, err = s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	resp, err := s.buildChannelState(ctx, rc, cc, &channelQueryRequest{State: true})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) addMemberTx(ctx context.Context, tx store.Tx, rc *RequestContext, cc *channelCtx, uid string, invited bool, hideBefore *time.Time) error {
	if err := domain.ValidateUserID(uid); err != nil {
		return apierror.Input("%s", err)
	}
	if _, err := store.EnsureUser(ctx, tx, rc.App.ID, uid, ""); err != nil {
		return err
	}
	member := &domain.Member{UserID: uid, Invited: invited, HideMessagesBefore: hideBefore}
	saved, added, err := store.AddMember(ctx, tx, rc.App.ID, cc.channel.Type, cc.channel.ID, member)
	if err != nil {
		return err
	}
	if !added {
		return nil
	}
	ev := newEvent(domain.EventMemberAdded, cc.channel.Type, cc.channel.ID)
	ev.User = &domain.User{ID: uid}
	ev.Member = saved
	if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
		return err
	}
	notifType := domain.EventNotificationAddedToChannel
	if invited {
		notifType = domain.EventNotificationInvited
	}
	nev := newEvent(notifType, cc.channel.Type, cc.channel.ID)
	nev.User = &domain.User{ID: uid}
	nev.Channel = cc.channel
	nev.Member = saved
	return s.emit(ctx, tx, rc.App.ID, nev)
}

func (s *Server) emitMemberUpdated(ctx context.Context, tx store.Tx, rc *RequestContext, cc *channelCtx, uid string) error {
	member, err := store.GetMember(ctx, tx, rc.App.ID, cc.channel.Type, cc.channel.ID, uid)
	if err != nil {
		return err
	}
	ev := newEvent(domain.EventMemberUpdated, cc.channel.Type, cc.channel.ID)
	ev.User = &domain.User{ID: uid}
	ev.Member = member
	return s.emit(ctx, tx, rc.App.ID, ev)
}

// PATCH /channels/{type}/{id} — partial update (set/unset custom).
func (s *Server) handlePartialUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")

	var req struct {
		Set   map[string]any `json:"set"`
		Unset []string       `json:"unset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionUpdateChannel, false); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := domain.ValidateCustom(&domain.Channel{}, req.Set); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}

	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		channel := cc.channel
		if channel.Custom == nil {
			channel.Custom = map[string]any{}
		}
		for k, v := range req.Set {
			channel.Custom[k] = v
		}
		for _, k := range req.Unset {
			delete(channel.Custom, k)
		}
		updated, err := store.UpdateChannel(ctx, tx, rc.App.ID, channel)
		if err != nil {
			return err
		}
		cc.channel = updated
		ev := newEvent(domain.EventChannelUpdated, channelType, channelID)
		ev.User = rc.User
		ev.Channel = updated
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channel": cc.channel})
}

// DELETE /channels/{type}/{id} (?hard_delete=true is server-only).
func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	hard := r.URL.Query().Get("hard_delete") == "true"

	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionDeleteChannel, cc.channel.CreatedByID == rc.actorID()); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if hard && !rc.Server {
		s.writeErr(w, r, apierror.NotAllowed("hard delete requires a server token"))
		return
	}

	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		ev := newEvent(domain.EventChannelDeleted, channelType, channelID)
		ev.User = rc.User
		ev.Channel = cc.channel
		if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
			return err
		}
		if hard {
			return store.HardDeleteChannel(ctx, tx, rc.App.ID, channelType, channelID)
		}
		return store.SoftDeleteChannel(ctx, tx, rc.App.ID, channelType, channelID)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channel": cc.channel})
}

// POST /channels/{type}/{id}/truncate (SPEC.md §5.2 C12).
func (s *Server) handleTruncateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")

	var req struct {
		HardDelete    bool   `json:"hard_delete"`
		SkipPush      bool   `json:"skip_push"`
		SystemMessage string `json:"message"`
	}
	if err := decodeJSONOptional(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionTruncateChannel, false); err != nil {
		s.writeErr(w, r, err)
		return
	}

	var truncated *domain.Channel
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		truncated, err = store.TruncateChannel(ctx, tx, rc.App.ID, channelType, channelID, req.HardDelete)
		if err != nil {
			return err
		}
		ev := newEvent(domain.EventChannelTruncated, channelType, channelID)
		ev.User = rc.User
		ev.Channel = truncated
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channel": truncated})
}

// POST /channels/{type}/{id}/hide — per-user hide (SPEC.md §5.2 C11).
func (s *Server) handleHideChannel(w http.ResponseWriter, r *http.Request) {
	s.setChannelHidden(w, r, true)
}

// POST /channels/{type}/{id}/show — unhide.
func (s *Server) handleShowChannel(w http.ResponseWriter, r *http.Request) {
	s.setChannelHidden(w, r, false)
}

func (s *Server) setChannelHidden(w http.ResponseWriter, r *http.Request, hidden bool) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")

	var req struct {
		ClearHistory bool `json:"clear_history"`
	}
	if err := decodeJSONOptional(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if cc.member == nil {
		s.writeErr(w, r, apierror.NotAllowed("only members can hide/show channels"))
		return
	}
	if err := cc.require(domain.ActionHideChannel, true); err != nil {
		s.writeErr(w, r, err)
		return
	}
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.SetMemberHidden(ctx, tx, rc.App.ID, channelType, channelID, rc.User.ID, hidden, req.ClearHistory); err != nil {
			return err
		}
		evType := domain.EventChannelHidden
		if !hidden {
			evType = domain.EventChannelVisible
		}
		ev := newEvent(evType, channelType, channelID)
		ev.User = rc.User
		ev.Channel = cc.channel
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"hidden": hidden})
}

// POST /channels/{type}/{id}/stop-watching.
func (s *Server) handleStopWatching(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	var req struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := decodeJSONOptional(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.ConnectionID != "" && s.Realtime != nil {
		if s.Realtime.StopWatching(req.ConnectionID, cc.channel.CID) {
			count, _ := s.State.AdjustWatchers(ctx, rc.App.ID, cc.channel.CID, -1)
			if cc.cfg.ConnectEvents && rc.User != nil && !rc.User.Invisible {
				ev := newEvent(domain.EventUserWatchingStop, channelType, channelID)
				ev.User = rc.User
				ev.WatcherCount = count
				_ = s.publishEphemeral(ctx, rc.App.ID, ev)
			}
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"watching": false})
}

// POST /channels/{type}/{id}/mute + DELETE — per-user channel mute (C10).
func (s *Server) handleMuteChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	var req struct {
		Expiration int `json:"expiration"` // milliseconds, 0 = forever
	}
	if err := decodeJSONOptional(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionMuteChannel, true); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	var expires *time.Time
	if req.Expiration > 0 {
		t := time.Now().Add(time.Duration(req.Expiration) * time.Millisecond)
		expires = &t
	}
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.UpsertChannelMute(ctx, tx, rc.App.ID, rc.User.ID, channelType, channelID, expires); err != nil {
			return err
		}
		ev := newEvent(domain.EventNotificationChannelMutesUpdated, "", "")
		ev.User = rc.User
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channel_mute": map[string]any{"cid": cc.channel.CID, "expires": expires}})
}

// DELETE /channels/{type}/{id}/mute.
func (s *Server) handleUnmuteChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	err := s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.DeleteChannelMute(ctx, tx, rc.App.ID, rc.User.ID, channelType, channelID); err != nil {
			return err
		}
		ev := newEvent(domain.EventNotificationChannelMutesUpdated, "", "")
		ev.User = rc.User
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"unmuted": true})
}
