//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/openstream/openstream/internal/domain"
)

// wsClient is a minimal test WebSocket client.
type wsClient struct {
	t      *testing.T
	conn   *websocket.Conn
	connID string
	events chan *domain.Event
	cancel context.CancelFunc
}

func (e *env) connectWS(t *testing.T, userID string) *wsClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) +
		"/connect?api_key=" + e.app.APIKey + "&authorization=" + e.userToken(userID)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("ws dial: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	c := &wsClient{t: t, conn: conn, events: make(chan *domain.Event, 100), cancel: cancel}
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				close(c.events)
				return
			}
			var ev domain.Event
			if json.Unmarshal(data, &ev) == nil {
				c.events <- &ev
			}
		}
	}()

	// First frame must be health.check with connection_id (SPEC.md §8.1).
	hello := c.wait("health.check", 5*time.Second)
	if hello.ConnectionID == "" {
		t.Fatal("health.check missing connection_id")
	}
	c.connID = hello.ConnectionID
	t.Cleanup(func() {
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "test done")
	})
	return c
}

func (c *wsClient) wait(eventType string, timeout time.Duration) *domain.Event {
	c.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				c.t.Fatalf("connection closed while waiting for %s", eventType)
				return nil
			}
			if ev.Type == eventType {
				return ev
			}
		case <-deadline:
			c.t.Fatalf("event %s not received within %s", eventType, timeout)
			return nil
		}
	}
}

// expectNone asserts no event of a type arrives within the window.
func (c *wsClient) expectNone(eventType string, window time.Duration) {
	c.t.Helper()
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				return
			}
			if ev.Type == eventType {
				c.t.Fatalf("unexpected %s event: %+v", eventType, ev)
			}
		case <-deadline:
			return
		}
	}
}

func TestWebSocketHelloCarriesOwnUser(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	e.createChannel(alice, "messaging", "hello-state", []string{"bob"})
	e.sendMessage(alice, "messaging", "hello-state", "unread for bob", nil)

	// Reconnect bob: hello me payload must carry the unread count.
	bobWS := e.connectWS(t, "bob")
	_ = bobWS
	// The hello frame was already consumed in connectWS; reconnect to
	// inspect it directly.
	ctx := context.Background()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) +
		"/connect?api_key=" + e.app.APIKey + "&authorization=" + e.userToken("bob")
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
	conn.SetReadLimit(1 << 20)
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	me, _ := raw["me"].(map[string]any)
	if me == nil {
		t.Fatalf("hello has no me: %s", data)
	}
	if me["total_unread_count"].(float64) < 1 {
		t.Fatalf("own_user unread missing: %v", me)
	}
	if _, ok := me["devices"]; !ok {
		t.Fatalf("own_user devices missing: %v", me)
	}
}

func TestWebSocketMessageDelivery(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	e.createChannel(alice, "messaging", "ws-room", []string{"bob"})

	aliceWS := e.connectWS(t, "alice")
	bobWS := e.connectWS(t, "bob")

	// Both watch via channel query with connection_id (SPEC.md §5.2 C7).
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/ws-room/query", alice,
		map[string]any{"state": true, "watch": true, "connection_id": aliceWS.connID})
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/ws-room/query", e.userToken("bob"),
		map[string]any{"state": true, "watch": true, "connection_id": bobWS.connID})

	start := time.Now()
	e.sendMessage(alice, "messaging", "ws-room", "realtime hello", nil)

	for _, c := range []*wsClient{aliceWS, bobWS} {
		ev := c.wait("message.new", 2*time.Second)
		if ev.Message == nil || ev.Message.Text != "realtime hello" {
			t.Fatalf("message.new payload: %+v", ev.Message)
		}
		if ev.CID != "messaging:ws-room" {
			t.Fatalf("cid: %s", ev.CID)
		}
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("send->deliver took %s (budget 1.5s in tests)", elapsed)
	}

	// Typing events flow through without persistence (SPEC.md §8.3).
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/ws-room/event", alice,
		map[string]any{"event": map[string]any{"type": "typing.start"}})
	ev := bobWS.wait("typing.start", 2*time.Second)
	if ev.User == nil || ev.User.ID != "alice" {
		t.Fatalf("typing.start user: %+v", ev.User)
	}

	// Watcher count reflects both watchers.
	resp := e.mustOK(http.MethodPost, "/api/v1/channels/messaging/ws-room/query", alice,
		map[string]any{"state": true})
	if wc, _ := resp["watcher_count"].(float64); wc < 2 {
		t.Fatalf("watcher_count: %v", resp["watcher_count"])
	}

	// Stop watching: bob no longer receives channel events.
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/ws-room/stop-watching", e.userToken("bob"),
		map[string]any{"connection_id": bobWS.connID})
	e.sendMessage(alice, "messaging", "ws-room", "after stop-watch", nil)
	aliceWS.wait("message.new", 2*time.Second)
	bobWS.expectNone("message.new", 500*time.Millisecond)
}

