package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/bus"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

// publishEphemeral sends an event straight to the bus, skipping the outbox
// and event log — typing and watching events are never persisted
// (SPEC.md §8.3).
func (s *Server) publishEphemeral(ctx context.Context, appID string, e *domain.Event) error {
	return s.Bus.Publish(ctx, bus.TopicFor(appID, e), e.Encode())
}

// POST /channels/{type}/{id}/read — mark read (SPEC.md §5.3 U14).
func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	var req struct {
		MessageID string `json:"message_id"`
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
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	if !cc.cfg.ReadEvents {
		s.writeErr(w, r, apierror.NotAllowed("read events disabled for channel type %q", channelType))
		return
	}
	if err := cc.require(domain.ActionMarkRead, true); err != nil {
		s.writeErr(w, r, err)
		return
	}
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.MarkRead(ctx, tx, rc.App.ID, channelType, channelID, rc.User.ID, req.MessageID); err != nil {
			return err
		}
		ev := newEvent(domain.EventMessageRead, channelType, channelID)
		ev.User = rc.User
		if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
			return err
		}
		total, channels, err := store.UnreadSummary(ctx, tx, rc.App.ID, rc.User.ID)
		if err != nil {
			return err
		}
		nev := newEvent(domain.EventNotificationMarkRead, channelType, channelID)
		nev.User = rc.User
		nev.TotalUnreadCount = total
		nev.UnreadChannels = len(channels)
		return s.emit(ctx, tx, rc.App.ID, nev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"event": domain.EventMessageRead})
}

// POST /channels/{type}/{id}/unread — mark unread from a message (U14).
func (s *Server) handleMarkUnread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	var req struct {
		MessageID string `json:"message_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.MessageID == "" {
		s.writeErr(w, r, apierror.Input("message_id required"))
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	if !cc.cfg.ReadEvents {
		s.writeErr(w, r, apierror.NotAllowed("read events disabled for channel type %q", channelType))
		return
	}
	var unread int
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		unread, err = store.MarkUnreadFrom(ctx, tx, rc.App.ID, channelType, channelID, rc.User.ID, req.MessageID)
		if err != nil {
			return err
		}
		ev := newEvent(domain.EventNotificationMarkUnread, channelType, channelID)
		ev.User = rc.User
		ev.UnreadMessages = unread
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"unread_messages": unread})
}

// POST /channels/{type}/{id}/event — typing + custom events (SPEC.md §8.2).
func (s *Server) handleSendEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	var req struct {
		Event *domain.Event `json:"event"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Event == nil || req.Event.Type == "" {
		s.writeErr(w, r, apierror.Input("event.type required"))
		return
	}
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	ev := newEvent(req.Event.Type, channelType, channelID)
	ev.User = rc.User
	ev.ParentID = req.Event.ParentID
	ev.Custom = req.Event.Custom

	switch req.Event.Type {
	case domain.EventTypingStart, domain.EventTypingStop:
		if err := cc.require(domain.ActionSendTypingEvent, true); err != nil {
			s.writeErr(w, r, err)
			return
		}
		// Typing is ephemeral: bus only, never persisted (SPEC.md §8.3).
		if err := s.publishEphemeral(ctx, rc.App.ID, ev); err != nil {
			s.writeErr(w, r, err)
			return
		}
	default:
		if err := cc.require(domain.ActionSendCustomEvent, true); err != nil {
			s.writeErr(w, r, err)
			return
		}
		err = s.Store.InTx(ctx, func(tx store.Tx) error {
			return s.emit(ctx, tx, rc.App.ID, ev)
		})
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"event": ev})
}

// POST /sync — replay missed channel events (SPEC.md §8.1).
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		LastSyncAt time.Time `json:"last_sync_at"`
		CIDs       []string  `json:"channel_cids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.LastSyncAt.IsZero() || len(req.CIDs) == 0 {
		s.writeErr(w, r, apierror.Input("last_sync_at and channel_cids required"))
		return
	}
	if len(req.CIDs) > 200 {
		s.writeErr(w, r, apierror.Input("at most 200 channel_cids"))
		return
	}
	if time.Since(req.LastSyncAt) > 7*24*time.Hour {
		s.writeErr(w, r, apierror.Input("last_sync_at is beyond the 7-day replay window"))
		return
	}
	// Authorize each channel (read access, team isolation).
	allowed := make([]string, 0, len(req.CIDs))
	for _, cid := range req.CIDs {
		channelType, channelID, err := domain.ParseCID(cid)
		if err != nil {
			s.writeErr(w, r, apierror.Input("%s", err))
			return
		}
		cc, err := s.loadChannel(ctx, rc, channelType, channelID)
		if err != nil {
			continue
		}
		if domain.AllowedWithFlags(cc.perm, domain.ActionReadChannel, false) {
			allowed = append(allowed, cid)
		}
	}
	events, err := store.ListEventsSince(ctx, s.Store.Pool, rc.App.ID, allowed, req.LastSyncAt, 500)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if events == nil {
		events = []*domain.Event{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
