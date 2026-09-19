package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Review round 3 (finding 1): the settings.json rewrite used to go through
// typed structs that knew only type/command/async, so every user hook that
// shared one of our events lost its other fields (timeout, prompt, url, if,
// statusMessage) and the top-level keys were re-sorted. jsonObject is the
// order-preserving object the install, heal and uninstall round-trip the file
// through: each value stays the raw bytes it was read as, unknown keys are
// kept in place, and only the values the code replaces change.

// jsonField is one key/value pair of a jsonObject, value kept verbatim.
type jsonField struct {
	Key   string
	Value json.RawMessage
}

// jsonObject is a JSON object with its keys in file order.
type jsonObject []jsonField

var errNotJSONObject = errors.New("not a JSON object")

// UnmarshalJSON decodes an object token by token so key order survives.
func (o *jsonObject) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errNotJSONObject
	}
	var out jsonObject
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("object key %v is not a string", keyTok)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		out = append(out, jsonField{Key: key, Value: value})
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return err
	}
	*o = out
	return nil
}

// MarshalJSON emits the fields in order, values verbatim.
func (o jsonObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(f.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		if len(f.Value) == 0 {
			buf.WriteString("null")
		} else {
			buf.Write(f.Value)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// get returns the raw value of key and whether it is present.
func (o jsonObject) get(key string) (json.RawMessage, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// getString returns the value of key as a string ("" when absent or not a
// string).
func (o jsonObject) getString(key string) string {
	raw, ok := o.get(key)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// set replaces the value of key in place, or appends the key when absent.
func (o *jsonObject) set(key string, value json.RawMessage) {
	for i, f := range *o {
		if f.Key == key {
			(*o)[i].Value = value
			return
		}
	}
	*o = append(*o, jsonField{Key: key, Value: value})
}

// del removes key; a no-op when absent.
func (o *jsonObject) del(key string) {
	for i, f := range *o {
		if f.Key == key {
			*o = append((*o)[:i], (*o)[i+1:]...)
			return
		}
	}
}

// mustMarshal is json.Marshal for values built by this file, which cannot
// fail to encode.
func mustMarshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// indentSettings renders the assembled settings object the way Claude Code
// and the previous install wrote it (two-space indent, trailing newline).
func indentSettings(root jsonObject) ([]byte, error) {
	compact, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// asMap returns the object's fields as a map for the read-only checks that
// do not care about order.
func (o jsonObject) asMap() map[string]json.RawMessage {
	m := make(map[string]json.RawMessage, len(o))
	for _, f := range o {
		m[f.Key] = f.Value
	}
	return m
}
