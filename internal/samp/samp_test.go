package samp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLine appends a single SAMP record as one JSONL line to
// $dir/log-<from>.jsonl, mirroring how the Python reference writer or
// any other SAMP producer would have written it. Tests use this to
// stage fixture data without coupling to any Go-side writer.
func writeLine(t *testing.T, dir string, m *Message) {
	t.Helper()
	if m.ID == "" {
		id, err := ComputeID(m.TS, m.From, m.To, m.Thread, m.Body)
		if err != nil {
			t.Fatal(err)
		}
		m.ID = id
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("log-%s.jsonl", m.From))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAlias(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"alice", true},
		{"a", true},
		{"agent-deck", true},
		{"foo.bar_baz-1", true},
		{"a" + strings.Repeat("b", 63), true},
		{"a" + strings.Repeat("b", 64), false},
		{"-leading-dash", false},
		{".dotfirst", false},
		{"has space", false},
		{"has/slash", false},
		{"has:colon", false},
		{"", false},
	}
	for _, c := range cases {
		got := ValidateAlias(c.in) == nil
		if got != c.ok {
			t.Errorf("ValidateAlias(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}

// TestCanonical_KnownVector pins the exact byte output of canonical for an
// ASCII-only input. Load-bearing parity test against the Python reference:
//
//	import json
//	c = json.dumps({"body":"hello","from":"alice","thread":"t1","to":"bob","ts":1700000000},
//	               sort_keys=True, separators=(",",":"), ensure_ascii=False)
//	# c == '{"body":"hello","from":"alice","thread":"t1","to":"bob","ts":1700000000}'
//
// If this test ever fails, do NOT relax it — Go's encoder has drifted
// from the Python reference and ids will diverge across impls.
func TestCanonical_KnownVector(t *testing.T) {
	want := `{"body":"hello","from":"alice","thread":"t1","to":"bob","ts":1700000000}`
	got, err := canonical(1700000000, "alice", "bob", "t1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("canonical mismatch:\n got  %s\n want %s", got, want)
	}
}

// TestCanonical_UnicodeRaw verifies non-ASCII characters survive as raw
// UTF-8 bytes (no \uXXXX escapes), per Python's ensure_ascii=False.
func TestCanonical_UnicodeRaw(t *testing.T) {
	want := `{"body":"héllo","from":"alice","thread":"t1","to":"bob","ts":1700000000}`
	got, err := canonical(1700000000, "alice", "bob", "t1", "héllo")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("canonical unicode mismatch:\n got  %s\n want %s", got, want)
	}
}

// TestCanonical_HTMLNotEscaped catches a Go-specific trap: encoding/json
// HTML-escapes '<', '>', '&' by default, which would diverge from Python.
// SetEscapeHTML(false) must stay set in canonical().
func TestCanonical_HTMLNotEscaped(t *testing.T) {
	c, err := canonical(1700000000, "a", "b", "t", "<tag>&amp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c), "<tag>&amp") {
		t.Fatalf("canonical body escaped HTML: %s", c)
	}
}

func TestComputeID_Stable(t *testing.T) {
	id1, err := ComputeID(1700000000, "alice", "bob", "t1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ComputeID(1700000000, "alice", "bob", "t1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("ComputeID not stable: %s vs %s", id1, id2)
	}
	if len(id1) != 16 {
		t.Fatalf("ComputeID length %d, want 16", len(id1))
	}
}

func TestComputeID_DiffersOnEachField(t *testing.T) {
	base, _ := ComputeID(1700000000, "alice", "bob", "t1", "hello")
	muts := map[string]string{
		"ts":     mustID(t, 1700000001, "alice", "bob", "t1", "hello"),
		"from":   mustID(t, 1700000000, "alicia", "bob", "t1", "hello"),
		"to":     mustID(t, 1700000000, "alice", "carol", "t1", "hello"),
		"thread": mustID(t, 1700000000, "alice", "bob", "t2", "hello"),
		"body":   mustID(t, 1700000000, "alice", "bob", "t1", "hi"),
	}
	for field, mut := range muts {
		if mut == base {
			t.Errorf("mutating %s did not change id (%s)", field, base)
		}
	}
}

func mustID(t *testing.T, ts int64, from, to, thread, body string) string {
	t.Helper()
	id, err := ComputeID(ts, from, to, thread, body)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestComputeID_NFCIdempotent verifies that the same logical body in NFC
// vs NFD form produces the same id, since SPEC.md §3 mandates body NFC
// normalization before serialization.
func TestComputeID_NFCIdempotent(t *testing.T) {
	nfc := "café"  // composed é (U+00E9)
	nfd := "café" // e + combining acute (U+0065 U+0301)
	if nfc == nfd {
		t.Fatal("test setup: NFC and NFD literals are equal")
	}
	idNFC, err := ComputeID(1700000000, "alice", "bob", "t1", nfc)
	if err != nil {
		t.Fatal(err)
	}
	idNFD, err := ComputeID(1700000000, "alice", "bob", "t1", nfd)
	if err != nil {
		t.Fatal(err)
	}
	if idNFC != idNFD {
		t.Fatalf("NFC/NFD ids differ: %s vs %s — NFC normalization broken", idNFC, idNFD)
	}
}

func TestScan_FiltersByRecipient(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	for i, to := range []string{"bob", "carol", "bob"} {
		writeLine(t, dir, &Message{
			From: "alice", To: to, Body: "msg-" + to, Thread: "t",
			TS: now.Unix() + int64(i),
		})
	}
	res, err := Scan(dir, ScanOptions{Me: "bob"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("got %d, want 2 messages addressed to bob", len(res.Messages))
	}
	for _, m := range res.Messages {
		if m.To != "bob" {
			t.Errorf("returned msg to=%q, want bob", m.To)
		}
	}
}

// TestScan_DedupsByID simulates Syncthing duplicating a record into a
// second writer's log — readers must dedup by content-addressed id.
func TestScan_DedupsByID(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	writeLine(t, dir, &Message{From: "alice", To: "bob", Body: "hi", Thread: "t", TS: now.Unix()})
	original, err := os.ReadFile(filepath.Join(dir, "log-alice.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log-mallory.jsonl"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(dir, ScanOptions{Me: "bob"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("Scan returned %d, want 1 (dedup by id)", len(res.Messages))
	}
}

func TestScan_AppliesWatermark(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	for i, body := range []string{"a", "b", "c"} {
		writeLine(t, dir, &Message{
			From: "alice", To: "bob", Body: body, Thread: "fixed",
			TS: now.Unix() + int64(i),
		})
	}
	res, err := Scan(dir, ScanOptions{Me: "bob"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("first scan got %d, want 3", len(res.Messages))
	}
	wm := &Watermark{TS: res.MaxTS, IDs: append([]string(nil), res.IDsAtMaxTS...)}

	res2, err := Scan(dir, ScanOptions{Me: "bob"}, wm)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Messages) != 0 {
		t.Fatalf("second scan got %d, want 0 (watermark suppression)", len(res2.Messages))
	}

	// Same-second-burst: a new record at the watermark TS must still
	// surface — distinguished by id, not ts.
	writeLine(t, dir, &Message{
		From: "alice", To: "bob", Body: "c2", Thread: "fixed",
		TS: now.Unix() + 2,
	})
	res3, err := Scan(dir, ScanOptions{Me: "bob"}, wm)
	if err != nil {
		t.Fatal(err)
	}
	if len(res3.Messages) != 1 {
		t.Fatalf("same-second-burst scan got %d, want 1", len(res3.Messages))
	}
	if res3.Messages[0].Body != "c2" {
		t.Errorf("expected body c2, got %q", res3.Messages[0].Body)
	}
	if !contains(res3.IDsAtMaxTS, wm.IDs[0]) {
		t.Errorf("IDsAtMaxTS dropped prior id %q; got %v", wm.IDs[0], res3.IDsAtMaxTS)
	}
}

// TestScan_LegacyRecordsRecomputeID exercises the SPEC.md §3 reader
// fallback: records emitted before the id field existed must have their
// id recomputed on the fly.
func TestScan_LegacyRecordsRecomputeID(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"ts":1700000000,"from":"alice","to":"bob","thread":"t1","body":"hello"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "log-alice.jsonl"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(dir, ScanOptions{Me: "bob"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("got %d, want 1", len(res.Messages))
	}
	want, _ := ComputeID(1700000000, "alice", "bob", "t1", "hello")
	if res.Messages[0].ID != want {
		t.Errorf("recomputed id %q, want %q", res.Messages[0].ID, want)
	}
}

func TestLatestTo(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	for i, body := range []string{"first", "second", "third"} {
		writeLine(t, dir, &Message{
			From: "alice", To: "bob", Body: body, Thread: "t",
			TS: now.Unix() + int64(i),
		})
	}
	last, err := LatestTo(dir, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if last == nil {
		t.Fatal("LatestTo returned nil")
	}
	if last.Body != "third" {
		t.Errorf("LatestTo body %q, want third", last.Body)
	}
}

func TestUnreadCount(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	for i, to := range []string{"bob", "bob", "carol", "bob"} {
		writeLine(t, dir, &Message{
			From: "alice", To: to, Body: "msg", Thread: "t",
			TS: now.Unix() + int64(i),
		})
	}
	if n, err := UnreadCount(dir, "bob"); err != nil || n != 3 {
		t.Fatalf("UnreadCount(bob) = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := UnreadCount(dir, "carol"); err != nil || n != 1 {
		t.Fatalf("UnreadCount(carol) = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := UnreadCount(dir, "dave"); err != nil || n != 0 {
		t.Fatalf("UnreadCount(dave) = (%d, %v), want (0, nil)", n, err)
	}
}

// TestUnreadCount_HonorsAgentWatermark verifies agent-deck's badge falls
// to zero once the agent advances its own .seen-<alias> file (e.g., via
// /message-inbox). agent-deck must NOT write that file itself.
func TestUnreadCount_HonorsAgentWatermark(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		writeLine(t, dir, &Message{
			From: "alice", To: "bob", Body: "msg", Thread: "t",
			TS: now.Unix() + int64(i),
		})
	}
	res, err := Scan(dir, ScanOptions{Me: "bob"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveWatermark(dir, "bob", &Watermark{TS: res.MaxTS, IDs: res.IDsAtMaxTS}); err != nil {
		t.Fatal(err)
	}
	if n, err := UnreadCount(dir, "bob"); err != nil || n != 0 {
		t.Fatalf("post-watermark UnreadCount(bob) = (%d, %v), want (0, nil)", n, err)
	}
	writeLine(t, dir, &Message{From: "alice", To: "bob", Body: "new", Thread: "t", TS: now.Unix() + 99})
	if n, err := UnreadCount(dir, "bob"); err != nil || n != 1 {
		t.Fatalf("post-arrival UnreadCount(bob) = (%d, %v), want (1, nil)", n, err)
	}
}

// TestUnreadCache_ShortCircuits proves the mtime cache actually skips
// Scan when (max_mtime, file_count) is unchanged.
func TestUnreadCache_ShortCircuits(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	writeLine(t, dir, &Message{From: "alice", To: "bob", Body: "hi", Thread: "t", TS: now.Unix()})

	var cache UnreadCache
	first, err := cache.Get(dir, "bob")
	if err != nil || first != 1 {
		t.Fatalf("first Get = (%d, %v), want (1, nil)", first, err)
	}

	logPath := filepath.Join(dir, "log-alice.jsonl")
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	origMtime := fi.ModTime()

	if err := os.WriteFile(logPath, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, origMtime, origMtime); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Get(dir, "bob"); err != nil || got != 1 {
		t.Fatalf("cached Get returned %d (err=%v); cache failed to short-circuit", got, err)
	}

	future := origMtime.Add(time.Second)
	if err := os.Chtimes(logPath, future, future); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Get(dir, "bob"); err != nil || got != 0 {
		t.Fatalf("post-invalidate Get = (%d, %v), want (0, nil)", got, err)
	}
}

func TestSaveLoadWatermark_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	wm := &Watermark{TS: 1700000000, IDs: []string{"abc123def4567890"}}
	if err := SaveWatermark(dir, "bob", wm); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWatermark(dir, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.TS != wm.TS || len(got.IDs) != 1 || got.IDs[0] != wm.IDs[0] {
		t.Fatalf("watermark round-trip failed: got %+v", got)
	}
}

func TestLoadWatermark_AbsentReturnsNil(t *testing.T) {
	dir := t.TempDir()
	wm, err := LoadWatermark(dir, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if wm != nil {
		t.Fatalf("expected nil watermark when absent, got %+v", wm)
	}
}

func TestMtimeCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := &MtimeCache{MaxMtime: 1700000123, Files: 4}
	if err := SaveMtimeCache(dir, "bob", c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadMtimeCache(dir, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != *c {
		t.Fatalf("mtime cache round-trip failed: got %+v want %+v", got, c)
	}
}

func TestCurrentMtime(t *testing.T) {
	dir := t.TempDir()
	for _, alias := range []string{"alice", "bob"} {
		path := filepath.Join(dir, "log-"+alias+".jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	max, n, err := CurrentMtime(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("file count %d, want 2", n)
	}
	if max == 0 {
		t.Errorf("max_mtime 0; expected non-zero")
	}
}
