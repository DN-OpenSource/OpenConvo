package filters

import (
	"encoding/json"
	"strings"
	"testing"
)

func userCompiler() *Compiler {
	return &Compiler{
		Fields: map[string]Field{
			"id":         {Column: "users.id", Kind: Text},
			"name":       {Column: "users.name", Kind: Text},
			"role":       {Column: "users.role", Kind: Text},
			"banned":     {Column: "users.banned", Kind: Bool},
			"created_at": {Column: "users.created_at", Kind: Time},
			"teams":      {Column: "users.teams", Kind: TextArray},
		},
		CustomColumn: "users.custom",
	}
}

func channelCompiler() *Compiler {
	return &Compiler{
		Fields: map[string]Field{
			"type":            {Column: "channels.type", Kind: Text},
			"id":              {Column: "channels.id", Kind: Text},
			"cid":             {Column: "channels.cid", Kind: Text},
			"frozen":          {Column: "channels.frozen", Kind: Bool},
			"member_count":    {Column: "channels.member_count", Kind: Int},
			"last_message_at": {Column: "channels.last_message_at", Kind: Time},
			"members":         {Kind: MemberSubquery},
		},
		CustomColumn:      "channels.custom",
		MemberSubquerySQL: "EXISTS (SELECT 1 FROM channel_members m WHERE m.app_id = channels.app_id AND m.channel_type = channels.type AND m.channel_id = channels.id AND m.user_id = %s)",
	}
}

func compile(t *testing.T, c *Compiler, filterJSON string) (string, []any) {
	t.Helper()
	var filter map[string]any
	if err := json.Unmarshal([]byte(filterJSON), &filter); err != nil {
		t.Fatalf("bad filter fixture: %v", err)
	}
	sql, args, err := c.Compile(filter, nil)
	if err != nil {
		t.Fatalf("Compile(%s): %v", filterJSON, err)
	}
	return sql, args
}

func mustFail(t *testing.T, c *Compiler, filterJSON string) {
	t.Helper()
	var filter map[string]any
	if err := json.Unmarshal([]byte(filterJSON), &filter); err != nil {
		t.Fatalf("bad filter fixture: %v", err)
	}
	if sql, _, err := c.Compile(filter, nil); err == nil {
		t.Fatalf("Compile(%s) expected error, got %q", filterJSON, sql)
	}
}

func TestCompileBasics(t *testing.T) {
	c := userCompiler()

	sql, args := compile(t, c, `{"id":"dhiraj"}`)
	if !strings.Contains(sql, "users.id = $1") || args[0] != "dhiraj" {
		t.Fatalf("eq shorthand: %q %v", sql, args)
	}

	sql, args = compile(t, c, `{"name":{"$autocomplete":"dhi"}}`)
	if !strings.Contains(sql, "users.name ILIKE $1") || args[0] != "dhi%" {
		t.Fatalf("autocomplete: %q %v", sql, args)
	}

	sql, args = compile(t, c, `{"role":{"$in":["admin","moderator"]}}`)
	if !strings.Contains(sql, "users.role = ANY($1)") {
		t.Fatalf("$in: %q", sql)
	}
	if list, ok := args[0].([]string); !ok || len(list) != 2 {
		t.Fatalf("$in args: %#v", args)
	}

	sql, _ = compile(t, c, `{"banned":true}`)
	if !strings.Contains(sql, "users.banned = $1") {
		t.Fatalf("bool: %q", sql)
	}

	sql, _ = compile(t, c, `{"created_at":{"$gt":"2026-01-01T00:00:00Z"}}`)
	if !strings.Contains(sql, "users.created_at > $1") {
		t.Fatalf("time gt: %q", sql)
	}

	sql, _ = compile(t, c, `{"teams":{"$in":["acme"]}}`)
	if !strings.Contains(sql, "users.teams && $1") {
		t.Fatalf("array overlap: %q", sql)
	}

	sql, _ = compile(t, c, `{"teams":{"$contains":"acme"}}`)
	if !strings.Contains(sql, "= ANY(users.teams)") {
		t.Fatalf("array contains: %q", sql)
	}
}

