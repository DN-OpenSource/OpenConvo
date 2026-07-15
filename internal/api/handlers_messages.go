package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/microcosm-cc/bluemonday"
	"github.com/oklog/ulid/v2"
	"github.com/yuin/goldmark"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/moderation"
	"github.com/openstream/openstream/internal/store"
)

var (
	markdown     = goldmark.New()
	htmlPolicy   = bluemonday.UGCPolicy()
	linkDetector = strings.Contains
)

// renderMarkdown produces sanitized HTML (SPEC.md §5.1 M3, §20).
func renderMarkdown(text string) string {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(text), &buf); err != nil {
		return ""
	}
	return htmlPolicy.Sanitize(buf.String())
}

type messageDTO struct {
	ID              string              `json:"id"`
	Text            string              `json:"text"`
	Type            string              `json:"type"`
	ParentID        string              `json:"parent_id"`
	ShowInChannel   bool                `json:"show_in_channel"`
	QuotedMessageID string              `json:"quoted_message_id"`
	MentionedUsers  []string            `json:"mentioned_users"`
	Attachments     []domain.Attachment `json:"attachments"`
	Silent          bool                `json:"silent"`
	Pinned          bool                `json:"pinned"`
	PinExpires      *time.Time          `json:"pin_expires"`
	Custom          map[string]any      `json:"-"`
}

func (d *messageDTO) UnmarshalJSON(data []byte) error {
	type alias messageDTO
	var a alias
	custom, err := unmarshalFlattenedDTO(data, &a, map[string]struct{}{
		"id": {}, "text": {}, "type": {}, "parent_id": {}, "show_in_channel": {},
		"quoted_message_id": {}, "mentioned_users": {}, "attachments": {},
		"silent": {}, "pinned": {}, "pin_expires": {},
	})
	if err != nil {
		return err
	}
	*d = messageDTO(a)
	d.Custom = custom
	return nil
}

