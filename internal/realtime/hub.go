// Package realtime implements the WebSocket engine (SPEC.md §8): the
// /connect endpoint, the connection registry, interest-based bus
// subscriptions, presence edges with offline debounce, watcher management
// and per-connection outbound queues with slow-consumer disconnect.
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/oklog/ulid/v2"

	"github.com/openstream/openstream/internal/bus"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/state"
	"github.com/openstream/openstream/internal/store"
)

// Authenticator resolves a WS upgrade request to an app + user. The API
// package provides the implementation (shared token logic).
type Authenticator interface {
	AuthenticateWS(r *http.Request) (app *domain.App, user *domain.User, server bool, err error)
}

// Hub owns every local connection and its bus subscriptions.
type Hub struct {
	Store *store.Store
	Bus   bus.Bus
	State state.State
	Auth  Authenticator
	Log   *slog.Logger

	HeartbeatInterval time.Duration
	DeadTimeout       time.Duration
	PresenceDebounce  time.Duration

	mu sync.Mutex
	// conns by connection id.
	conns map[string]*conn
	// watchers maps "appID|cid" -> conn ids.
	watchers map[string]map[string]struct{}
	// userConns maps "appID|userID" -> conn ids.
	userConns map[string]map[string]struct{}
	// subs holds active bus subscriptions per topic with refcounts.
	subs map[string]*refSub
	// offlineTimers debounce presence-offline edges per "appID|userID".
	offlineTimers map[string]*time.Timer
}

type refSub struct {
	sub  bus.Subscription
	refs int
}

type conn struct {
	id     string
	appID  string
	user   *domain.User
	sock   *websocket.Conn
	out    chan []byte
	done   chan struct{}
	closed sync.Once

	mu      sync.Mutex
	watched map[string]struct{} // cids
}

// NewHub builds a hub with defaults from SPEC.md §8.1.
func NewHub(st *store.Store, b bus.Bus, ephemeral state.State, auth Authenticator, log *slog.Logger) *Hub {
	return &Hub{
		Store:             st,
		Bus:               b,
		State:             ephemeral,
		Auth:              auth,
		Log:               log,
		HeartbeatInterval: 25 * time.Second,
		DeadTimeout:       60 * time.Second,
		PresenceDebounce:  10 * time.Second,
		conns:             map[string]*conn{},
		watchers:          map[string]map[string]struct{}{},
		userConns:         map[string]map[string]struct{}{},
		subs:              map[string]*refSub{},
		offlineTimers:     map[string]*time.Timer{},
	}
}

// HandleConnect is the /connect WS endpoint (SPEC.md §8.1).
func (h *Hub) HandleConnect(w http.ResponseWriter, r *http.Request) {
	app, user, server, err := h.Auth.AuthenticateWS(r)
	if err != nil || (user == nil && !server) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if user == nil {
		// Server tokens must name a user to open a socket.
		http.Error(w, "user_id required for websocket connections", http.StatusBadRequest)
		return
	}
	sock, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"}, // CORS policy is enforced per app at the HTTP tier
	})
	if err != nil {
		return
	}

	c := &conn{
		id:      ulid.Make().String(),
		appID:   app.ID,
		user:    user,
		sock:    sock,
		out:     make(chan []byte, 256),
		done:    make(chan struct{}),
		watched: map[string]struct{}{},
	}
	ctx := context.WithoutCancel(r.Context())
	if err := h.register(ctx, c); err != nil {
		_ = sock.Close(websocket.StatusInternalError, "registration failed")
		return
	}

	hello, err := h.helloEvent(ctx, c)
	if err != nil {
		h.Log.Error("hello event", "error", err)
		h.unregister(ctx, c, websocket.StatusInternalError, "hello failed")
		return
	}
	c.enqueue(hello)

	go h.writeLoop(ctx, c)
	go h.heartbeatLoop(ctx, c)
	h.readLoop(ctx, c) // blocks until the connection dies
}

// register adds the connection to the registry, subscribes the user topic
// and handles the presence-online edge.
func (h *Hub) register(ctx context.Context, c *conn) error {
	userKey := c.appID + "|" + c.user.ID

	h.mu.Lock()
	h.conns[c.id] = c
	if h.userConns[userKey] == nil {
		h.userConns[userKey] = map[string]struct{}{}
	}
	h.userConns[userKey][c.id] = struct{}{}
	if timer, ok := h.offlineTimers[userKey]; ok {
		timer.Stop()
		delete(h.offlineTimers, userKey)
	}
	err := h.subscribeLocked(bus.UserTopic(c.appID, c.user.ID))
	h.mu.Unlock()
	if err != nil {
		return err
	}

	first, err := h.State.AddConnection(ctx, c.appID, c.user.ID, c.id)
	if err != nil {
		return err
	}
	if first && !c.user.Invisible {
		if err := store.SetUserOnline(ctx, h.Store.Pool, c.appID, c.user.ID, true); err != nil {
			h.Log.Warn("presence online", "error", err)
		}
		h.publishPresence(ctx, c.appID, c.user, true)
	}
	return nil
}

