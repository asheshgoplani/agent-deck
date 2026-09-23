package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Cursor is a durable, monotonically increasing, per-profile sequence number
// assigned to a Frame when it is committed to the bus's append log. Cursor 0
// is never assigned to a real frame: Subscribe(ctx, 0) means "from the
// beginning of the retained log".
type Cursor uint64

// Frame is one event on the bus: {cursor, event_id, ts, kind, session_id,
// data}. TS is Unix milliseconds. Data is an arbitrary, producer-defined JSON
// value (may be empty/omitted).
type Frame struct {
	Cursor    Cursor          `json:"cursor"`
	EventID   string          `json:"event_id"`
	TS        int64           `json:"ts"`
	Kind      string          `json:"kind"`
	SessionID string          `json:"session_id"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// CanonicalJSON renders the frame as compact JSON with every object's keys
// sorted lexicographically and no trailing whitespace, so the same logical
// frame always serializes to identical bytes. The append log, the golden
// NDJSON test and `events follow --json` all use this encoding.
func (f Frame) CanonicalJSON() ([]byte, error) {
	obj := map[string]any{
		"cursor":     uint64(f.Cursor),
		"event_id":   f.EventID,
		"ts":         f.TS,
		"kind":       f.Kind,
		"session_id": f.SessionID,
	}
	if len(f.Data) > 0 {
		dataAny, err := decodeCanonicalValue(f.Data)
		if err != nil {
			return nil, fmt.Errorf("decode frame data: %w", err)
		}
		obj["data"] = dataAny
	}
	var buf bytes.Buffer
	if err := encodeCanonical(&buf, obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeCanonicalValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// encodeCanonical writes v as compact JSON, sorting map keys at every level.
// It never adds indentation or trailing spaces.
func encodeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := encodeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}

// ParseFrameLine parses one NDJSON line (as written to the append log or
// emitted by `events follow`) back into a Frame.
func ParseFrameLine(line []byte) (Frame, error) {
	var f Frame
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&f); err != nil {
		return Frame{}, err
	}
	return f, nil
}
