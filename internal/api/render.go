package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/store"
)

// maxBodyBytes caps request bodies (defense in depth; uploads use their own
// limits).
const maxBodyBytes = 1 << 20 // 1 MiB

// decodeJSON reads and decodes a request body.
func decodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apierror.New(apierror.CodePayloadTooBig, "request body exceeds %d bytes", maxBodyBytes)
		}
		if errors.Is(err, io.EOF) {
			return apierror.Input("request body required")
		}
		return apierror.Input("invalid JSON: %s", err)
	}
	return nil
}

// decodeJSONOptional tolerates an empty body.
func decodeJSONOptional(r *http.Request, dst any) error {
	err := decodeJSON(r, dst)
	var apiErr *apierror.Error
	if errors.As(err, &apiErr) && apiErr.Code == apierror.CodeInput && apiErr.Message == "request body required" {
		return nil
	}
	return err
}

// writeJSON renders a success payload.
func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		s.Log.Error("encode response", "error", err)
	}
}

// writeErr renders any error via the envelope, translating store.ErrNotFound
// and logging internals.
func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *apierror.Error
	switch {
	case errors.As(err, &apiErr):
	case errors.Is(err, store.ErrNotFound):
		apiErr = apierror.NotFound("resource not found")
	default:
		s.Log.Error("internal error", "path", r.URL.Path, "error", err)
		apiErr = apierror.Internal()
	}
	apierror.Write(w, apiErr)
}