// POST /channels/{type}/{id}/message — send (SPEC.md §5.1 M1, M19).
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")

	var req struct {
		Message *messageDTO `json:"message"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Message == nil {
		s.writeErr(w, r, apierror.Input("message required"))
		return
	}
	dto := req.Message

	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if cc.channel.Disabled {
		s.writeErr(w, r, apierror.NotAllowed("channel is disabled"))
		return
	}
	if err := cc.require(domain.ActionCreateMessage, true); err != nil {
		s.writeErr(w, r, err)
		return
	}

	senderID := rc.actorID()
	if senderID == "" {
		s.writeErr(w, r, apierror.Input("user context required to send messages"))
		return
	}

	// Validation (SPEC.md Prompt 9).
	if len(dto.Text) > cc.cfg.MaxMessageLength {
		s.writeErr(w, r, apierror.Input("message exceeds max length %d", cc.cfg.MaxMessageLength))
		return
	}
	if err := domain.ValidateCustom(&domain.Message{}, dto.Custom); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	msgType := dto.Type
	switch msgType {
	case "", domain.MessageTypeRegular:
		msgType = domain.MessageTypeRegular
	case domain.MessageTypeSystem:
		if !rc.Server {
			s.writeErr(w, r, apierror.NotAllowed("system messages require a server token"))
			return
		}
	default:
		s.writeErr(w, r, apierror.Input("unsupported message type %q", msgType))
		return
	}
	if len(dto.Attachments) > 0 {
		if err := cc.require(domain.ActionUploadAttachment, true); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if strings.Contains(dto.Text, "http://") || linkDetector(dto.Text, "https://") {
		if !domain.AllowedWithFlags(cc.perm, domain.ActionSendLinks, true) {
			s.writeErr(w, r, apierror.NotAllowed("SendLinks not allowed on %s", cc.channel.CID))
			return
		}
	}

	// Thread rules (M5): parent must exist, no nested threads.
	if dto.ParentID != "" {
		if err := cc.require(domain.ActionCreateReply, true); err != nil {
			s.writeErr(w, r, err)
			return
		}
		parent, err := store.GetMessage(ctx, s.Store.Pool, rc.App.ID, dto.ParentID)
		if err != nil {
			s.writeErr(w, r, apierror.Input("parent message does not exist"))
			return
		}
		if parent.ParentID != "" {
			s.writeErr(w, r, apierror.Input("nested threads are not supported"))
			return
		}
		if parent.CID != cc.channel.CID {
			s.writeErr(w, r, apierror.Input("parent message belongs to another channel"))
			return
		}
		msgType = domain.MessageTypeReply
	}
	if dto.QuotedMessageID != "" {
		if err := cc.require(domain.ActionQuoteMessage, true); err != nil {
			s.writeErr(w, r, err)
			return
		}
		quoted, err := store.GetMessage(ctx, s.Store.Pool, rc.App.ID, dto.QuotedMessageID)
		if err != nil || quoted.CID != cc.channel.CID {
			s.writeErr(w, r, apierror.Input("quoted message does not exist in this channel"))
			return
		}
	}

	// Ban / shadow-ban enforcement (SPEC.md §11.3).
	shadowed := false
	if ban, err := s.actorBan(ctx, rc, cc); err != nil {
		s.writeErr(w, r, err)
		return
	} else if ban != nil {
		if !ban.Shadow {
			s.writeErr(w, r, apierror.NotAllowed("you are banned from this channel"))
			return
		}
		shadowed = true
	}
	if cc.member != nil && cc.member.ShadowBanned {
		shadowed = true
	}

	// Slow mode (SPEC.md §5.2 C15).
	if cc.channel.Cooldown > 0 && !domain.AllowedWithFlags(cc.perm, domain.ActionSkipSlowMode, false) {
		key := fmt.Sprintf("cooldown:%s:%s:%s", rc.App.ID, cc.channel.CID, senderID)
		allowed, _, _, err := s.State.RateAllow(ctx, key, 1, time.Duration(cc.channel.Cooldown)*time.Second)
		if err == nil && !allowed {
			s.writeErr(w, r, apierror.New(apierror.CodeCooldown, "slow mode: wait %d seconds between messages", cc.channel.Cooldown))
			return
		}
	}

	// Automod pipeline (SPEC.md §11.2).
	msg := &domain.Message{
		ID:              dto.ID,
		ChannelType:     channelType,
		ChannelID:       channelID,
		UserID:          senderID,
		Text:            dto.Text,
		Type:            msgType,
		ParentID:        dto.ParentID,
		ShowInChannel:   dto.ShowInChannel,
		QuotedMessageID: dto.QuotedMessageID,
		Attachments:     dto.Attachments,
		Silent:          dto.Silent,
		Shadowed:        shadowed,
		Custom:          dto.Custom,
	}
	if msg.ID == "" {
		msg.ID = ulid.Make().String()
	}
	flagAfterSave := false
	if cc.cfg.Automod == domain.AutomodSimple && !rc.Server &&
		!domain.AllowedWithFlags(cc.perm, domain.ActionSkipMessageModeration, false) {
		decision := s.automodPipeline(ctx, rc.App.ID, cc.cfg).Check(ctx, rc.App.ID, msg)
		switch decision.Verdict {
		case moderation.VerdictBlock:
			s.writeErr(w, r, apierror.NotAllowed("message blocked by moderation: %s", decision.Reason))
			return
		case moderation.VerdictShadow:
			msg.Shadowed = true
		case moderation.VerdictFlag:
			flagAfterSave = true
		}
	}

	for _, uid := range dedupe(dto.MentionedUsers) {
		msg.MentionedUsers = append(msg.MentionedUsers, &domain.User{ID: uid})
	}
	msg.HTML = renderMarkdown(msg.Text)

	var saved *domain.Message
	var inserted bool
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		saved, inserted, err = store.InsertMessage(ctx, tx, rc.App.ID, msg)
		if err != nil {
			return err
		}
		if !inserted {
			return nil // idempotent duplicate: no side effects (M19)
		}
		bumpOrdering := msg.Type != domain.MessageTypeSystem || !cc.cfg.SkipLastMsgUpdateForSystemMsgs
		if bumpOrdering {
			if err := store.BumpLastMessageAt(ctx, tx, rc.App.ID, channelType, channelID, saved.CreatedAt); err != nil {
				return err
			}
		}
		if msg.ParentID != "" {
			if _, err := store.AdjustReplyCount(ctx, tx, rc.App.ID, msg.ParentID, 1); err != nil {
				return err
			}
		}
		if !msg.Silent && !msg.Shadowed && (msg.ParentID == "" || msg.ShowInChannel) {
			if err := store.IncrementUnread(ctx, tx, rc.App.ID, channelType, channelID, senderID); err != nil {
				return err
			}
		}
		unhidden, err := store.UnhideChannelForMembers(ctx, tx, rc.App.ID, channelType, channelID)
		if err != nil {
			return err
		}
		for _, uid := range unhidden {
			ev := newEvent(domain.EventChannelVisible, channelType, channelID)
			ev.User = &domain.User{ID: uid}
			ev.Channel = cc.channel
			if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
				return err
			}
		}
		if flagAfterSave {
			flag := &domain.Flag{CreatedByID: "automod", TargetMessageID: saved.ID, Reason: "automod"}
			if _, err := store.InsertFlag(ctx, tx, rc.App.ID, flag); err != nil {
				return err
			}
		}

		saved.User = rc.User
		ev := newEvent(domain.EventMessageNew, channelType, channelID)
		ev.User = rc.User
		ev.Message = saved
		ev.WatcherCount, _ = s.State.WatcherCount(ctx, rc.App.ID, cc.channel.CID)
		if err := s.emit(ctx, tx, rc.App.ID, ev); err != nil {
			return err
		}

		// notification.message_new for members (bounded fan-out; very large
		// channels rely on channel-scoped delivery + push workers).
		if !msg.Silent && !msg.Shadowed && cc.channel.MemberCount <= 100 {
			memberIDs, err := store.ListMemberIDs(ctx, tx, rc.App.ID, channelType, channelID)
			if err != nil {
				return err
			}
			for _, uid := range memberIDs {
				if uid == senderID {
					continue
				}
				nev := newEvent(domain.EventNotificationMessageNew, channelType, channelID)
				nev.User = &domain.User{ID: uid}
				nev.Message = saved
				nev.Channel = cc.channel
				if err := s.emit(ctx, tx, rc.App.ID, nev); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.enrichMessages(ctx, rc.App.ID, senderID, []*domain.Message{saved}); err != nil {
		s.writeErr(w, r, err)
		return
	}
	status := http.StatusCreated
	if !inserted {
		status = http.StatusOK
	}
	s.writeJSON(w, status, map[string]any{"message": saved})
}

// automodPipeline builds the middleware chain for a channel type.
func (s *Server) automodPipeline(ctx context.Context, appID string, cfg *domain.ChannelTypeConfig) *moderation.Pipeline {
	var stages []moderation.Middleware
	list := moderation.DefaultProfanityList()
	list.Behavior = cfg.AutomodBehavior
	if cfg.Blocklist != "" {
		if stored, err := store.GetBlocklist(ctx, s.Store.Pool, appID, cfg.Blocklist); err == nil {
			list = stored
		}
	}
	stages = append(stages, moderation.NewBlocklistMatcher(list))
	return moderation.NewPipeline(stages...)
}

// GET /messages/{id}.
func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request) {
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
	if msg.Shadowed && msg.UserID != rc.actorID() && !rc.Server {
		s.writeErr(w, r, apierror.NotFound("message %s does not exist", msg.ID))
		return
	}
	if err := s.enrichMessages(ctx, rc.App.ID, rc.actorID(), []*domain.Message{msg}); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"message": msg})
}

// POST /messages/{id} — full update (SPEC.md M1, M20).
func (s *Server) handleUpdateMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		Message *messageDTO `json:"message"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Message == nil {
		s.writeErr(w, r, apierror.Input("message required"))
		return
	}
	msg, cc, err := s.getMessageChannel(ctx, rc, chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	owns := msg.UserID == rc.actorID()
	action := domain.ActionUpdateOwnMessage
	if !owns {
		action = domain.ActionUpdateAnyMessage
	}
	if err := cc.require(action, owns); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if len(req.Message.Text) > cc.cfg.MaxMessageLength {
		s.writeErr(w, r, apierror.Input("message exceeds max length %d", cc.cfg.MaxMessageLength))
		return
	}
	if err := domain.ValidateCustom(&domain.Message{}, req.Message.Custom); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}

	pinChanged := req.Message.Pinned != msg.Pinned
	if pinChanged {
		if err := cc.require(domain.ActionPinMessage, owns); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}

	msg.Text = req.Message.Text
	msg.HTML = renderMarkdown(msg.Text)
	msg.Custom = req.Message.Custom
	if req.Message.Attachments != nil {
		msg.Attachments = req.Message.Attachments
	}
	msg.MentionedUsers = nil
	for _, uid := range dedupe(req.Message.MentionedUsers) {
		msg.MentionedUsers = append(msg.MentionedUsers, &domain.User{ID: uid})
	}
	if pinChanged {
		msg.Pinned = req.Message.Pinned
		if msg.Pinned {
			now := time.Now().UTC()
			msg.PinnedAt = &now
			msg.PinnedByID = rc.actorID()
			msg.PinExpires = req.Message.PinExpires
		} else {
			msg.PinnedAt, msg.PinnedByID, msg.PinExpires = nil, "", nil
		}
	}

	var saved *domain.Message
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		saved, err = store.UpdateMessage(ctx, tx, rc.App.ID, msg)
		if err != nil {
			return err
		}
		ev := newEvent(domain.EventMessageUpdated, msg.ChannelType, msg.ChannelID)
		ev.User = rc.User
		ev.Message = saved
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.enrichMessages(ctx, rc.App.ID, rc.actorID(), []*domain.Message{saved}); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"message": saved})
}

