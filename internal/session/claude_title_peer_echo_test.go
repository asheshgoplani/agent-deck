package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeClaudeSessionFile(t *testing.T, claudeDir, pid string, fields map[string]any) {
	t.Helper()
	dir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, pid+".json"), data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
}

// ResolveTitleFromClaude must ignore the `--name` address agent-deck itself
// passed at launch. Claude 2.1.237 writes it back with no nameSource, so
// without the peer-echo guard the deck renames the session to its own handle.
func TestResolveTitleFromClaude_IgnoresPeerNameEcho(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("HOME", claudeDir)
	writeClaudeSessionFile(t, filepath.Join(claudeDir, ".claude"), "4242", map[string]any{
		"sessionId": "sid-echo",
		"name":      "pearly-mesa-ce08010d",
		"updatedAt": int64(1000),
	})

	inst := &Instance{ID: "ce08010d-1787223144", Title: "pearly-mesa-ce08010d"}
	if name, ok := inst.ResolveTitleFromClaude("sid-echo"); ok {
		t.Fatalf("peer-name echo accepted as a rename: %q", name)
	}
}

// The compounding case: the title already carries one suffix, so the address
// the deck passes carries two. Syncing it back is what grew
// "pale-swallow-07cc4f4e-07cc4f4e".
func TestResolveTitleFromClaude_IgnoresCompoundedPeerNameEcho(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("HOME", claudeDir)
	writeClaudeSessionFile(t, filepath.Join(claudeDir, ".claude"), "4243", map[string]any{
		"sessionId": "sid-grow",
		"name":      "pale-swallow-07cc4f4e-07cc4f4e",
		"updatedAt": int64(1000),
	})

	inst := &Instance{ID: "07cc4f4e-1787223144", Title: "pale-swallow-07cc4f4e"}
	if name, ok := inst.ResolveTitleFromClaude("sid-grow"); ok {
		t.Fatalf("compounded peer-name echo accepted as a rename: %q", name)
	}
}

// The point of the guard is to let REAL names through: once Claude derives a
// task name it no longer ends in the instance's id suffix, so it must sync.
func TestResolveTitleFromClaude_AcceptsDerivedTaskName(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("HOME", claudeDir)
	writeClaudeSessionFile(t, filepath.Join(claudeDir, ".claude"), "4244", map[string]any{
		"sessionId": "sid-real",
		"name":      "debug-claude-startup-performance",
		"updatedAt": int64(1000),
	})

	inst := &Instance{ID: "1554adf0-1787223144", Title: "violet-fox-1554adf0"}
	name, ok := inst.ResolveTitleFromClaude("sid-real")
	if !ok {
		t.Fatal("a Claude-derived task name must still sync into the title")
	}
	if name != "debug-claude-startup-performance" {
		t.Fatalf("synced name = %q, want the derived task name", name)
	}
}
