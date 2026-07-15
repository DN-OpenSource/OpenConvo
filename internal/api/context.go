package api

import (
	"context"

	"github.com/openstream/openstream/internal/auth"
	"github.com/openstream/openstream/internal/domain"
)

type ctxKey int

const requestCtxKey ctxKey = iota

// RequestContext is the authenticated request state injected by the auth
// middleware.
type RequestContext struct {
	App *domain.App
	// User is the acting user (nil for pure server calls without
	// on-behalf-of).
	User   *domain.User
	Claims *auth.Claims
	// Server is true for api_secret-signed server tokens (SPEC.md §10).
	Server bool
}

// withRequestContext stores rc on ctx.
func withRequestContext(ctx context.Context, rc *RequestContext) context.Context {
	return context.WithValue(ctx, requestCtxKey, rc)
}

// reqCtx retrieves the RequestContext; handlers behind the auth middleware
// may assume it exists.
func reqCtx(ctx context.Context) *RequestContext {
	rc, _ := ctx.Value(requestCtxKey).(*RequestContext)
	return rc
}

// actorID returns the acting user id ("" for pure server calls).
func (rc *RequestContext) actorID() string {
	if rc.User != nil {
		return rc.User.ID
	}
	return ""
}

// permContext assembles the domain permission context for a channel.
func (rc *RequestContext) permContext(channel *domain.Channel, member *domain.Member, cfg domain.ChannelTypeConfig) domain.PermissionContext {
	return domain.PermissionContext{
		User:    rc.User,
		Member:  member,
		Channel: channel,
		Config:  cfg,
		Server:  rc.Server,
	}
}
