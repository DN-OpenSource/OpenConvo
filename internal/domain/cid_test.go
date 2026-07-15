package domain

import (
	"strings"
	"testing"
)

func TestCIDRoundTrip(t *testing.T) {
	cid := CID("messaging", "general")
	if cid != "messaging:general" {
		t.Fatalf("CID = %q", cid)
	}
	typ, id, err := ParseCID(cid)
	if err != nil || typ != "messaging" || id != "general" {
		t.Fatalf("ParseCID = %q %q %v", typ, id, err)
	}
}

func TestParseCIDInvalid(t *testing.T) {
	for _, bad := range []string{"", "messaging", ":x", "messaging:"} {
		if _, _, err := ParseCID(bad); err == nil {
			t.Errorf("ParseCID(%q) expected error", bad)
		}
	}
}

func TestValidateIdentifiers(t *testing.T) {
	tests := []struct {
		name  string
		fn    func(string) error
		ok    []string
		notOK []string
	}{
		{"channelType", ValidateChannelType,
			[]string{"messaging", "team", "my_type-2"},
			[]string{"", "has space", "type:colon", strings.Repeat("x", 65)}},
		{"channelID", ValidateChannelID,
			[]string{"general", "!members-abc123", "a-b_c"},
			[]string{"", "has space", strings.Repeat("x", 65)}},
		{"userID", ValidateUserID,
			[]string{"dhiraj", "user.name@corp", "u_1-2"},
			[]string{"", "has space", "semi;colon", strings.Repeat("x", 65)}},
	}
	for _, tc := range tests {
		for _, s := range tc.ok {
			if err := tc.fn(s); err != nil {
				t.Errorf("%s(%q) unexpected error: %v", tc.name, s, err)
			}
		}
		for _, s := range tc.notOK {
			if err := tc.fn(s); err == nil {
				t.Errorf("%s(%q) expected error", tc.name, s)
			}
		}
	}
}

func TestDistinctChannelID(t *testing.T) {
	a := DistinctChannelID([]string{"alice", "bob"})
	b := DistinctChannelID([]string{"bob", "alice"})
	c := DistinctChannelID([]string{"bob", "alice", "bob"})
	if a != b || a != c {
		t.Fatalf("distinct id not stable across ordering/dedup: %q %q %q", a, b, c)
	}
	if !IsDistinctChannelID(a) {
		t.Fatalf("IsDistinctChannelID(%q) = false", a)
	}
	other := DistinctChannelID([]string{"alice", "carol"})
	if other == a {
		t.Fatal("different member sets produced the same id")
	}
	if err := ValidateChannelID(a); err != nil {
		t.Fatalf("derived id fails validation: %v", err)
	}
}
