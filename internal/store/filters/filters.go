// Package filters compiles MongoDB-style query filters (SPEC.md §9.2) into
// parameterized SQL. Injection is impossible by construction: field names
// must be whitelisted (or compile to JSONB accessors on a fixed column with
// the key passed as a bind parameter) and operator handling never
// interpolates user input into SQL text.
package filters

import (
	"fmt"
	"strings"
	"time"
)

// Kind describes how a whitelisted field compiles.
type Kind int

const (
	// Text compares a text column.
	Text Kind = iota
	// Int compares an integer column.
	Int
	// Bool compares a boolean column.
	Bool
	// Time compares a timestamptz column; values are RFC3339 strings.
	Time
	// TextArray matches a text[] column ($in/$contains).
	TextArray
	// MemberSubquery matches channel membership via an EXISTS subquery;
	// only valid for the channels query (field "members").
	MemberSubquery
)

// Field is one whitelisted filterable field.
type Field struct {
	Column string
	Kind   Kind
}

// Compiler translates a filter document into a WHERE clause.
type Compiler struct {
	// Fields whitelists top-level filter keys.
	Fields map[string]Field
	// CustomColumn, when set, routes unknown keys to JSONB lookups on this
	// column (e.g. "channels.custom"). The key travels as a bind parameter.
	CustomColumn string
	// MemberSubquerySQL is the EXISTS template used for MemberSubquery
	// fields; it must contain exactly one %s for the ANY($n) expression.
	MemberSubquerySQL string
}

type builder struct {
	c    *Compiler
	args []any
}

// Compile renders the filter into SQL starting at bind position
// len(existingArgs)+1. It returns the SQL fragment (or "TRUE" for an empty
// filter) and the full argument list.
func (c *Compiler) Compile(filter map[string]any, existingArgs []any) (string, []any, error) {
	b := &builder{c: c, args: existingArgs}
	if len(filter) == 0 {
		return "TRUE", b.args, nil
	}
	sql, err := b.compileAnd(filter)
	if err != nil {
		return "", nil, err
	}
	return sql, b.args, nil
}

