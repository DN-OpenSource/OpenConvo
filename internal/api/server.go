package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/openstream/openstream/internal/bus"
	"github.com/openstream/openstream/internal/config"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/state"
	"github.com/openstream/openstream/internal/store"
)

// WatchRegistrar lets the API service register watch intent with the
// realtime tier (same-process direct call in serve mode; SPEC.md §8.1).
type WatchRegistrar interface {
	// Watch subscribes an existing connection to a channel; it returns
	// false when the connection is unknown to this node.
	Watch(connectionID, cid string) bool
	StopWatching(connectionID, cid string) bool
}

// Server hosts the REST API.
type Server struct {
	Store *store.Store
	Bus   bus.Bus
	State state.State
	Cfg   config.Config
	Log   *slog.Logger
	// Realtime is optional; nil disables watch registration (API-only
	// process — watch intent then flows through the event bus consumers).
	Realtime WatchRegistrar

	appCache sync.Map // api_key -> appCacheEntry
}

type appCacheEntry struct {
	app     *domain.App
	expires time.Time
}

// Router builds the full HTTP handler.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(s.logMiddleware)
	r.Use(s.recoverMiddleware)
	r.Use(corsMiddleware)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", s.handleReady)
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/api/v1", func(r chi.Router) {
		// Guest token issuance authenticates with the api_key only.
		r.With(s.appOnlyMiddleware).Post("/guest", s.handleGuestToken)

		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware)
			r.Use(s.rateLimitMiddleware)

			// Users (SPEC.md §5.3)
			r.Post("/users", s.handleUpsertUsers)
			r.Patch("/users", s.handlePartialUpdateUsers)
			r.Post("/users/query", s.handleQueryUsers)
			r.Post("/users/{id}/deactivate", s.handleDeactivateUser)
			r.Post("/users/{id}/reactivate", s.handleReactivateUser)
			r.Delete("/users/{id}", s.handleDeleteUser)

			// Devices (SPEC.md §5.3 U11)
			r.Get("/devices", s.handleListDevices)
			r.Post("/devices", s.handleAddDevice)
			r.Delete("/devices", s.handleRemoveDevice)

			// Channels (SPEC.md §5.2, §9.1)
			r.Post("/channels", s.handleQueryChannels)
			r.Post("/channels/{type}/query", s.handleQueryDistinctChannel)
			r.Post("/channels/{type}/{id}/query", s.handleChannelQuery)
			r.Post("/channels/{type}/{id}", s.handleUpdateChannel)
			r.Patch("/channels/{type}/{id}", s.handlePartialUpdateChannel)
			r.Delete("/channels/{type}/{id}", s.handleDeleteChannel)
			r.Post("/channels/{type}/{id}/truncate", s.handleTruncateChannel)
			r.Post("/channels/{type}/{id}/hide", s.handleHideChannel)
			r.Post("/channels/{type}/{id}/show", s.handleShowChannel)
			r.Post("/channels/{type}/{id}/stop-watching", s.handleStopWatching)
			r.Post("/channels/{type}/{id}/read", s.handleMarkRead)
			r.Post("/channels/{type}/{id}/unread", s.handleMarkUnread)
			r.Post("/channels/{type}/{id}/event", s.handleSendEvent)
			r.Post("/channels/{type}/{id}/mute", s.handleMuteChannel)
			r.Delete("/channels/{type}/{id}/mute", s.handleUnmuteChannel)
			r.Get("/channels/{type}/{id}/messages", s.handleGetChannelMessages)
			r.Get("/channels/{type}/{id}/pinned", s.handlePinnedMessages)
			r.Post("/channels/{type}/{id}/message", s.handleSendMessage)
			r.Post("/members", s.handleQueryMembers)

			// Messages (SPEC.md §5.1)
			r.Get("/messages/{id}", s.handleGetMessage)
			r.Post("/messages/{id}", s.handleUpdateMessage)
			r.Put("/messages/{id}", s.handlePartialUpdateMessage)
			r.Delete("/messages/{id}", s.handleDeleteMessage)
			r.Get("/messages/{id}/replies", s.handleThreadReplies)
			r.Post("/messages/{id}/reaction", s.handleSendReaction)
			r.Delete("/messages/{id}/reaction/{reaction_type}", s.handleDeleteReaction)
			r.Get("/messages/{id}/reactions", s.handleListReactions)

			// Read state (SPEC.md §5.3 U14-U15)
			r.Get("/unread", s.handleUnreadSummary)
			r.Post("/sync", s.handleSync)

			// Moderation (SPEC.md §11)
			r.Post("/moderation/ban", s.handleBan)
			r.Delete("/moderation/ban", s.handleUnban)
			r.Get("/moderation/banned", s.handleQueryBanned)
			r.Post("/moderation/mute", s.handleMuteUser)
			r.Delete("/moderation/mute", s.handleUnmuteUser)
			r.Post("/moderation/flag", s.handleFlag)
			r.Post("/moderation/unflag", s.handleUnflag)
			r.Get("/moderation/queue", s.handleFlagQueue)
			r.Post("/moderation/queue/{id}/review", s.handleReviewFlag)
			r.Get("/moderation/audit", s.handleAuditLog)

			// Blocklists (server-only, SPEC.md §11.1)
			r.Get("/blocklists", s.handleListBlocklists)
			r.Post("/blocklists", s.handleUpsertBlocklist)
			r.Get("/blocklists/{name}", s.handleGetBlocklist)
			r.Delete("/blocklists/{name}", s.handleDeleteBlocklist)

			// Channel types & app config (server-only, SPEC.md §9.1 Config)
			r.Get("/channeltypes", s.handleListChannelTypes)
			r.Post("/channeltypes", s.handleCreateChannelType)
			r.Get("/channeltypes/{name}", s.handleGetChannelType)
			r.Put("/channeltypes/{name}", s.handleUpdateChannelType)
			r.Delete("/channeltypes/{name}", s.handleDeleteChannelType)
			r.Get("/app", s.handleGetApp)
			r.Patch("/app", s.handleUpdateApp)
		})
	})
	return r
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Pool.Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("db unavailable"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// newEvent stamps the envelope fields shared by all emitted events.
func newEvent(eventType string, channelType, channelID string) *domain.Event {
	e := &domain.Event{
		EventID:   ulid.Make().String(),
		Type:      eventType,
		CreatedAt: time.Now().UTC(),
	}
	if channelType != "" {
		e.ChannelType = channelType
		e.ChannelID = channelID
		e.CID = domain.CID(channelType, channelID)
	}
	return e
}

// emit stages events in the transactional outbox (SPEC.md §2.3). User-scoped
// events must carry the target user in Event.User.
func (s *Server) emit(ctx context.Context, q store.Querier, appID string, events ...*domain.Event) error {
	for _, e := range events {
		if err := store.InsertOutbox(ctx, q, appID, bus.TopicFor(appID, e), e); err != nil {
			return err
		}
	}
	return nil
}
