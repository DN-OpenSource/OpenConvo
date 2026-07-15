// Package apierror implements the Stream-compatible error envelope and the
// stable numeric code registry (SPEC.md §9.3). All client-visible errors go
// through this package — handlers never write ad-hoc error bodies.
package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Stable numeric error codes (Stream-compatible registry).
const (
	CodeInput          = 4 // malformed request / validation failure
	CodeNotAllowed     = 5 // permission denied
	CodeDuplicate      = 6 // conflicting resource state
	CodeRateLimited    = 9
	CodeDoesNotExist   = 16
	CodeNotOwner       = 17
	CodePayloadTooBig  = 22
	CodeTokenExpired   = 40
	CodeTokenInvalid   = 41
	CodeTokenSignature = 42
	CodeTokenRevoked   = 43
	CodeCooldown       = 60 // slow mode active
	CodeInternal       = 500
)

var statusByCode = map[int]int{
	CodeInput:          http.StatusBadRequest,
	CodeNotAllowed:     http.StatusForbidden,
	CodeDuplicate:      http.StatusConflict,
	CodeRateLimited:    http.StatusTooManyRequests,
	CodeDoesNotExist:   http.StatusNotFound,
	CodeNotOwner:       http.StatusForbidden,
	CodePayloadTooBig:  http.StatusRequestEntityTooLarge,
	CodeTokenExpired:   http.StatusUnauthorized,
	CodeTokenInvalid:   http.StatusUnauthorized,
	CodeTokenSignature: http.StatusUnauthorized,
	CodeTokenRevoked:   http.StatusUnauthorized,
	CodeCooldown:       http.StatusTooManyRequests,
	CodeInternal:       http.StatusInternalServerError,
}

// Error is the wire error envelope (SPEC.md §9.3).
type Error struct {
	Code       int    `json:"code"`
	Message    string `json:"message"`
	StatusCode int    `json:"status_code"`
	MoreInfo   string `json:"more_info"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("api error %d (%d): %s", e.Code, e.StatusCode, e.Message)
}

// New builds an Error for a registry code.
func New(code int, format string, args ...any) *Error {
	status, ok := statusByCode[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	return &Error{
		Code:       code,
		Message:    fmt.Sprintf(format, args...),
		StatusCode: status,
		MoreInfo:   "https://github.com/openstream/openstream/blob/main/docs/errors.md",
	}
}

// Convenience constructors for the common classes.
func Input(format string, args ...any) *Error        { return New(CodeInput, format, args...) }
func NotAllowed(format string, args ...any) *Error   { return New(CodeNotAllowed, format, args...) }
func NotFound(format string, args ...any) *Error     { return New(CodeDoesNotExist, format, args...) }
func NotOwner(format string, args ...any) *Error     { return New(CodeNotOwner, format, args...) }
func RateLimited(format string, args ...any) *Error  { return New(CodeRateLimited, format, args...) }
func TokenInvalid(format string, args ...any) *Error { return New(CodeTokenInvalid, format, args...) }
func Internal() *Error                               { return New(CodeInternal, "internal server error") }

// Write renders any error as the envelope. Non-*Error values become an
// opaque internal error — internal details never leak to clients.
func Write(w http.ResponseWriter, err error) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		apiErr = Internal()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(apiErr.StatusCode)
	_ = json.NewEncoder(w).Encode(apiErr)
}