func (b *builder) bind(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

func (b *builder) compileAnd(filter map[string]any) (string, error) {
	var parts []string
	for key, value := range filter {
		part, err := b.compileCondition(key, value)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "TRUE", nil
	}
	return "(" + strings.Join(parts, " AND ") + ")", nil
}

func (b *builder) compileCondition(key string, value any) (string, error) {
	switch key {
	case "$and", "$or", "$nor":
		list, ok := value.([]any)
		if !ok || len(list) == 0 {
			return "", fmt.Errorf("filters: %s expects a non-empty array", key)
		}
		var parts []string
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return "", fmt.Errorf("filters: %s items must be objects", key)
			}
			part, err := b.compileAnd(m)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
		switch key {
		case "$and":
			return "(" + strings.Join(parts, " AND ") + ")", nil
		case "$or":
			return "(" + strings.Join(parts, " OR ") + ")", nil
		default:
			return "NOT (" + strings.Join(parts, " OR ") + ")", nil
		}
	}

	if strings.HasPrefix(key, "$") {
		return "", fmt.Errorf("filters: unknown logical operator %q", key)
	}

	field, whitelisted := b.c.Fields[key]
	if !whitelisted {
		if b.c.CustomColumn == "" || !validCustomKey(key) {
			return "", fmt.Errorf("filters: field %q is not filterable", key)
		}
		return b.compileCustom(key, value)
	}

	ops, isDoc := value.(map[string]any)
	if !isDoc {
		ops = map[string]any{"$eq": value}
	}
	var parts []string
	for op, operand := range ops {
		part, err := b.compileOp(field, op, operand)
		if err != nil {
			return "", fmt.Errorf("filters: field %q: %w", key, err)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("filters: field %q has no operators", key)
	}
	return strings.Join(parts, " AND "), nil
}

func (b *builder) compileOp(f Field, op string, operand any) (string, error) {
	if f.Kind == MemberSubquery {
		return b.compileMemberOp(op, operand)
	}
	if f.Kind == TextArray {
		return b.compileTextArrayOp(f, op, operand)
	}

	switch op {
	case "$eq", "$ne", "$gt", "$gte", "$lt", "$lte":
		v, err := coerce(f.Kind, operand)
		if err != nil {
			return "", err
		}
		sqlOp := map[string]string{"$eq": "=", "$ne": "!=", "$gt": ">", "$gte": ">=", "$lt": "<", "$lte": "<="}[op]
		return fmt.Sprintf("%s %s %s", f.Column, sqlOp, b.bind(v)), nil
	case "$in", "$nin":
		list, err := coerceList(f.Kind, operand)
		if err != nil {
			return "", err
		}
		expr := fmt.Sprintf("%s = ANY(%s)", f.Column, b.bind(list))
		if op == "$nin" {
			expr = "NOT (" + expr + ")"
		}
		return expr, nil
	case "$autocomplete":
		if f.Kind != Text {
			return "", fmt.Errorf("$autocomplete requires a text field")
		}
		s, ok := operand.(string)
		if !ok {
			return "", fmt.Errorf("$autocomplete expects a string")
		}
		return fmt.Sprintf("%s ILIKE %s", f.Column, b.bind(escapeLike(s)+"%")), nil
	case "$contains":
		if f.Kind != Text {
			return "", fmt.Errorf("$contains requires a text or array field")
		}
		s, ok := operand.(string)
		if !ok {
			return "", fmt.Errorf("$contains expects a string")
		}
		return fmt.Sprintf("%s ILIKE %s", f.Column, b.bind("%"+escapeLike(s)+"%")), nil
	case "$exists":
		want, ok := operand.(bool)
		if !ok {
			return "", fmt.Errorf("$exists expects a boolean")
		}
		if want {
			return fmt.Sprintf("%s IS NOT NULL", f.Column), nil
		}
		return fmt.Sprintf("%s IS NULL", f.Column), nil
	default:
		return "", fmt.Errorf("unsupported operator %q", op)
	}
}

func (b *builder) compileTextArrayOp(f Field, op string, operand any) (string, error) {
	switch op {
	case "$eq", "$contains":
		s, ok := operand.(string)
		if !ok {
			return "", fmt.Errorf("%s on array field expects a string", op)
		}
		return fmt.Sprintf("%s = ANY(%s)", b.bind(s), f.Column), nil
	case "$in":
		list, err := coerceList(Text, operand)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s && %s", f.Column, b.bind(list)), nil
	default:
		return "", fmt.Errorf("unsupported array operator %q", op)
	}
}

func (b *builder) compileMemberOp(op string, operand any) (string, error) {
	if b.c.MemberSubquerySQL == "" {
		return "", fmt.Errorf("members filter not supported here")
	}
	switch op {
	case "$in":
		list, err := coerceList(Text, operand)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(b.c.MemberSubquerySQL, "ANY("+b.bind(list)+")"), nil
	case "$eq", "$contains":
		s, ok := operand.(string)
		if !ok {
			return "", fmt.Errorf("%s expects a string user id", op)
		}
		return fmt.Sprintf(b.c.MemberSubquerySQL, b.bind(s)), nil
	default:
		return "", fmt.Errorf("unsupported members operator %q", op)
	}
}

// compileCustom compiles JSONB custom-field access; the key is a bind
// parameter, never interpolated.
func (b *builder) compileCustom(key string, value any) (string, error) {
	ops, isDoc := value.(map[string]any)
	if !isDoc {
		ops = map[string]any{"$eq": value}
	}
	var parts []string
	for op, operand := range ops {
		accessor := func() string {
			return fmt.Sprintf("%s->>%s", b.c.CustomColumn, b.bind(key))
		}
		switch op {
		case "$eq", "$ne":
			sqlOp := "="
			if op == "$ne" {
				sqlOp = "!="
			}
			parts = append(parts, fmt.Sprintf("%s %s %s", accessor(), sqlOp, b.bind(stringify(operand))))
		case "$gt", "$gte", "$lt", "$lte":
			sqlOp := map[string]string{"$gt": ">", "$gte": ">=", "$lt": "<", "$lte": "<="}[op]
			if n, isNum := numeric(operand); isNum {
				parts = append(parts, fmt.Sprintf("(%s)::numeric %s %s", accessor(), sqlOp, b.bind(n)))
			} else {
				parts = append(parts, fmt.Sprintf("%s %s %s", accessor(), sqlOp, b.bind(stringify(operand))))
			}
		case "$in":
			list, err := coerceList(Text, normalizeToStrings(operand))
			if err != nil {
				return "", fmt.Errorf("custom field %q: %w", key, err)
			}
			parts = append(parts, fmt.Sprintf("%s = ANY(%s)", accessor(), b.bind(list)))
		case "$exists":
			want, ok := operand.(bool)
			if !ok {
				return "", fmt.Errorf("custom field %q: $exists expects a boolean", key)
			}
			expr := fmt.Sprintf("%s ? %s", b.c.CustomColumn, b.bind(key))
			if !want {
				expr = "NOT (" + expr + ")"
			}
			parts = append(parts, expr)
		case "$autocomplete", "$contains":
			s, ok := operand.(string)
			if !ok {
				return "", fmt.Errorf("custom field %q: %s expects a string", key, op)
			}
			pattern := escapeLike(s) + "%"
			if op == "$contains" {
				pattern = "%" + escapeLike(s) + "%"
			}
			parts = append(parts, fmt.Sprintf("%s ILIKE %s", accessor(), b.bind(pattern)))
		default:
			return "", fmt.Errorf("custom field %q: unsupported operator %q", key, op)
		}
	}
	return strings.Join(parts, " AND "), nil
}

func validCustomKey(key string) bool {
	if key == "" || len(key) > 255 {
		return false
	}
	for _, r := range key {
		legal := r == '_' || r == '-' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !legal {
			return false
		}
	}
	return true
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%f", t), "0"), ".")
	default:
		return fmt.Sprintf("%v", v)
	}
}

