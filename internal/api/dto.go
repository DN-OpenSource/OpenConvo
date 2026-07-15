package api

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/openstream/openstream/internal/apierror"
	"github.com/openstream/openstream/internal/domain"
)

// unmarshalFlattenedDTO decodes data into dst and collects unknown top-level
// keys (minus reserved) into a custom map, mirroring domain flattening for
// request DTOs. Size is bounded by domain.MaxCustomDataBytes.
func unmarshalFlattenedDTO(data []byte, dst any, reserved map[string]struct{}) (map[string]any, error) {
	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	custom := map[string]any{}
	size := 0
	for k, raw := range all {
		if _, isReserved := reserved[k]; isReserved {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		custom[k] = v
		size += len(k) + len(raw)
	}
	if size > domain.MaxCustomDataBytes {
		return nil, fmt.Errorf("custom data exceeds %d bytes", domain.MaxCustomDataBytes)
	}
	if len(custom) == 0 {
		return nil, nil
	}
	return custom, nil
}

// sortParam is the wire sort form: {"field": "...", "direction": -1|1}.
type sortParam struct {
	Field     string `json:"field"`
	Direction int    `json:"direction"`
}

// buildOrderBy renders a whitelisted ORDER BY clause; unknown fields are an
// input error (never interpolated).
func buildOrderBy(sorts []sortParam, whitelist map[string]string, fallback string) (string, error) {
	if len(sorts) == 0 {
		return fallback, nil
	}
	out := ""
	for i, sp := range sorts {
		column, ok := whitelist[sp.Field]
		if !ok {
			return "", apierror.Input("cannot sort by %q", sp.Field)
		}
		if i > 0 {
			out += ", "
		}
		out += column
		if sp.Direction < 0 {
			out += " DESC"
		}
	}
	return out, nil
}

// clampPage bounds limit/offset.
func clampPage(limit, offset, def, max int) (int, int) {
	if limit <= 0 || limit > max {
		limit = def
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func itoa(n int) string { return strconv.Itoa(n) }
