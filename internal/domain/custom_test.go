package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUserCustomFieldRoundTrip(t *testing.T) {
	in := []byte(`{"id":"dhiraj","name":"Dhiraj","role":"user","project":"FSM-EPC","level":7,"tags":["a","b"]}`)
	var u User
	if err := json.Unmarshal(in, &u); err != nil {
		t.Fatal(err)
	}
	if u.ID != "dhiraj" || u.Name != "Dhiraj" {
		t.Fatalf("declared fields lost: %+v", u)
	}
	if u.Custom["project"] != "FSM-EPC" {
		t.Fatalf("custom project = %v", u.Custom["project"])
	}
	if u.Custom["level"] != float64(7) {
		t.Fatalf("custom level = %v", u.Custom["level"])
	}

	out, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["project"] != "FSM-EPC" {
		t.Fatalf("marshaled custom missing: %s", out)
	}
	if m["id"] != "dhiraj" {
		t.Fatalf("marshaled id missing: %s", out)
	}
	if _, hasCustomKey := m["Custom"]; hasCustomKey {
		t.Fatal("Custom map leaked as a named field")
	}
}

func TestCustomFieldCannotShadowReserved(t *testing.T) {
	// "id" is declared; it must bind to the struct field, never to custom.
	var u User
	if err := json.Unmarshal([]byte(`{"id":"x","online":true}`), &u); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.Custom["id"]; ok {
		t.Fatal("reserved key leaked into custom map")
	}
	if err := ValidateCustom(&User{}, map[string]any{"id": "evil"}); err == nil {
		t.Fatal("ValidateCustom accepted reserved key")
	}
	if err := ValidateCustom(&User{}, map[string]any{"favorite_color": "blue"}); err != nil {
		t.Fatalf("ValidateCustom rejected legal key: %v", err)
	}
}

func TestCustomFieldSizeLimit(t *testing.T) {
	big := strings.Repeat("x", MaxCustomDataBytes+1)
	var u User
	err := json.Unmarshal([]byte(`{"id":"x","blob":"`+big+`"}`), &u)
	if err == nil {
		t.Fatal("expected size-limit error")
	}
	if err := ValidateCustom(&User{}, map[string]any{"blob": big}); err == nil {
		t.Fatal("ValidateCustom accepted oversized payload")
	}
}

func TestMessageMarshalDefaults(t *testing.T) {
	m := Message{ID: "m1", Type: MessageTypeRegular, CreatedAt: time.Now()}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reaction_counts", "reaction_scores", "latest_reactions", "mentioned_users", "attachments"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("expected %q present in message payload", key)
		}
	}
}

func TestOwnUserMarshalIncludesPrivateState(t *testing.T) {
	o := OwnUser{User: User{ID: "u1", Role: "user", Custom: map[string]any{"plan": "pro"}}, TotalUnreadCount: 3}
	out, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["total_unread_count"] != float64(3) {
		t.Fatalf("total_unread_count = %v", m["total_unread_count"])
	}
	if m["plan"] != "pro" {
		t.Fatalf("custom field lost on OwnUser: %s", out)
	}
	if _, ok := m["mutes"]; !ok {
		t.Fatal("mutes missing")
	}
}
