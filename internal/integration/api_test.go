//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestAuthClasses(t *testing.T) {
	e := newEnv(t)

	// No token.
	status, body := e.do(http.MethodPost, "/api/v1/users/query", "", map[string]any{"filter_conditions": map[string]any{}})
	if status != http.StatusUnauthorized {
		t.Fatalf("no token: status %d %v", status, body)
	}
	if body["code"] == nil || body["status_code"] == nil || body["message"] == nil {
		t.Fatalf("error envelope missing fields: %v", body)
	}

	// Wrong-secret token.
	status, _ = e.do(http.MethodPost, "/api/v1/users/query", "not-a-jwt", map[string]any{})
	if status != http.StatusUnauthorized {
		t.Fatalf("garbage token: status %d", status)
	}

	// Valid user token.
	e.mustOK(http.MethodPost, "/api/v1/users/query", e.userToken("alice"), map[string]any{"filter_conditions": map[string]any{}})

	// Server-only endpoint rejects user tokens.
	status, _ = e.do(http.MethodGet, "/api/v1/app", e.userToken("alice"), nil)
	if status != http.StatusForbidden {
		t.Fatalf("server-only endpoint with user token: status %d", status)
	}
	e.mustOK(http.MethodGet, "/api/v1/app", e.serverToken(), nil)

	// Server on-behalf-of.
	resp := e.mustOK(http.MethodPost, "/api/v1/users/query?user_id=alice", e.serverToken(),
		map[string]any{"filter_conditions": map[string]any{"id": "alice"}})
	users := resp["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("expected alice via on-behalf-of, got %v", resp)
	}
}