// PUT /messages/{id} — partial update (set/unset custom + text).
func (s *Server) handlePartialUpdateMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	var req struct {
		Set   map[string]any `json:"set"`
		Unset []string       `json:"unset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	msg, cc, err := s.getMessageChannel(ctx, rc, chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	owns := msg.UserID == rc.actorID()
	action := domain.ActionUpdateOwnMessage
	if !owns {
		action = domain.ActionUpdateAnyMessage
	}
	if err := cc.require(action, owns); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if msg.Custom == nil {
		msg.Custom = map[string]any{}
	}
	for k, v := range req.Set {
		if k == "text" {
			if text, ok := v.(string); ok {
				msg.Text = text
				msg.HTML = renderMarkdown(text)
			}
			continue
		}
		msg.Custom[k] = v
	}
	for _, k := range req.Unset {
		delete(msg.Custom, k)
	}
	if err := domain.ValidateCustom(&domain.Message{}, msg.Custom); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	var saved *domain.Message
	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		saved, err = store.UpdateMessage(ctx, tx, rc.App.ID, msg)
		if err != nil {
			return err
		}
		ev := newEvent(domain.EventMessageUpdated, msg.ChannelType, msg.ChannelID)
		ev.User = rc.User
		ev.Message = saved
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"message": saved})
}

// DELETE /messages/{id} (?hard=true server-only) — M1 soft/hard delete.
func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	hard := r.URL.Query().Get("hard") == "true"

	msg, cc, err := s.getMessageChannel(ctx, rc, chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	owns := msg.UserID == rc.actorID()
	action := domain.ActionDeleteOwnMessage
	if !owns {
		action = domain.ActionDeleteAnyMessage
	}
	if err := cc.require(action, owns); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if hard && !rc.Server {
		s.writeErr(w, r, apierror.NotAllowed("hard delete requires a server token"))
		return
	}

	err = s.Store.InTx(ctx, func(tx store.Tx) error {
		if msg.ParentID != "" && msg.DeletedAt == nil {
			if _, err := store.AdjustReplyCount(ctx, tx, rc.App.ID, msg.ParentID, -1); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if hard {
			if err := store.HardDeleteMessage(ctx, tx, rc.App.ID, msg.ID); err != nil {
				return err
			}
			msg.DeletedAt = ptrTime(time.Now().UTC())
			msg.Type = domain.MessageTypeDeleted
		} else {
			deleted, err := store.SoftDeleteMessage(ctx, tx, rc.App.ID, msg.ID)
			if err != nil {
				return err
			}
			msg = deleted
		}
		ev := newEvent(domain.EventMessageDeleted, msg.ChannelType, msg.ChannelID)
		ev.User = rc.User
		ev.Message = msg
		if hard {
			ev.Custom = map[string]any{"hard_delete": true}
		}
		return s.emit(ctx, tx, rc.App.ID, ev)
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"message": msg})
}

// GET /messages/{id}/replies — thread pagination (M5).
func (s *Server) handleThreadReplies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	parent, cc, err := s.getMessageChannel(ctx, rc, chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionReadChannel, false); err != nil {
		s.writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	limit, _ := paginationParams(q.Get, 25, 300)
	page := store.MessagePage{
		Limit:    limit,
		ParentID: parent.ID,
		ViewerID: rc.actorID(),
		IDLT:     q.Get("id_lt"),
		IDGT:     q.Get("id_gt"),
		IDLTE:    q.Get("id_lte"),
		IDGTE:    q.Get("id_gte"),
	}
	replies, err := store.ListMessages(ctx, s.Store.Pool, rc.App.ID, parent.ChannelType, parent.ChannelID, page)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.enrichMessages(ctx, rc.App.ID, rc.actorID(), replies); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if replies == nil {
		replies = []*domain.Message{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"messages": replies})
}

// GET /channels/{type}/{id}/messages?ids=a,b,c.
func (s *Server) handleGetChannelMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionReadChannel, false); err != nil {
		s.writeErr(w, r, err)
		return
	}
	idsParam := r.URL.Query().Get("ids")
	if idsParam == "" {
		s.writeErr(w, r, apierror.Input("ids query parameter required"))
		return
	}
	ids := dedupe(strings.Split(idsParam, ","))
	if len(ids) > 100 {
		s.writeErr(w, r, apierror.Input("at most 100 ids"))
		return
	}
	messages, err := store.GetMessagesByIDs(ctx, s.Store.Pool, rc.App.ID, channelType, channelID, ids)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	visible := messages[:0]
	for _, m := range messages {
		if m.Shadowed && m.UserID != rc.actorID() && !rc.Server {
			continue
		}
		visible = append(visible, m)
	}
	if err := s.enrichMessages(ctx, rc.App.ID, rc.actorID(), visible); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"messages": visible})
}

// GET /channels/{type}/{id}/pinned — pinned messages (M10).
func (s *Server) handlePinnedMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := reqCtx(ctx)
	channelType, channelID := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	cc, err := s.loadChannel(ctx, rc, channelType, channelID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := cc.require(domain.ActionReadChannel, false); err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, _ := paginationParams(r.URL.Query().Get, 25, 100)
	pinned, err := store.ListPinnedMessages(ctx, s.Store.Pool, rc.App.ID, channelType, channelID, limit)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.enrichMessages(ctx, rc.App.ID, rc.actorID(), pinned); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if pinned == nil {
		pinned = []*domain.Message{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"messages": pinned})
}

func ptrTime(t time.Time) *time.Time { return &t }
