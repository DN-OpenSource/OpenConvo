package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

type reactionDTO struct {
	Type   string         `json:"type"`
	Score  int            `json:"score"`
	Custom map[string]any `json:"-"`
}

func (d *reactionDTO) UnmarshalJSON(data []byte) error {
	type alias reactionDTO
	var a alias
	custom, err := unmarshalFlattenedDTO(data, &a, map[string]struct{}{"type": {}, "score": {}})
	if err != nil {
		return err
	}
	*d = reactionDTO(a)
	d.Custom = custom
	return nil
}

// POST /messages/{id}/reaction (SPEC.md §5.1 M7).
func (s *Server) handleSendReaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		Reaction      *reactionDTO `json:"reaction"`
		EnforceUnique bool         `json:"enforce_unique"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Reaction == nil || req.Reaction.Type == "" {
		s.writeErr(w, r, apierror.Input("reaction.type required"))
		return
	}
	if req.Reaction.Score < 0 || req.Reaction.Score > 100 {
		s.writeErr(w, r, apierror.Input("reaction score must be 0-100"))
		return
	}
	if err := domain.ValidateCustom(&domain.Reaction{}, req.Reaction.Custom); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}

	msg, cc, err := s.getMessageChannel(ctx, rc, chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionCreateReaction, true); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}

	reaction := &domain.Reaction{
		MessageID: msg.ID,
		Type:      req.Reaction.Type,
		Score:     req.Reaction.Score,
		UserID:    rc.User.ID,
		Custom:    req.Reaction.Custom,
	}
	var saved *domain.Reaction
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.LockMessageReactions(ctx, tx, rc.App.ID, msg.ID); err != nil {
			return err
		}
		existing, err := store.ListOwnReactions(ctx, tx, rc.App.ID, msg.ID, rc.User.ID)
		if err != nil {
			return err
		}
		saved, err = store.UpsertReaction(ctx, tx, rc.App.ID, reaction, req.EnforceUnique)
		if err != nil {
			return err
		}
		counts, scores, err := store.AggregateReactions(ctx, tx, rc.App.ID, msg.ID)
		if err != nil {
			return err
		}
		if err := store.SetReactionDenorm(ctx, tx, rc.App.ID, msg.ID, counts, scores); err != nil {
			return err
		}
		msg.ReactionCounts = counts
		msg.ReactionScores = scores

		evType := domain.EventReactionNew
		for _, ex := range existing {
			if ex.Type == saved.Type {
				evType = domain.EventReactionUpdated
				break
			}
		}
		saved.User = rc.User
		ev := newEvent(evType, msg.ChannelType, msg.ChannelID)
		ev.User = rc.User
		ev.Message = msg
		ev.Reaction = saved
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.enrichMessages(ctx, rc.App.ID, rc.actorID(), []*domain.Message{msg}); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"message": msg, "reaction": saved})
}

// DELETE /messages/{id}/reaction/{reaction_type}.
func (s *Server) handleDeleteReaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	reactionType := chi.URLParam(r, "reaction_type")
	userID := rc.actorID()
	// Server tokens may remove another user's reaction via ?user_id=.
	if rc.Server {
		if uid := r.URL.Query().Get("user_id"); uid != "" {
			userID = uid
		}
	}
	if userID == "" {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}

	msg, cc, err := s.getMessageChannel(ctx, rc, chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	owns := userID == rc.actorID()
	action := domain.ActionDeleteOwnReaction
	if !owns {
		action = domain.ActionDeleteAnyReaction
	}
	if err := cc.require(action, owns); err != nil {
		s.writeErr(w, r, err)
		return
	}

	var deleted *domain.Reaction
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		if err := store.LockMessageReactions(ctx, tx, rc.App.ID, msg.ID); err != nil {
			return err
		}
		deleted, err = store.DeleteReaction(ctx, tx, rc.App.ID, msg.ID, userID, reactionType)
		if err != nil {
			return err
		}
		counts, scores, err := store.AggregateReactions(ctx, tx, rc.App.ID, msg.ID)
		if err != nil {
			return err
		}
		if err := store.SetReactionDenorm(ctx, tx, rc.App.ID, msg.ID, counts, scores); err != nil {
			return err
		}
		msg.ReactionCounts = counts
		msg.ReactionScores = scores

		ev := newEvent(domain.EventReactionDeleted, msg.ChannelType, msg.ChannelID)
		ev.User = rc.User
		ev.Message = msg
		ev.Reaction = deleted
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"message": msg, "reaction": deleted})
}

// GET /messages/{id}/reactions — paginated (M7).
func (s *Server) handleListReactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	msg, cc, err := s.getMessageChannel(ctx, rc, chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionReadChannel, false); err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, offset := paginationParams(r.URL.Query().Get, 25, 300)
	reactions, err := store.ListReactions(ctx, s.Store.Pool, rc.App.ID, msg.ID, limit, offset)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ids := make([]string, 0, len(reactions))
	for _, re := range reactions {
		ids = append(ids, re.UserID)
	}
	lookup, err := s.hydrateUsers(ctx, rc.App.ID, ids)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	for _, re := range reactions {
		re.User = lookup(re.UserID)
	}
	if reactions == nil {
		reactions = []*domain.Reaction{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"reactions": reactions})
}
