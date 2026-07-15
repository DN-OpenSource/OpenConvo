package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/auth"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
	"github.com/openstream/openstream/internal/store/filters"
)

var userFilterCompiler = &filters.Compiler{
	Fields: map[string]filters.Field{
		"id":          {Column: "users.id", Kind: filters.Text},
		"name":        {Column: "users.name", Kind: filters.Text},
		"role":        {Column: "users.role", Kind: filters.Text},
		"banned":      {Column: "users.banned", Kind: filters.Bool},
		"online":      {Column: "users.online", Kind: filters.Bool},
		"created_at":  {Column: "users.created_at", Kind: filters.Time},
		"updated_at":  {Column: "users.updated_at", Kind: filters.Time},
		"last_active": {Column: "users.last_active", Kind: filters.Time},
		"teams":       {Column: "users.teams", Kind: filters.TextArray},
	},
	CustomColumn: "users.custom",
}

var userSortColumns = map[string]string{
	"id": "users.id", "name": "users.name", "created_at": "users.created_at",
	"updated_at": "users.updated_at", "last_active": "users.last_active",
}

// POST /users — batch upsert (SPEC.md §5.3 U1), ≤100 users.
func (s *Server) handleUpsertUsers(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	var req struct {
		Users map[string]*domain.User `json:"users"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if len(req.Users) == 0 || len(req.Users) > 100 {
		s.writeErr(w, r, apierror.Input("users must contain 1-100 entries"))
		return
	}
	// Role escalation requires a server token.
	for id, u := range req.Users {
		if u == nil {
			s.writeErr(w, r, apierror.Input("user %q is null", id))
			return
		}
		if u.ID == "" {
			u.ID = id
		}
		if err := domain.ValidateUserID(u.ID); err != nil {
			s.writeErr(w, r, apierror.Input("%s", err))
			return
		}
		if !rc.Server {
			if rc.User == nil || u.ID != rc.User.ID {
				s.writeErr(w, r, apierror.NotAllowed("client tokens may only upsert their own user"))
				return
			}
			if u.Role != "" && u.Role != rc.User.Role {
				s.writeErr(w, r, apierror.NotAllowed("role changes require a server token"))
				return
			}
			u.Role = rc.User.Role
		}
		if err := domain.ValidateCustom(&domain.User{}, u.Custom); err != nil {
			s.writeErr(w, r, apierror.Input("user %q: %s", id, err))
			return
		}
	}

	saved := make(map[string]*domain.User, len(req.Users))
	err := s.Store.InTx(r.Context(), func(tx store.Tx) error {
		for _, u := range req.Users {
			out, err := store.UpsertUser(r.Context(), tx, rc.App.ID, u)
			if err != nil {
				return err
			}
			saved[out.ID] = out
			ev := newEvent(domain.EventUserUpdated, "", "")
			ev.User = out
			if err := s.emit(r.Context(), tx, rc.App.ID, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"users": saved})
}

// PATCH /users — partial update with set/unset (SPEC.md §5.3 U2).
func (s *Server) handlePartialUpdateUsers(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	var req struct {
		Users []struct {
			ID    string         `json:"id"`
			Set   map[string]any `json:"set"`
			Unset []string       `json:"unset"`
		} `json:"users"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if len(req.Users) == 0 || len(req.Users) > 100 {
		s.writeErr(w, r, apierror.Input("users must contain 1-100 entries"))
		return
	}
	for _, u := range req.Users {
		if !rc.Server && (rc.User == nil || u.ID != rc.User.ID) {
			s.writeErr(w, r, apierror.NotAllowed("client tokens may only update their own user"))
			return
		}
		if _, roleChange := u.Set["role"]; roleChange && !rc.Server {
			s.writeErr(w, r, apierror.NotAllowed("role changes require a server token"))
			return
		}
	}

	saved := make(map[string]*domain.User, len(req.Users))
	err := s.Store.InTx(r.Context(), func(tx store.Tx) error {
		for _, u := range req.Users {
			out, err := store.PartialUpdateUser(r.Context(), tx, rc.App.ID, u.ID, u.Set, u.Unset)
			if err != nil {
				return err
			}
			saved[out.ID] = out
			ev := newEvent(domain.EventUserUpdated, "", "")
			ev.User = out
			if err := s.emit(r.Context(), tx, rc.App.ID, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": saved})
}

// POST /users/query — filter/sort/paginate (SPEC.md §5.3 U3).
func (s *Server) handleQueryUsers(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	var req struct {
		FilterConditions map[string]any `json:"filter_conditions"`
		Sort             []sortParam    `json:"sort"`
		Limit            int            `json:"limit"`
		Offset           int            `json:"offset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	whereSQL, args, err := userFilterCompiler.Compile(req.FilterConditions, nil)
	if err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	// Teams isolation: non-server callers only see users sharing a team
	// (SPEC.md §7.2).
	if rc.App.Settings.MultiTenantEnabled && !rc.Server && rc.User != nil {
		args = append(args, rc.User.Teams)
		whereSQL = "(" + whereSQL + ") AND users.teams && $" + itoa(len(args))
	}
	orderBy, err := buildOrderBy(req.Sort, userSortColumns, "users.created_at DESC")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	limit, offset := clampPage(req.Limit, req.Offset, 30, 100)
	users, err := store.QueryUsers(r.Context(), s.Store.Pool, rc.App.ID, whereSQL, args, orderBy, limit, offset)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if users == nil {
		users = []*domain.User{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// POST /users/{id}/deactivate (server-only, SPEC.md §5.3 U8).
func (s *Server) handleDeactivateUser(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if err := store.DeactivateUser(r.Context(), s.Store.Pool, rc.App.ID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	user, err := store.GetUser(r.Context(), s.Store.Pool, rc.App.ID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// POST /users/{id}/reactivate (server-only).
func (s *Server) handleReactivateUser(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if err := store.ReactivateUser(r.Context(), s.Store.Pool, rc.App.ID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	user, err := store.GetUser(r.Context(), s.Store.Pool, rc.App.ID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// DELETE /users/{id} (server-only; soft by default, SPEC.md §5.3 U9).
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if err := store.SoftDeleteUser(r.Context(), s.Store.Pool, rc.App.ID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// POST /guest — ephemeral guest user + token (SPEC.md §5.3 U6).
func (s *Server) handleGuestToken(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	var req struct {
		User struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.User.ID == "" {
		s.writeErr(w, r, apierror.Input("user.id required"))
		return
	}
	if err := domain.ValidateUserID(req.User.ID); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	user := &domain.User{ID: req.User.ID, Name: req.User.Name, Role: domain.RoleGuest}
	saved, err := store.UpsertUser(r.Context(), s.Store.Pool, rc.App.ID, user)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	token, err := auth.MintGuestToken(rc.App.APISecret, saved.ID, 24*time.Hour)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"user": saved, "access_token": token})
}

// GET /unread — aggregated unread summary (SPEC.md §5.3 U15).
func (s *Server) handleUnreadSummary(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	total, channels, err := store.UnreadSummary(r.Context(), s.Store.Pool, rc.App.ID, rc.User.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	list := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		list = append(list, map[string]any{
			"channel_id":   domain.CID(c.ChannelType, c.ChannelID),
			"unread_count": c.UnreadMessages,
			"last_read":    c.LastRead,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"total_unread_count": total,
		"unread_channels":    len(channels),
		"channels":           list,
	})
}
