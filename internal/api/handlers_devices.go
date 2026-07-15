package api

import (
	"net/http"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

var validPushProviders = map[string]bool{"firebase": true, "apn": true, "webpush": true}

// POST /devices — register a push device (SPEC.md §5.3 U11).
func (s *Server) handleAddDevice(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	var req struct {
		ID               string `json:"id"`
		PushProvider     string `json:"push_provider"`
		PushProviderName string `json:"push_provider_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.ID == "" || !validPushProviders[req.PushProvider] {
		s.writeErr(w, r, apierror.Input("id and push_provider (firebase|apn|webpush) required"))
		return
	}
	d := &domain.Device{ID: req.ID, PushProvider: req.PushProvider, PushProviderName: req.PushProviderName, UserID: rc.User.ID}
	if err := store.UpsertDevice(r.Context(), s.Store.Pool, rc.App.ID, d); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"device": d})
}

// GET /devices — list own devices.
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	devices, err := store.ListDevices(r.Context(), s.Store.Pool, rc.App.ID, rc.User.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if devices == nil {
		devices = []*domain.Device{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

// DELETE /devices?id=... — remove a device.
func (s *Server) handleRemoveDevice(w http.ResponseWriter, r *http.Request) {
	rc := reqCtx(r.Context())
	if rc.User == nil {
		s.writeErr(w, r, apierror.Input("user context required"))
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		s.writeErr(w, r, apierror.Input("id query parameter required"))
		return
	}
	if err := store.DeleteDevice(r.Context(), s.Store.Pool, rc.App.ID, rc.User.ID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