// unregister tears the connection down: watcher counts, presence debounce,
// topic unsubscription.
func (h *Hub) unregister(ctx context.Context, c *conn, code websocket.StatusCode, reason string) {
	c.closed.Do(func() {
		close(c.done)
		_ = c.sock.Close(code, reason)

		userKey := c.appID + "|" + c.user.ID
		c.mu.Lock()
		watched := make([]string, 0, len(c.watched))
		for cid := range c.watched {
			watched = append(watched, cid)
		}
		c.watched = map[string]struct{}{}
		c.mu.Unlock()

		h.mu.Lock()
		delete(h.conns, c.id)
		for _, cid := range watched {
			key := c.appID + "|" + cid
			if set, ok := h.watchers[key]; ok {
				delete(set, c.id)
				if len(set) == 0 {
					delete(h.watchers, key)
				}
			}
			channelType, channelID, err := domain.ParseCID(cid)
			if err == nil {
				h.unsubscribeLocked(bus.ChannelTopic(c.appID, channelType, channelID))
			}
		}
		if set, ok := h.userConns[userKey]; ok {
			delete(set, c.id)
			if len(set) == 0 {
				delete(h.userConns, userKey)
			}
		}
		h.unsubscribeLocked(bus.UserTopic(c.appID, c.user.ID))
		h.mu.Unlock()

		for _, cid := range watched {
			if _, err := h.State.AdjustWatchers(ctx, c.appID, cid, -1); err != nil {
				h.Log.Warn("watcher decrement", "error", err)
			}
		}

		last, err := h.State.RemoveConnection(ctx, c.appID, c.user.ID, c.id)
		if err != nil {
			h.Log.Warn("remove connection", "error", err)
			return
		}
		if last && !c.user.Invisible {
			h.scheduleOffline(ctx, c.appID, c.user)
		}
	})
}

// scheduleOffline debounces the offline edge (SPEC.md §8.1: 10s for
// reconnects).
func (h *Hub) scheduleOffline(ctx context.Context, appID string, user *domain.User) {
	userKey := appID + "|" + user.ID
	h.mu.Lock()
	defer h.mu.Unlock()
	if timer, ok := h.offlineTimers[userKey]; ok {
		timer.Stop()
	}
	h.offlineTimers[userKey] = time.AfterFunc(h.PresenceDebounce, func() {
		h.mu.Lock()
		delete(h.offlineTimers, userKey)
		h.mu.Unlock()
		online, err := h.State.OnlineUsers(ctx, appID, []string{user.ID})
		if err != nil || online[user.ID] {
			return // reconnected during the debounce window
		}
		if err := store.SetUserOnline(ctx, h.Store.Pool, appID, user.ID, false); err != nil {
			h.Log.Warn("presence offline", "error", err)
		}
		h.publishPresence(ctx, appID, user, false)
	})
}

func (h *Hub) publishPresence(ctx context.Context, appID string, user *domain.User, online bool) {
	u := *user
	u.Online = online
	now := time.Now().UTC()
	u.LastActive = &now
	e := &domain.Event{
		EventID:   ulid.Make().String(),
		Type:      domain.EventUserPresenceChanged,
		User:      &u,
		CreatedAt: now,
	}
	// Presence is delivered to channels the user is watched in; local
	// fan-out covers same-node watchers, the user topic covers their other
	// devices.
	if err := h.Bus.Publish(ctx, bus.UserTopic(appID, user.ID), e.Encode()); err != nil {
		h.Log.Warn("publish presence", "error", err)
	}
	h.deliverToWatchersOfUserChannels(appID, e)
}

// subscribeLocked adds a refcounted bus subscription (caller holds h.mu).
func (h *Hub) subscribeLocked(topic string) error {
	if rs, ok := h.subs[topic]; ok {
		rs.refs++
		return nil
	}
	sub, err := h.Bus.Subscribe(topic, "", h.onBusEvent)
	if err != nil {
		return err
	}
	h.subs[topic] = &refSub{sub: sub, refs: 1}
	return nil
}

func (h *Hub) unsubscribeLocked(topic string) {
	rs, ok := h.subs[topic]
	if !ok {
		return
	}
	rs.refs--
	if rs.refs <= 0 {
		_ = rs.sub.Unsubscribe()
		delete(h.subs, topic)
	}
}

