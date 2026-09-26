package events

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCanonicalJSONGoldenFrames pins the exact wire bytes for a handful of
// representative frames: sorted keys at every level, no trailing spaces, one
// frame per line. `agent-deck events follow --json` and the append log both
// use this same encoding, so a byte-for-byte diff here is a real contract
// test, not just a style check.
func TestCanonicalJSONGoldenFrames(t *testing.T) {
	frames := []Frame{
		{
			Cursor:    1,
			EventID:   "abc123",
			TS:        1758000000000,
			Kind:      "session.status",
			SessionID: "sess-1",
			Data:      json.RawMessage(`{"status":"running","prev_status":"idle"}`),
		},
		{
			Cursor:    2,
			EventID:   "def456",
			TS:        1758000000500,
			Kind:      "tmux.output",
			SessionID: "sess-2",
			Data:      json.RawMessage(`{"z_last":true,"a_first":1,"nested":{"b":2,"a":1}}`),
		},
		{
			Cursor:    3,
			EventID:   "ghi789",
			TS:        1758000001000,
			Kind:      "watcher.health",
			SessionID: "",
		},
	}

	var got []byte
	for _, f := range frames {
		line, err := f.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		got = append(got, line...)
		got = append(got, '\n')
	}

	goldenPath := filepath.Join("testdata", "frames.golden.ndjson")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("canonical NDJSON drifted from golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	for i, line := range splitNDJSON(got) {
		if len(line) == 0 {
			continue
		}
		if line[0] == ' ' || line[len(line)-1] == ' ' {
			t.Fatalf("line %d has leading/trailing space: %q", i, line)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i, err)
		}
		assertSortedKeys(t, line)
	}
}

func splitNDJSON(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	return out
}

// assertSortedKeys checks that every object's keys, at every nesting level,
// appear in lexicographic order, via a standard recursive-descent walk of
// json.Decoder's token stream (which preserves source order, unlike
// unmarshaling into a map).
func assertSortedKeys(t *testing.T, line []byte) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(line))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	checkSortedValue(t, dec, tok)
}

func checkSortedValue(t *testing.T, dec *json.Decoder, tok json.Token) {
	t.Helper()
	delim, ok := tok.(json.Delim)
	if !ok {
		return // scalar: nothing to check
	}
	switch delim {
	case '{':
		checkSortedObject(t, dec)
	case '[':
		checkSortedArray(t, dec)
	}
}

func checkSortedObject(t *testing.T, dec *json.Decoder) {
	t.Helper()
	var lastKey string
	first := true
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatalf("object key token: %v", err)
		}
		key := keyTok.(string)
		if !first && key < lastKey {
			t.Fatalf("keys not sorted: %q before %q", lastKey, key)
		}
		lastKey, first = key, false

		valTok, err := dec.Token()
		if err != nil {
			t.Fatalf("object value token: %v", err)
		}
		checkSortedValue(t, dec, valTok)
	}
	if _, err := dec.Token(); err != nil { // consume closing '}'
		t.Fatalf("object close token: %v", err)
	}
}

func checkSortedArray(t *testing.T, dec *json.Decoder) {
	t.Helper()
	for dec.More() {
		valTok, err := dec.Token()
		if err != nil {
			t.Fatalf("array element token: %v", err)
		}
		checkSortedValue(t, dec, valTok)
	}
	if _, err := dec.Token(); err != nil { // consume closing ']'
		t.Fatalf("array close token: %v", err)
	}
}
