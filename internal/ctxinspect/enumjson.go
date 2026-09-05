package ctxinspect

import (
	"encoding/json"
	"fmt"
)

// enumName looks up the display/wire name of an enum value, falling back to a
// caller-supplied default for values outside the table.
func enumName[T comparable](v T, names map[T]string, fallback string) string {
	if s, ok := names[v]; ok {
		return s
	}
	return fallback
}

// invertEnum builds the name -> value table used when decoding.
func invertEnum[T comparable](names map[T]string) map[string]T {
	out := make(map[string]T, len(names))
	for v, s := range names {
		out[s] = v
	}
	return out
}

// marshalEnum encodes an enum as its wire name. Values outside the table are a
// programming error and are reported as such rather than silently encoded as a
// number that no decoder would accept.
func marshalEnum[T comparable](v T, names map[T]string) ([]byte, error) {
	s, ok := names[v]
	if !ok {
		return nil, fmt.Errorf("ctxinspect: cannot encode unknown enum value %v", v)
	}
	return json.Marshal(s)
}

// unmarshalEnum decodes an enum from its wire name, rejecting names the table
// does not know instead of defaulting to the zero value.
func unmarshalEnum[T comparable](b []byte, byName map[string]T, dst *T, kind string) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("ctxinspect: %s must be a string: %w", kind, err)
	}
	v, ok := byName[s]
	if !ok {
		return fmt.Errorf("ctxinspect: unknown %s %q", kind, s)
	}
	*dst = v
	return nil
}