// Watch implements api.WatchRegistrar (SPEC.md §5.2 C7).
func (h *Hub) Watch(connectionID, cid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[connectionID]
	if !ok {
		return false
	}
	c.mu.Lock()
	_, already := c.watched[cid]
	c.watched[cid] = struct{}{}
	c.mu.Unlock()
	if already {
		return false
	}
	key := c.appID + "|" + cid
	if h.watchers[key] == nil {
		h.watchers[key] = map[string]struct{}{}
	}
	h.watchers[key][connectionID] = struct{}{}
	channelType, channelID, err := domain.ParseCID(cid)
	if err == nil {
		if err := h.subscribeLocked(bus.ChannelTopic(c.appID, channelType, channelID)); err != nil {
			h.Log.Error("subscribe channel", "cid", cid, "error", err)
		}
	}
	return true
}

// StopWatching implements api.WatchRegistrar.
func (h *Hub) StopWatching(connectionID, cid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[connectionID]
	if !ok {
		return false
	}
	c.mu.Lock()
	_, watched := c.watched[cid]
	delete(c.watched, cid)
	c.mu.Unlock()
	if !watched {
		return false
	}
	key := c.appID + "|" + cid
	if set, ok := h.watchers[key]; ok {
		delete(set, connectionID)
		if len(set) == 0 {
			delete(h.watchers, key)
		}
	}
	channelType, channelID, err := domain.ParseCID(cid)
	if err == nil {
		h.unsubscribeLocked(bus.ChannelTopic(c.appID, channelType, channelID))
	}
	return true
}

// onBusEvent routes a bus message to local connections.
func (h *Hub) onBusEvent(topic string, payload []byte) {
	var e domain.Event
	if err := json.Unmarshal(payload, &e); err != nil {
		h.Log.Warn("decode bus event", "topic", topic, "error", err)
		return
	}
	appID, isUser, targetID := parseTopic(topic)
	if appID == "" {
		return
	}
	if isUser {
		h.deliverToUser(appID, targetID, &e, payload)
		return
	}
	h.deliverToWatchers(appID, e.CID, &e, payload)
}

func (h *Hub) deliverToUser(appID, userID string, e *domain.Event, payload []byte) {
	h.mu.Lock()
	set := h.userConns[appID+"|"+userID]
	targets := make([]*conn, 0, len(set))
	for id := range set {
		if c, ok := h.conns[id]; ok {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		h.send(c, e, payload)
	}
}

func (h *Hub) deliverToWatchers(appID, cid string, e *domain.Event, payload []byte) {
	h.mu.Lock()
	set := h.watchers[appID+"|"+cid]
	targets := make([]*conn, 0, len(set))
	for id := range set {
		if c, ok := h.conns[id]; ok {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		h.send(c, e, payload)
	}
}

// deliverToWatchersOfUserChannels fans a user event (presence) to local
// connections watching any channel — cheap approximation delivered
// best-effort; canonical presence lives in query responses.
func (h *Hub) deliverToWatchersOfUserChannels(appID string, e *domain.Event) {
	payload := e.Encode()
	h.mu.Lock()
	targets := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		if c.appID == appID && c.user.ID != e.User.ID {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		h.send(c, e, payload)
	}
}

// send enqueues with shadow filtering (SPEC.md §11.3: shadowed messages are
// visible only to their author).
func (h *Hub) send(c *conn, e *domain.Event, payload []byte) {
	if e.Message != nil && e.Message.Shadowed && (e.User == nil || e.User.ID != c.user.ID) {
		return
	}
	if !c.enqueue(payload) {
		// Slow consumer: outbound queue overflow (SPEC.md §8.3).
		h.Log.Warn("slow consumer dropped", "conn", c.id, "user", c.user.ID)
		go h.unregister(context.Background(), c, websocket.StatusPolicyViolation, "connection.slow")
	}
}

func (c *conn) enqueue(payload []byte) bool {
	select {
	case <-c.done:
		return true // already closing; drop silently
	default:
	}
	select {
	case c.out <- payload:
		return true
	default:
		return false
	}
}

func parseTopic(topic string) (appID string, isUser bool, targetID string) {
	// evt.{app}.{type}.{id} | usr.{app}.{user}
	parts := splitN(topic, '.', 4)
	if len(parts) < 3 {
		return "", false, ""
	}
	switch parts[0] {
	case "usr":
		return parts[1], true, parts[2]
	case "evt":
		return parts[1], false, ""
	}
	return "", false, ""
}

func splitN(s string, sep byte, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
