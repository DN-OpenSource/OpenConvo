package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// MaxCustomDataBytes caps the serialized size of custom fields per entity
// (SPEC.md §4.3).
const MaxCustomDataBytes = 5 * 1024

var reservedKeyCache sync.Map // reflect.Type -> map[string]struct{}

// reservedKeys returns the set of declared json field names for a struct
// type; these may not be used as custom-field keys.
func reservedKeys(t reflect.Type) map[string]struct{} {
	if cached, ok := reservedKeyCache.Load(t); ok {
		return cached.(map[string]struct{})
	}
	keys := make(map[string]struct{})
	collectJSONKeys(t, keys)
	reservedKeyCache.Store(t, keys)
	return keys
}

func collectJSONKeys(t reflect.Type, keys map[string]struct{}) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				collectJSONKeys(ft, keys)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		keys[name] = struct{}{}
	}
}

// marshalFlattened marshals v and merges custom key/values at the top level,
// producing Stream-style flattened JSON (SPEC.md §4.3).
func marshalFlattened(v any, custom map[string]any) ([]byte, error) {
	base, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(custom) == 0 {
		return base, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	reserved := reservedKeys(reflect.TypeOf(v).Elem())
	for k, val := range custom {
		if _, isReserved := reserved[k]; isReserved {
			continue
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return nil, err
		}
		m[k] = raw
	}
	return json.Marshal(m)
}

// unmarshalFlattened decodes data into v and collects unknown top-level keys
// into a custom map. Reserved-key collisions are impossible by construction;
// the size limit is enforced here.
func unmarshalFlattened(data []byte, v any) (map[string]any, error) {
	if err := json.Unmarshal(data, v); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	reserved := reservedKeys(reflect.TypeOf(v).Elem())
	custom := make(map[string]any)
	size := 0
	for k, raw := range all {
		if _, isReserved := reserved[k]; isReserved {
			continue
		}
		var val any
		if err := json.Unmarshal(raw, &val); err != nil {
			return nil, err
		}
		custom[k] = val
		size += len(k) + len(raw)
	}
	if size > MaxCustomDataBytes {
		return nil, fmt.Errorf("custom data exceeds %d bytes", MaxCustomDataBytes)
	}
	if len(custom) == 0 {
		return nil, nil
	}
	return custom, nil
}

// ValidateCustom rejects reserved keys and oversized payloads for custom
// data supplied through partial-update style APIs (explicit set maps).
func ValidateCustom(entity any, custom map[string]any) error {
	if len(custom) == 0 {
		return nil
	}
	reserved := reservedKeys(reflect.TypeOf(entity).Elem())
	size := 0
	for k, v := range custom {
		if _, isReserved := reserved[k]; isReserved {
			return fmt.Errorf("field %q is reserved", k)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("field %q: %w", k, err)
		}
		size += len(k) + len(raw)
	}
	if size > MaxCustomDataBytes {
		return fmt.Errorf("custom data exceeds %d bytes", MaxCustomDataBytes)
	}
	return nil
}
