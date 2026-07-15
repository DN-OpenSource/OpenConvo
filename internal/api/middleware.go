package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/auth"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/store"
)

const appCacheTTL = 10 * time.Second

// resolveApp looks up the app by api_key with a small in-process cache.
func (s *Server) resolveApp(r *http.Request) (*domain.App, error) {
	apiKey := r.URL.Query().Get("api_key")
	if apiKey == "" {
		apiKey = r.Header.Get("X-Api-Key")
	}
	if apiKey == "" {
		return nil, apierror.TokenInvalid("api_key required (query or X-Api-Key header)")
	}
	if cached, ok := s.appCache.Load(apiKey); ok {
		entry := cached.(appCacheEntry)
		if time.Now().Before(entry.expires) {
			return entry.app, nil
		}
		s.appCache.Delete(apiKey)
	}
	app, err := store.GetAppByKey(r.Context(), s.Store.Pool, apiKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apierror.TokenInvalid("unknown api_key")
		}
		return nil, err
	}
	s.appCache.Store(apiKey, appCacheEntry{app: app, expires: time.Now().Add(appCacheTTL)})
	return app, nil
}

// appOnlyMiddleware resolves the app without requiring a user token
// (guest-token issuance).
func (s *Server) appOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app, err := s.resolveApp(r)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withRequestContext(r.Context(), &RequestContext{App: app})))
	})
}

// bearerToken extracts the JWT: Stream-style raw Authorization header,
// standard Bearer form, or the authorization query parameter (WS connect).
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header != "" {
		return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	return r.URL.Query().Get("authorization")
}

// authMiddleware implements SPEC.md §10: resolve app -> verify JWT ->
// enforce revocation -> load/auto-create the user -> inject context.
// Server tokens may act on behalf of a user via the user_id parameter.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := s.authenticate(r)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withRequestContext(r.Context(), rc)))
	})
}

func (s *Server) authenticate(r *http.Request) (*RequestContext, error) {
	app, err := s.resolveApp(r)
	if err != nil {
		return nil, err
	}
	tokenString := bearerToken(r)
	if tokenString == "" {
		return nil, apierror.TokenInvalid("authorization token required")
	}

	devMode := app.Settings.DisableAuthChecks || s.Cfg.DisableAuthChecks
	var claims *auth.Claims
	if devMode {
		claims, err = auth.VerifyDev(tokenString)
	} else {
		claims, err = auth.Verify(app.APISecret, tokenString)
	}
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrTokenExpired):
			return nil, apierror.New(apierror.CodeTokenExpired, "token expired")
		default:
			return nil, apierror.TokenInvalid("token verification failed")
		}
	}

	rc := &RequestContext{App: app, Claims: claims, Server: claims.Server}

	actingUserID := claims.UserID
	if claims.Server {
		// on-behalf-of (SPEC.md §10): user_id query param or header.
		if onBehalf := r.URL.Query().Get("user_id"); onBehalf != "" {
			actingUserID = onBehalf
		} else if onBehalf := r.Header.Get("X-User-Id"); onBehalf != "" {
			actingUserID = onBehalf
		}
	}
	if actingUserID != "" {
		if err := domain.ValidateUserID(actingUserID); err != nil {
			return nil, apierror.Input("%s", err)
		}
		role := ""
		if claims.Role == domain.RoleGuest {
			role = domain.RoleGuest
		}
		user, err := store.EnsureUser(r.Context(), s.Store.Pool, app.ID, actingUserID, role)
		if err != nil {
			return nil, err
		}
		if user.DeactivatedAt != nil || user.DeletedAt != nil {
			return nil, apierror.NotAllowed("user is deactivated")
		}
		if !claims.Server {
			if err := auth.CheckRevocation(claims, app.Settings.RevokeTokensIssuedBefore, user.RevokeTokensIssuedBefore); err != nil {
				return nil, apierror.New(apierror.CodeTokenRevoked, "token revoked")
			}
		}
		rc.User = user
	}
	if rc.User == nil && !rc.Server {
		return nil, apierror.TokenInvalid("token carries no user_id")
	}
	return rc, nil
}

// AuthenticateWS implements realtime.Authenticator: WS upgrades carry
// api_key + authorization as query parameters (SPEC.md §8.1).
func (s *Server) AuthenticateWS(r *http.Request) (*domain.App, *domain.User, bool, error) {
	rc, err := s.authenticate(r)
	if err != nil {
		return nil, nil, false, err
	}
	return rc.App, rc.User, rc.Server, nil
}

// rateLimitMiddleware enforces per-app and per-user-write limits with
// X-RateLimit headers (SPEC.md §9.3).
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := reqCtx(r.Context())
		limit := s.Cfg.RateLimitAppPerMin
		key := "app:" + rc.App.ID
		if !rc.Server && isWrite(r.Method) {
			limit = s.Cfg.RateLimitUserWritesPerMin
			key = "user:" + rc.App.ID + ":" + rc.actorID()
		}
		allowed, remaining, reset, err := s.State.RateAllow(r.Context(), key, limit, time.Minute)
		if err != nil {
			// Rate-limit backend failure must not take the API down.
			s.Log.Warn("rate limiter unavailable", "error", err)
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if !allowed {
			s.writeErr(w, r, apierror.RateLimited("rate limit exceeded"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// logMiddleware emits one structured line per request.
func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.Log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}

// recoverMiddleware converts panics into the error envelope.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic", "path", r.URL.Path, "panic", rec)
				apierror.Write(w, apierror.Internal())
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware applies permissive CORS (per-app origin allowlists land
// with the dashboard config; SPEC.md §20).
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key, X-User-Id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireServer guards server-only endpoints (SPEC.md §9.1 Config block).
func requireServer(rc *RequestContext) error {
	if !rc.Server {
		return apierror.NotAllowed("server-side authentication required")
	}
	return nil
}