func TestWebSocketShadowedInvisibleToOthers(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	server := e.serverToken()
	e.createChannel(alice, "messaging", "shadow-ws", []string{"troll"})

	aliceWS := e.connectWS(t, "alice")
	trollWS := e.connectWS(t, "troll")
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/shadow-ws/query", alice,
		map[string]any{"watch": true, "connection_id": aliceWS.connID})
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/shadow-ws/query", e.userToken("troll"),
		map[string]any{"watch": true, "connection_id": trollWS.connID})

	e.mustOK(http.MethodPost, "/api/v1/moderation/ban", server, map[string]any{
		"target_user_id": "troll", "type": "messaging", "id": "shadow-ws", "shadow": true,
	})
	e.sendMessage(e.userToken("troll"), "messaging", "shadow-ws", "shadowed", nil)

	// Author receives their own shadowed message; alice must not.
	trollWS.wait("message.new", 2*time.Second)
	aliceWS.expectNone("message.new", 500*time.Millisecond)
}

func TestSyncReplay(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	e.createChannel(alice, "messaging", "sync-room", []string{"bob"})

	since := time.Now().Add(-time.Second).UTC()
	e.sendMessage(alice, "messaging", "sync-room", "missed-1", nil)
	e.sendMessage(alice, "messaging", "sync-room", "missed-2", nil)

	resp := e.mustOK(http.MethodPost, "/api/v1/sync", e.userToken("bob"), map[string]any{
		"last_sync_at": since.Format(time.RFC3339),
		"channel_cids": []string{"messaging:sync-room"},
	})
	events := resp["events"].([]any)
	var newMessages int
	for _, raw := range events {
		if raw.(map[string]any)["type"] == "message.new" {
			newMessages++
		}
	}
	if newMessages != 2 {
		t.Fatalf("sync replay: expected 2 message.new, got %d (%d events)", newMessages, len(events))
	}

	// Users without read access get nothing.
	resp = e.mustOK(http.MethodPost, "/api/v1/sync", e.userToken("stranger"), map[string]any{
		"last_sync_at": since.Format(time.RFC3339),
		"channel_cids": []string{"messaging:sync-room"},
	})
	if len(resp["events"].([]any)) != 0 {
		t.Fatalf("stranger must not replay events: %v", resp)
	}
}

func TestPresenceEdges(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	e.createChannel(alice, "messaging", "presence-room", []string{"bob"})

	aliceWS := e.connectWS(t, "alice")
	_ = aliceWS

	// Bob connects: alice (another connection on the node) sees presence.
	bobWS := e.connectWS(t, "bob")
	ev := aliceWS.wait("user.presence.changed", 3*time.Second)
	if ev.User == nil || ev.User.ID != "bob" || !ev.User.Online {
		t.Fatalf("presence online event: %+v", ev.User)
	}

	// Presence flag in channel query reports bob online.
	resp := e.mustOK(http.MethodPost, "/api/v1/channels/messaging/presence-room/query", alice,
		map[string]any{"state": true, "presence": true})
	var bobOnline bool
	for _, raw := range resp["members"].([]any) {
		m := raw.(map[string]any)
		if m["user_id"] == "bob" {
			user := m["user"].(map[string]any)
			bobOnline, _ = user["online"].(bool)
		}
	}
	if !bobOnline {
		t.Fatal("bob must be online in presence query")
	}

	// Disconnect: after the debounce the offline edge fires.
	bobWS.cancel()
	_ = bobWS.conn.Close(websocket.StatusNormalClosure, "bye")
	ev = aliceWS.wait("user.presence.changed", 3*time.Second)
	if ev.User == nil || ev.User.ID != "bob" || ev.User.Online {
		t.Fatalf("presence offline event: %+v", ev.User)
	}
}
