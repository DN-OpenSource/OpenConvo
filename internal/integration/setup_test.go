//go:build integration

// Package integration exercises the full stack — HTTP API → transactional
// outbox → relay → bus → realtime WebSocket delivery — against a real
// PostgreSQL. Requires OPENSTREAM_TEST_POSTGRES_DSN (tests skip otherwise).
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/openstream/openstream/internal/api"
	"github.com/openstream/openstream/internal/auth"
	"github.com/openstream/openstream/internal/bus"
	"github.com/openstream/openstream/internal/config"
	"github.com/openstream/openstream/internal/domain"
	"github.com/openstream/openstream/internal/realtime"
	"github.com/openstream/openstream/internal/state"
	"github.com/openstream/openstream/internal/store"
	"github.com/openstream/openstream/internal/worker"
)

var testDSN string

func TestMain(m *testing.M) {
	testDSN = os.Getenv("OPENSTREAM_TEST_POSTGRES_DSN")
	if testDSN == "" {
		fmt.Println("OPENSTREAM_TEST_POSTGRES_DSN not set; skipping integration tests")
		os.Exit(0)
	}
	// Fresh schema per run: down + up also proves reversibility.
	if err := store.MigrateDown(testDSN); err != nil {
		fmt.Println("migrate down:", err)
		os.Exit(1)
	}
	if err := store.MigrateUp(testDSN); err != nil {
		fmt.Println("migrate up:", err)
		os.Exit(1)
	}
	// Idempotency: a second up must be a no-op.
	if err := store.MigrateUp(testDSN); err != nil {
		fmt.Println("migrate up (idempotent):", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// env is one fully-wired test deployment.
type env struct {
	t      *testing.T
	store  *store.Store
	bus    *bus.InProc
	hub    *realtime.Hub
	http   *httptest.Server
	app    *domain.App
	cancel context.CancelFunc
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	st, err := store.New(ctx, testDSN)
	if err != nil {
		cancel()
		t.Fatalf("store: %v", err)
	}
	logSink := io.Discard
	if os.Getenv("TEST_VERBOSE") != "" {
		logSink = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logSink, nil))
	eventBus := bus.NewInProc()
	ephemeral := state.NewMemory()

	cfg := config.Default()
	apiServer := &api.Server{Store: st, Bus: eventBus, State: ephemeral, Cfg: cfg, Log: log}
	hub := realtime.NewHub(st, eventBus, ephemeral, apiServer, log)
	hub.HeartbeatInterval = 5 * time.Second
	hub.PresenceDebounce = 100 * time.Millisecond
	apiServer.Realtime = hub

	relay := &worker.Relay{Store: st, Bus: eventBus, Batch: 100, Interval: 20 * time.Millisecond, Log: log}
	go relay.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("/", apiServer.Router())
	mux.HandleFunc("/connect", hub.HandleConnect)
	srv := httptest.NewServer(mux)

	app, err := st.CreateApp(ctx, "test-app-"+time.Now().Format("150405.000000000"))
	if err != nil {
		srv.Close()
		cancel()
		t.Fatalf("create app: %v", err)
	}

	e := &env{t: t, store: st, bus: eventBus, hub: hub, http: srv, app: app, cancel: cancel}
	t.Cleanup(func() {
		srv.Close()
		cancel()
		_ = eventBus.Close()
		st.Close()
	})
	return e
}

func (e *env) userToken(userID string) string {
	tok, err := auth.MintUserToken(e.app.APISecret, userID, time.Hour)
	if err != nil {
		e.t.Fatalf("mint token: %v", err)
	}
	return tok
}

func (e *env) serverToken() string {
	tok, err := auth.MintServerToken(e.app.APISecret)
	if err != nil {
		e.t.Fatalf("mint server token: %v", err)
	}
	return tok
}

// do performs an authenticated JSON request and decodes the response.
func (e *env) do(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	sep := "?"
	if bytes.ContainsRune([]byte(path), '?') {
		sep = "&"
	}
	url := e.http.URL + path + sep + "api_key=" + e.app.APIKey
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, decoded
}

// mustOK asserts a 2xx and returns the body.
func (e *env) mustOK(method, path, token string, body any) map[string]any {
	e.t.Helper()
	status, decoded := e.do(method, path, token, body)
	if status < 200 || status > 299 {
		e.t.Fatalf("%s %s: status %d body %v", method, path, status, decoded)
	}
	return decoded
}

// createChannel provisions a channel with members via the query endpoint.
func (e *env) createChannel(token, channelType, channelID string, members []string) map[string]any {
	e.t.Helper()
	return e.mustOK(http.MethodPost, "/api/v1/channels/"+channelType+"/"+channelID+"/query", token,
		map[string]any{"data": map[string]any{"members": members}, "state": true})
}

// sendMessage posts a message and returns the message payload.
func (e *env) sendMessage(token, channelType, channelID, text string, extra map[string]any) map[string]any {
	e.t.Helper()
	msg := map[string]any{"text": text}
	for k, v := range extra {
		msg[k] = v
	}
	resp := e.mustOK(http.MethodPost, "/api/v1/channels/"+channelType+"/"+channelID+"/message", token,
		map[string]any{"message": msg})
	m, _ := resp["message"].(map[string]any)
	if m == nil {
		e.t.Fatalf("no message in response: %v", resp)
	}
	return m
}

// collectEvents subscribes directly to the bus for assertion of emitted
// events (the outbox relay publishes them).
func (e *env) collectEvents(pattern string) *eventCollector {
	c := &eventCollector{ch: make(chan *domain.Event, 100)}
	sub, err := e.bus.Subscribe(pattern, "", func(_ string, payload []byte) {
		var ev domain.Event
		if json.Unmarshal(payload, &ev) == nil {
			c.ch <- &ev
		}
	})
	if err != nil {
		e.t.Fatalf("subscribe: %v", err)
	}
	e.t.Cleanup(func() { _ = sub.Unsubscribe() })
	return c
}

type eventCollector struct {
	ch chan *domain.Event
}

// wait blocks for an event of the given type.
func (c *eventCollector) wait(t *testing.T, eventType string, timeout time.Duration) *domain.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-c.ch:
			if ev.Type == eventType {
				return ev
			}
		case <-deadline:
			t.Fatalf("event %s not received within %s", eventType, timeout)
			return nil
		}
	}
}