func numeric(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	}
	return 0, false
}

func normalizeToStrings(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, len(list))
	for i, item := range list {
		out[i] = stringify(item)
	}
	return out
}

func coerce(k Kind, v any) (any, error) {
	switch k {
	case Text:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", v)
		}
		return s, nil
	case Int:
		switch n := v.(type) {
		case float64:
			return int64(n), nil
		case int:
			return int64(n), nil
		}
		return nil, fmt.Errorf("expected number, got %T", v)
	case Bool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected boolean, got %T", v)
		}
		return b, nil
	case Time:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected RFC3339 string, got %T", v)
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp %q", s)
		}
		return t, nil
	default:
		return nil, fmt.Errorf("field kind does not support scalar comparison")
	}
}

func coerceList(k Kind, v any) (any, error) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("expected non-empty array, got %T", v)
	}
	if len(list) > 1000 {
		return nil, fmt.Errorf("array operand too large (max 1000)")
	}
	switch k {
	case Text, TextArray:
		out := make([]string, len(list))
		for i, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("array item %d: expected string", i)
			}
			out[i] = s
		}
		return out, nil
	case Int:
		out := make([]int64, len(list))
		for i, item := range list {
			n, ok := item.(float64)
			if !ok {
				return nil, fmt.Errorf("array item %d: expected number", i)
			}
			out[i] = int64(n)
		}
		return out, nil
	case Time:
		out := make([]time.Time, len(list))
		for i, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("array item %d: expected RFC3339 string", i)
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return nil, fmt.Errorf("array item %d: invalid timestamp", i)
			}
			out[i] = t
		}
		return out, nil
	default:
		return nil, fmt.Errorf("field kind does not support array comparison")
	}
}