func TestUserUpsertQueryPartial(t *testing.T) {
	e := newEnv(t)
	server := e.serverToken()

	e.mustOK(http.MethodPost, "/api/v1/users", server, map[string]any{
		"users": map[string]any{
			"dhiraj": map[string]any{"id": "dhiraj", "name": "Dhiraj", "role": "admin", "project": "FSM-EPC"},
			"bob":    map[string]any{"id": "bob", "name": "Bob"},
		},
	})

	// Custom-field filter + autocomplete.
	resp := e.mustOK(http.MethodPost, "/api/v1/users/query", server, map[string]any{
		"filter_conditions": map[string]any{"project": map[string]any{"$eq": "FSM-EPC"}},
	})
	if len(resp["users"].([]any)) != 1 {
		t.Fatalf("custom filter: %v", resp)
	}
	resp = e.mustOK(http.MethodPost, "/api/v1/users/query", server, map[string]any{
		"filter_conditions": map[string]any{"name": map[string]any{"$autocomplete": "Dhi"}},
	})
	if len(resp["users"].([]any)) != 1 {
		t.Fatalf("autocomplete: %v", resp)
	}

	// Partial update: set custom, unset later.
	e.mustOK(http.MethodPatch, "/api/v1/users", server, map[string]any{
		"users": []map[string]any{{"id": "bob", "set": map[string]any{"tier": "pro", "name": "Bobby"}}},
	})
	resp = e.mustOK(http.MethodPost, "/api/v1/users/query", server, map[string]any{
		"filter_conditions": map[string]any{"id": "bob"},
	})
	bob := resp["users"].([]any)[0].(map[string]any)
	if bob["name"] != "Bobby" || bob["tier"] != "pro" {
		t.Fatalf("partial update: %v", bob)
	}

	// Client tokens cannot escalate role.
	status, _ := e.do(http.MethodPost, "/api/v1/users", e.userToken("bob"), map[string]any{
		"users": map[string]any{"bob": map[string]any{"id": "bob", "role": "admin"}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("role escalation must be forbidden, got %d", status)
	}
	// Client tokens cannot upsert other users.
	status, _ = e.do(http.MethodPost, "/api/v1/users", e.userToken("bob"), map[string]any{
		"users": map[string]any{"mallory": map[string]any{"id": "mallory"}},
	})
	if status != http.StatusForbidden {
		t.Fatalf("cross-user upsert must be forbidden, got %d", status)
	}
}

func TestChannelCreateOrGetAndDistinct(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")

	resp := e.createChannel(alice, "messaging", "general", []string{"bob"})
	channel := resp["channel"].(map[string]any)
	if channel["cid"] != "messaging:general" {
		t.Fatalf("cid: %v", channel["cid"])
	}
	caps, _ := channel["own_capabilities"].([]any)
	if len(caps) == 0 {
		t.Fatal("own_capabilities missing")
	}
	members := resp["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	// Same id → same channel, member list unchanged.
	resp2 := e.createChannel(alice, "messaging", "general", nil)
	if resp2["channel"].(map[string]any)["cid"] != "messaging:general" {
		t.Fatal("create-or-get must return the same channel")
	}

	// Distinct channels resolve identically for the same member set.
	d1 := e.mustOK(http.MethodPost, "/api/v1/channels/messaging/query", alice,
		map[string]any{"data": map[string]any{"members": []string{"bob"}}, "state": true})
	d2 := e.mustOK(http.MethodPost, "/api/v1/channels/messaging/query", e.userToken("bob"),
		map[string]any{"data": map[string]any{"members": []string{"alice"}}, "state": true})
	cid1 := d1["channel"].(map[string]any)["cid"]
	cid2 := d2["channel"].(map[string]any)["cid"]
	if cid1 != cid2 {
		t.Fatalf("distinct channel mismatch: %v vs %v", cid1, cid2)
	}

	// Unknown channel type is a 404 with the envelope.
	status, body := e.do(http.MethodPost, "/api/v1/channels/nope/x/query", alice, map[string]any{})
	if status != http.StatusNotFound {
		t.Fatalf("unknown type: %d %v", status, body)
	}
}

func TestQueryChannelsFilters(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	e.createChannel(alice, "messaging", "with-bob", []string{"bob"})
	e.createChannel(alice, "team", "eng", nil)
	e.createChannel(e.userToken("carol"), "messaging", "carol-only", nil)

	resp := e.mustOK(http.MethodPost, "/api/v1/channels", alice, map[string]any{
		"filter_conditions": map[string]any{
			"type":    "messaging",
			"members": map[string]any{"$in": []string{"alice"}},
		},
		"sort": []map[string]any{{"field": "last_message_at", "direction": -1}},
	})
	channels := resp["channels"].([]any)
	if len(channels) != 1 {
		t.Fatalf("members $in filter: expected 1 channel, got %d", len(channels))
	}

	// Injection attempt through a filter field is rejected cleanly.
	status, _ := e.do(http.MethodPost, "/api/v1/channels", alice, map[string]any{
		"filter_conditions": map[string]any{"cid; DROP TABLE channels;--": "x"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("hostile filter field: status %d", status)
	}
}

func TestMessageLifecycle(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	bob := e.userToken("bob")
	e.createChannel(alice, "messaging", "life", []string{"bob"})

	events := e.collectEvents("evt.>")

	// Send with client-generated id → idempotent double send (M19).
	m1 := e.sendMessage(alice, "messaging", "life", "hello **world**", map[string]any{"id": "client-id-1"})
	if m1["id"] != "client-id-1" {
		t.Fatalf("client id not honored: %v", m1["id"])
	}
	if html, _ := m1["html"].(string); html == "" {
		t.Fatal("markdown html missing")
	}
	ev := events.wait(t, "message.new", 2*time.Second)
	if ev.Message == nil || ev.Message.ID != "client-id-1" {
		t.Fatalf("message.new payload: %+v", ev)
	}

	m2 := e.sendMessage(alice, "messaging", "life", "different text", map[string]any{"id": "client-id-1"})
	if m2["text"] != "hello **world**" {
		t.Fatalf("idempotent resend must return the original, got %v", m2["text"])
	}

	// Unread lifecycle across two users (U14).
	resp := e.mustOK(http.MethodGet, "/api/v1/unread", bob, nil)
	if resp["total_unread_count"].(float64) != 1 {
		t.Fatalf("bob unread: %v", resp)
	}
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/life/read", bob, map[string]any{})
	events.wait(t, "message.read", 2*time.Second)
	resp = e.mustOK(http.MethodGet, "/api/v1/unread", bob, nil)
	if resp["total_unread_count"].(float64) != 0 {
		t.Fatalf("bob unread after read: %v", resp)
	}

	// Threads (M5): replies bump reply_count; nested threads rejected.
	reply := e.sendMessage(bob, "messaging", "life", "a reply", map[string]any{"parent_id": "client-id-1"})
	parent := e.mustOK(http.MethodGet, "/api/v1/messages/client-id-1", alice, nil)["message"].(map[string]any)
	if parent["reply_count"].(float64) != 1 {
		t.Fatalf("reply_count: %v", parent["reply_count"])
	}
	status, _ := e.do(http.MethodPost, "/api/v1/channels/messaging/life/message", alice,
		map[string]any{"message": map[string]any{"text": "nested", "parent_id": reply["id"]}})
	if status != http.StatusBadRequest {
		t.Fatalf("nested thread must be rejected, got %d", status)
	}
	replies := e.mustOK(http.MethodGet, "/api/v1/messages/client-id-1/replies", alice, nil)["messages"].([]any)
	if len(replies) != 1 {
		t.Fatalf("thread pagination: %d replies", len(replies))
	}

	// Edit rules: bob cannot edit alice's message; alice can.
	status, _ = e.do(http.MethodPost, "/api/v1/messages/client-id-1", bob,
		map[string]any{"message": map[string]any{"text": "hijacked"}})
	if status != http.StatusForbidden {
		t.Fatalf("cross-user edit must be forbidden, got %d", status)
	}
	e.mustOK(http.MethodPost, "/api/v1/messages/client-id-1", alice,
		map[string]any{"message": map[string]any{"text": "edited"}})
	events.wait(t, "message.updated", 2*time.Second)

	// Soft delete: tombstone visible, text wiped.
	e.mustOK(http.MethodDelete, "/api/v1/messages/client-id-1", alice, nil)
	events.wait(t, "message.deleted", 2*time.Second)
	deleted := e.mustOK(http.MethodGet, "/api/v1/messages/client-id-1", alice, nil)["message"].(map[string]any)
	if deleted["type"] != "deleted" || deleted["text"] != "" {
		t.Fatalf("soft delete tombstone: %v", deleted)
	}

	// Message pagination window.
	for i := 0; i < 5; i++ {
		e.sendMessage(alice, "messaging", "life", fmt.Sprintf("m%d", i), nil)
	}
	page := e.mustOK(http.MethodPost, "/api/v1/channels/messaging/life/query", alice,
		map[string]any{"state": true, "messages": map[string]any{"limit": 3}})
	msgs := page["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("message page: %d", len(msgs))
	}
	last := msgs[len(msgs)-1].(map[string]any)
	if last["text"] != "m4" {
		t.Fatalf("newest message must be last in window: %v", last["text"])
	}
}

func TestReactions(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	reactors := make([]string, 20)
	for i := range reactors {
		reactors[i] = fmt.Sprintf("user-%d", i)
	}
	e.createChannel(alice, "messaging", "reactions", append([]string{"bob"}, reactors...))
	msg := e.sendMessage(alice, "messaging", "reactions", "react to me", nil)
	msgID := msg["id"].(string)

	// Score accumulation (clap x5).
	for i := 0; i < 5; i++ {
		e.mustOK(http.MethodPost, "/api/v1/messages/"+msgID+"/reaction", alice,
			map[string]any{"reaction": map[string]any{"type": "clap", "score": 1}})
	}
	got := e.mustOK(http.MethodGet, "/api/v1/messages/"+msgID, alice, nil)["message"].(map[string]any)
	scores := got["reaction_scores"].(map[string]any)
	counts := got["reaction_counts"].(map[string]any)
	if scores["clap"].(float64) != 5 || counts["clap"].(float64) != 1 {
		t.Fatalf("clap x5: scores=%v counts=%v", scores, counts)
	}

	// enforce_unique replaces other types.
	e.mustOK(http.MethodPost, "/api/v1/messages/"+msgID+"/reaction", alice,
		map[string]any{"reaction": map[string]any{"type": "love"}, "enforce_unique": true})
	got = e.mustOK(http.MethodGet, "/api/v1/messages/"+msgID, alice, nil)["message"].(map[string]any)
	counts = got["reaction_counts"].(map[string]any)
	if len(counts) != 1 || counts["love"].(float64) != 1 {
		t.Fatalf("enforce_unique: %v", counts)
	}

	// Concurrency: 20 users react in parallel; counts must be exact.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tok := e.userToken(fmt.Sprintf("user-%d", n))
			e.mustOK(http.MethodPost, "/api/v1/messages/"+msgID+"/reaction", tok,
				map[string]any{"reaction": map[string]any{"type": "thumbs"}})
		}(i)
	}
	wg.Wait()
	got = e.mustOK(http.MethodGet, "/api/v1/messages/"+msgID, alice, nil)["message"].(map[string]any)
	counts = got["reaction_counts"].(map[string]any)
	if counts["thumbs"].(float64) != 20 {
		t.Fatalf("concurrent reactions lost updates: %v", counts)
	}

	// Delete own reaction.
	e.mustOK(http.MethodDelete, "/api/v1/messages/"+msgID+"/reaction/love", alice, nil)
	got = e.mustOK(http.MethodGet, "/api/v1/messages/"+msgID, alice, nil)["message"].(map[string]any)
	if _, exists := got["reaction_counts"].(map[string]any)["love"]; exists {
		t.Fatalf("love reaction should be gone: %v", got["reaction_counts"])
	}
}

func TestFrozenChannelAndPermissions(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	e.createChannel(alice, "messaging", "frost", []string{"bob"})

	// Owner freezes the channel.
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/frost", alice, map[string]any{"frozen": true})

	status, _ := e.do(http.MethodPost, "/api/v1/channels/messaging/frost/message", e.userToken("bob"),
		map[string]any{"message": map[string]any{"text": "should fail"}})
	if status != http.StatusForbidden {
		t.Fatalf("frozen channel send must be forbidden, got %d", status)
	}

	// Server bypasses.
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/frost/message?user_id=alice", e.serverToken(),
		map[string]any{"message": map[string]any{"text": "server can"}})

	// Unfreeze restores sends.
	e.mustOK(http.MethodPost, "/api/v1/channels/messaging/frost", alice, map[string]any{"frozen": false})
	e.sendMessage(e.userToken("bob"), "messaging", "frost", "works again", nil)

	// Non-members cannot send (permission engine).
	status, _ = e.do(http.MethodPost, "/api/v1/channels/messaging/frost/message", e.userToken("stranger"),
		map[string]any{"message": map[string]any{"text": "outsider"}})
	if status != http.StatusForbidden {
		t.Fatalf("non-member send must be forbidden, got %d", status)
	}
}

func TestModerationShadowBanAndBlocklist(t *testing.T) {
	e := newEnv(t)
	alice := e.userToken("alice")
	troll := e.userToken("troll")
	server := e.serverToken()
	e.createChannel(alice, "messaging", "modtest", []string{"troll", "bob"})

	// Shadow ban the troll channel-wide.
	e.mustOK(http.MethodPost, "/api/v1/moderation/ban", server, map[string]any{
		"target_user_id": "troll", "type": "messaging", "id": "modtest", "shadow": true,
	})

	shadowMsg := e.sendMessage(troll, "messaging", "modtest", "shadowed message", nil)
	if shadowMsg["shadowed"] != true {
		t.Fatalf("message must be shadowed: %v", shadowMsg)
	}

	// Author sees it; others don't.
	trollView := e.mustOK(http.MethodPost, "/api/v1/channels/messaging/modtest/query", troll,
		map[string]any{"state": true})
	aliceView := e.mustOK(http.MethodPost, "/api/v1/channels/messaging/modtest/query", alice,
		map[string]any{"state": true})
	if len(trollView["messages"].([]any)) != 1 {
		t.Fatalf("author must see shadowed message: %v", trollView["messages"])
	}
	if len(aliceView["messages"].([]any)) != 0 {
		t.Fatalf("others must not see shadowed message: %v", aliceView["messages"])
	}

	// Hard ban blocks sends outright.
	e.mustOK(http.MethodPost, "/api/v1/moderation/ban", server, map[string]any{
		"target_user_id": "troll", "type": "messaging", "id": "modtest",
	})
	status, _ := e.do(http.MethodPost, "/api/v1/channels/messaging/modtest/message", troll,
		map[string]any{"message": map[string]any{"text": "banned now"}})
	if status != http.StatusForbidden {
		t.Fatalf("banned send must be forbidden, got %d", status)
	}
	// Unban restores.
	e.mustOK(http.MethodDelete, "/api/v1/moderation/ban?target_user_id=troll&type=messaging&id=modtest", server, nil)
	e.sendMessage(troll, "messaging", "modtest", "reformed", nil)

	// Blocklist with block behavior (messaging has automod=simple).
	e.mustOK(http.MethodPost, "/api/v1/blocklists", server, map[string]any{
		"name": "no-spoilers", "mode": "exact", "behavior": "block", "words": []string{"spoiler"},
	})
	e.mustOK(http.MethodPut, "/api/v1/channeltypes/messaging", server, map[string]any{
		"blocklist": "no-spoilers",
	})
	status, body := e.do(http.MethodPost, "/api/v1/channels/messaging/modtest/message", troll,
		map[string]any{"message": map[string]any{"text": "huge spoiler ahead"}})
	if status != http.StatusForbidden {
		t.Fatalf("blocklisted message must be blocked, got %d %v", status, body)
	}

	// Flags queue + review.
	e.mustOK(http.MethodPost, "/api/v1/moderation/flag", alice, map[string]any{
		"target_message_id": shadowMsg["id"], "reason": "abuse",
	})
	queue := e.mustOK(http.MethodGet, "/api/v1/moderation/queue", server, nil)["flags"].([]any)
	if len(queue) == 0 {
		t.Fatal("flag queue empty")
	}
	flagID := queue[0].(map[string]any)["id"].(string)
	e.mustOK(http.MethodPost, "/api/v1/moderation/queue/"+flagID+"/review", server,
		map[string]any{"result": "approved"})

	// Audit log recorded the moderation actions (server-only endpoint).
	audit := e.mustOK(http.MethodGet, "/api/v1/moderation/audit", server, nil)["audit"].([]any)
	if len(audit) < 3 {
		t.Fatalf("audit log too small: %d entries", len(audit))
	}
}

func TestRateLimitHeaders(t *testing.T) {
	e := newEnv(t)
	token := e.userToken("limited")

	url := e.http.URL + "/api/v1/users/query?api_key=" + e.app.APIKey
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("X-RateLimit-Limit") == "" ||
		resp.Header.Get("X-RateLimit-Remaining") == "" ||
		resp.Header.Get("X-RateLimit-Reset") == "" {
		t.Fatalf("rate limit headers missing: %v", resp.Header)
	}

	// Exhaust the per-user write budget (60/min) and expect 429 + envelope.
	var lastStatus int
	var lastBody map[string]any
	for i := 0; i < 62; i++ {
		lastStatus, lastBody = e.do(http.MethodPost, "/api/v1/users/query", token, map[string]any{})
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exhausting budget, got %d", lastStatus)
	}
	if lastBody["code"].(float64) != 9 {
		t.Fatalf("rate limit error code: %v", lastBody)
	}
}

func TestGuestToken(t *testing.T) {
	e := newEnv(t)
	resp := e.mustOK(http.MethodPost, "/api/v1/guest", "", map[string]any{
		"user": map[string]any{"id": "guest-1", "name": "Visitor"},
	})
	token, _ := resp["access_token"].(string)
	if token == "" {
		t.Fatalf("no guest token: %v", resp)
	}
	user := resp["user"].(map[string]any)
	if user["role"] != "guest" {
		t.Fatalf("guest role: %v", user)
	}
	// Guests cannot create channels (default policy).
	status, _ := e.do(http.MethodPost, "/api/v1/channels/messaging/guest-ch/query", token,
		map[string]any{"data": map[string]any{}})
	if status != http.StatusForbidden {
		t.Fatalf("guest channel creation must be forbidden, got %d", status)
	}
}