func TestCompileLogical(t *testing.T) {
	c := userCompiler()
	sql, args := compile(t, c, `{"$or":[{"id":"a"},{"name":{"$autocomplete":"b"}}]}`)
	if !strings.Contains(sql, " OR ") || len(args) != 2 {
		t.Fatalf("$or: %q %v", sql, args)
	}
	sql, _ = compile(t, c, `{"$and":[{"role":"user"},{"banned":false}]}`)
	if !strings.Contains(sql, " AND ") {
		t.Fatalf("$and: %q", sql)
	}
	sql, _ = compile(t, c, `{"$nor":[{"role":"admin"}]}`)
	if !strings.HasPrefix(sql, "(NOT (") {
		t.Fatalf("$nor: %q", sql)
	}
}

func TestCompileCustomFields(t *testing.T) {
	c := userCompiler()
	sql, args := compile(t, c, `{"project":{"$eq":"FSM-EPC"}}`)
	if !strings.Contains(sql, "users.custom->>$1 = $2") {
		t.Fatalf("custom eq must bind key and value: %q", sql)
	}
	if args[0] != "project" || args[1] != "FSM-EPC" {
		t.Fatalf("custom args: %v", args)
	}

	sql, _ = compile(t, c, `{"level":{"$gt":3}}`)
	if !strings.Contains(sql, "::numeric >") {
		t.Fatalf("custom numeric: %q", sql)
	}

	sql, _ = compile(t, c, `{"vip":{"$exists":true}}`)
	if !strings.Contains(sql, "users.custom ? $1") {
		t.Fatalf("custom exists: %q", sql)
	}
}

func TestCompileMembers(t *testing.T) {
	c := channelCompiler()
	sql, args := compile(t, c, `{"type":"messaging","members":{"$in":["dhiraj"]}}`)
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM channel_members") {
		t.Fatalf("members: %q", sql)
	}
	if len(args) != 2 {
		t.Fatalf("members args: %v", args)
	}
}

func TestArgOffsets(t *testing.T) {
	c := userCompiler()
	var filter map[string]any
	if err := json.Unmarshal([]byte(`{"id":"x"}`), &filter); err != nil {
		t.Fatal(err)
	}
	sql, args, err := c.Compile(filter, []any{"pre-existing"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "$2") || len(args) != 2 {
		t.Fatalf("offset binding: %q %v", sql, args)
	}
}

func TestMaliciousInputRejected(t *testing.T) {
	c := userCompiler()
	malicious := []string{
		`{"id; DROP TABLE users;--":"x"}`,     // hostile field name
		`{"id":{"$regex":"^a"}}`,              // unsupported operator
		`{"$where":"1=1"}`,                    // mongo eval operator
		`{"id":{"$in":[]}}`,                   // empty $in
		`{"id":{"$in":"not-an-array"}}`,       // wrong operand type
		`{"banned":{"$eq":"true or 1=1"}}`,    // type mismatch on bool
		`{"created_at":{"$gt":"not-a-date"}}`, // bad timestamp
		`{"$or":"nope"}`,                      // logical operator wrong type
		`{"$or":[]}`,                          // empty logical group
		`{"custom' OR '1'='1":{"$eq":"x"}}`,   // hostile custom key
		`{"a":{"$exists":"yes"}}`,             // $exists non-bool
	}
	for _, m := range malicious {
		mustFail(t, c, m)
	}

	// LIKE metacharacters must be escaped, not act as wildcards.
	sql, args := compile(t, c, `{"name":{"$autocomplete":"50%_off"}}`)
	if !strings.Contains(sql, "ILIKE") {
		t.Fatalf("autocomplete: %q", sql)
	}
	if args[0] != `50\%\_off%` {
		t.Fatalf("LIKE escaping failed: %v", args[0])
	}

	// Oversized $in arrays are rejected.
	big := make([]string, 1001)
	list := "["
	for i := range big {
		if i > 0 {
			list += ","
		}
		list += `"x"`
	}
	list += "]"
	mustFail(t, c, `{"id":{"$in":`+list+`}}`)
}
