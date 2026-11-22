package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

func writePiSessionDoc(t *testing.T, home, instID, name, id, cwd string) string {
	t.Helper()
	dir := filepath.Join(home, ".pi", "agent-deck", instID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(`{"type":"session","version":3,"id":"`+id+`","cwd":"`+cwd+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A Pi session's live transcript is its exact session document, so the
// status bus, the send queue and the rows reader all find it.
func TestLiveTranscriptPathPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	inst := &Instance{ID: "pi-live", Tool: "pi", ProjectPath: project}
	if got := LiveTranscriptPath(inst, nil); got != "" {
		t.Fatalf("no session document yet: got %q", got)
	}
	first := writePiSessionDoc(t, home, inst.ID, "2026-11-16T00-00-00-000Z_a.jsonl", "pi-a", project)
	if got := LiveTranscriptPath(inst, nil); got != first {
		t.Fatalf("single document: got %q, want %q", got, first)
	}
	writePiSessionDoc(t, home, inst.ID, "2026-11-16T00-01-00-000Z_b.jsonl", "pi-b", project)
	if got := LiveTranscriptPath(inst, nil); got != "" {
		t.Fatalf("two documents and no persisted identity is ambiguous: got %q", got)
	}
	inst.PiSessionID, inst.PiSessionPath = "pi-a", first
	if got := LiveTranscriptPath(inst, nil); got != first {
		t.Fatalf("persisted identity: got %q, want %q", got, first)
	}
	foreign := &Instance{ID: "pi-foreign", Tool: "pi", ProjectPath: project}
	writePiSessionDoc(t, home, foreign.ID, "x.jsonl", "pi-x", "/somewhere/else")
	if got := LiveTranscriptPath(foreign, nil); got != "" {
		t.Fatalf("a document for another directory: got %q", got)
	}
}

// BenchmarkLiveTranscriptPath times the per-session lookup the status bus
// runs on each pass, for Claude and for a Pi session with a persisted
// identity.
func BenchmarkLiveTranscriptPath(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	project := filepath.Join(home, "project")
	claude := &Instance{ID: "c1", Tool: "claude", ProjectPath: project, ClaudeSessionID: "conv-1"}
	cpath := ClaudeTranscriptPathForInstance(claude)
	_ = os.MkdirAll(filepath.Dir(cpath), 0o700)
	_ = os.WriteFile(cpath, []byte("{}\n"), 0o600)
	pi := &Instance{ID: "pi-bench", Tool: "pi", ProjectPath: project}
	dir := filepath.Join(home, ".pi", "agent-deck", pi.ID)
	_ = os.MkdirAll(dir, 0o700)
	ppath := filepath.Join(dir, "s.jsonl")
	_ = os.WriteFile(ppath, []byte(`{"type":"session","version":3,"id":"pi-s","cwd":"`+project+`"}`+"\n"), 0o600)
	pi.PiSessionID, pi.PiSessionPath = "pi-s", ppath
	for _, inst := range []*Instance{claude, pi} {
		b.Run(inst.Tool, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if LiveTranscriptPath(inst, nil) == "" {
					b.Fatal("no path")
				}
			}
		})
	}
}

// With several session files and no saved identity, LiveRowsSource says
// so instead of a bare "no native transcript", and the native id is the
// session header's when there is one document.
func TestLiveRowsSourcePiDocuments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	inst := &Instance{ID: "pi-rows", Tool: "pi", Title: "pi rows", ProjectPath: project}
	first := writePiSessionDoc(t, home, inst.ID, "a.jsonl", "pi-header-id", project)
	src, sess, err := LiveRowsSource(inst, nil)
	if err != nil || src.Path != first || sess.NativeID != "pi-header-id" || sess.Harness != "pi" {
		t.Fatalf("one document: src=%+v sess=%+v err=%v", src, sess, err)
	}
	writePiSessionDoc(t, home, inst.ID, "b.jsonl", "pi-other", project)
	// A follow that started on one document learns it is no longer the only one.
	if _, err := src.Resolve(); !errors.Is(err, query.ErrSourceUnresolved) {
		t.Fatalf("follow resolve after a second document: err = %v", err)
	}
	_, _, err = LiveRowsSource(inst, nil)
	if !errors.Is(err, ErrNoNativeTranscript) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("two documents: err = %v", err)
	}
}

// TestPiFollowError: only a resolve failure that means the document changed
// ends a Pi follow. An I/O error on a path that still exists is retried.
func TestPiFollowError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		resync bool
	}{
		{"too many open files", fmt.Errorf("read Pi instance session directory: %w", &fs.PathError{Op: "open", Path: "/d", Err: syscall.EMFILE}), false},
		{"read error", fmt.Errorf("read Pi session: %w", &fs.PathError{Op: "read", Path: "/d/a.jsonl", Err: syscall.EIO}), false},
		{"persisted document removed", fmt.Errorf("inspect Pi session: %w", &fs.PathError{Op: "lstat", Path: "/d/a.jsonl", Err: fs.ErrNotExist}), true},
		{"several documents", errors.New(`ambiguous Pi context artifacts for instance "pi" (2 matches)`), true},
		{"no document", errors.New(`no exact Pi context artifact found for instance "pi"`), true},
		{"identity mismatch", errors.New(`persisted Pi session identity mismatch: expected "a", file contains "b"`), true},
	}
	for _, tc := range cases {
		got := piFollowError(tc.err)
		if errors.Is(got, query.ErrSourceUnresolved) != tc.resync || !errors.Is(got, tc.err) {
			t.Errorf("%s: piFollowError(%v) = %v, want resync %v", tc.name, tc.err, got, tc.resync)
		}
	}
}
