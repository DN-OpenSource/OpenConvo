package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

// Blocklist CRUD (server-only, SPEC.md §11.1).

func (s *Server) handleListBlocklists(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	lists, err := store.ListBlocklists(r.Context(), s.Store.Pool, rc.App.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if lists == nil {
		lists = []*domain.Blocklist{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"blocklists": lists})
}

func (s *Server) handleUpsertBlocklist(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req domain.Blocklist
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Name == "" || len(req.Words) == 0 {
		s.writeErr(w, r, apierror.Input("name and words required"))
		return
	}
	switch req.Mode {
	case "", "exact", "wildcard", "regex":
		if req.Mode == "" {
			req.Mode = "exact"
		}
	default:
		s.writeErr(w, r, apierror.Input("mode must be exact|wildcard|regex"))
		return
	}
	switch req.Behavior {
	case "", domain.AutomodBehaviorFlag, domain.AutomodBehaviorBlock, domain.AutomodBehaviorShadowBlock:
		if req.Behavior == "" {
			req.Behavior = domain.AutomodBehaviorFlag
		}
	default:
		s.writeErr(w, r, apierror.Input("behavior must be flag|block|shadow_block"))
		return
	}
	if err := store.UpsertBlocklist(r.Context(), s.Store.Pool, rc.App.ID, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"blocklist": req})
}

func (s *Server) handleGetBlocklist(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	list, err := store.GetBlocklist(r.Context(), s.Store.Pool, rc.App.ID, chi.URLParam(r, "name"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"blocklist": list})
}

func (s *Server) handleDeleteBlocklist(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := store.DeleteBlocklist(r.Context(), s.Store.Pool, rc.App.ID, chi.URLParam(r, "name")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// Channel type CRUD (server-only, SPEC.md §6, §9.1 Config).

func (s *Server) handleListChannelTypes(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	types, err := store.ListChannelTypes(r.Context(), s.Store.Pool, rc.App.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channel_types": types})
}

func (s *Server) handleGetChannelType(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cfg, err := store.GetChannelType(r.Context(), s.Store.Pool, rc.App.ID, chi.URLParam(r, "name"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleCreateChannelType(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cfg := domain.BuiltinChannelTypes()["messaging"] // sensible defaults
	cfg.Automod = domain.AutomodDisabled
	if err := decodeJSON(r, &cfg); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := domain.ValidateChannelType(cfg.Name); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	if cfg.Grants == nil {
		cfg.Grants = domain.DefaultGrants()
	}
	if err := store.CreateChannelType(r.Context(), s.Store.Pool, rc.App.ID, cfg); err != nil {
		s.writeErr(w, r, apierror.Input("%s", err))
		return
	}
	s.writeJSON(w, http.StatusCreated, cfg)
}

func (s *Server) handleUpdateChannelType(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	name := chi.URLParam(r, "name")
	existing, err := store.GetChannelType(r.Context(), s.Store.Pool, rc.App.ID, name)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	cfg := *existing
	if err := decodeJSON(r, &cfg); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cfg.Name = name
	if err := store.UpdateChannelType(r.Context(), s.Store.Pool, rc.App.ID, cfg); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleDeleteChannelType(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := store.DeleteChannelType(r.Context(), s.Store.Pool, rc.App.ID, chi.URLParam(r, "name")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// GET/PATCH /app — app settings (server-only).

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"app": rc.App})
}

func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if err := requireServer(rc); err != nil {
		s.writeErr(w, r, err)
		return
	}
	settings := rc.App.Settings
	if err := decodeJSON(r, &settings); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := store.UpdateAppSettings(r.Context(), s.Store.Pool, rc.App.ID, settings); err != nil {
		s.writeErr(w, r, err)
		return
	}
	// Invalidate the app cache so the change takes effect immediately.
	s.appCache.Delete(rc.App.APIKey)
	rc.App.Settings = settings
	s.writeJSON(w, http.StatusOK, map[string]any{"app": rc.App})
}
