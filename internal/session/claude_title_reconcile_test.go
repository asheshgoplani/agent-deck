package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// seedClaudeSession writes ~/.claude/sessions/<pid>.json under home with the
// given sessionId/name so ClaudeSessionName can resolve it.
func seedClaudeSession(t *testing.T, home, sessionID, name string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	b, _ := json.Marshal(map[string]any{"sessionId": sessionID, "name": name})
	if err := os.WriteFile(filepath.Join(dir, "1234.json"), b, 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
}

// TestReconcileTitleFromClaude_UpdatesAndWritesBadge: when Claude's name differs
// from the instance Title, reconcile updates Title, returns (name,true), and
// drops the badge-update file the attach-side watcher reads (#1114 on-attach).
func TestReconcileTitleFromClaude_UpdatesAndWritesBadge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	badgeDir := t.TempDir()
	t.Setenv("AGENTDECK_BADGE_UPDATES_DIR", badgeDir)

	seedClaudeSession(t, home, "sid-1", "Conduit Federation 2SP")

	inst := &Instance{ID: "i1", Title: "rustic-island", Tool: "claude"}
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_rustic_abcd1234"}

	name, changed := inst.ReconcileTitleFromClaude("sid-1")
	if !changed || name != "Conduit Federation 2SP" {
		t.Fatalf("ReconcileTitleFromClaude = (%q,%v), want (%q,true)", name, changed, "Conduit Federation 2SP")
	}
	if inst.Title != "Conduit Federation 2SP" {
		t.Errorf("Title = %q, want %q", inst.Title, "Conduit Federation 2SP")
	}
	got, err := os.ReadFile(filepath.Join(badgeDir, "agentdeck_rustic_abcd1234"))
	if err != nil {
		t.Fatalf("badge-update file missing: %v", err)
	}
	if string(got) != "Conduit Federation 2SP" {
		t.Errorf("badge-update file = %q, want %q", got, "Conduit Federation 2SP")
	}
}

// TestReconcileTitleFromClaude_NoopWhenEqual: a matching name is a no-op, with
// no badge-update file written (avoids a redundant OSC on every attach).
func TestReconcileTitleFromClaude_NoopWhenEqual(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	badgeDir := t.TempDir()
	t.Setenv("AGENTDECK_BADGE_UPDATES_DIR", badgeDir)

	seedClaudeSession(t, home, "sid-2", "already-set")
	inst := &Instance{ID: "i2", Title: "already-set", Tool: "claude"}
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_x"}

	if name, changed := inst.ReconcileTitleFromClaude("sid-2"); changed || name != "" {
		t.Errorf("got (%q,%v), want no-op", name, changed)
	}
	if _, err := os.Stat(filepath.Join(badgeDir, "agentdeck_x")); !os.IsNotExist(err) {
		t.Errorf("badge-update file written for unchanged title")
	}
}

// TestReconcileTitleFromClaude_NoopWhenLocked: TitleLocked blocks the sync (#697).
func TestReconcileTitleFromClaude_NoopWhenLocked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedClaudeSession(t, home, "sid-3", "auto-name")

	inst := &Instance{ID: "i3", Title: "SCRUM-351", TitleLocked: true, Tool: "claude"}
	if _, changed := inst.ReconcileTitleFromClaude("sid-3"); changed {
		t.Errorf("locked title changed")
	}
	if inst.Title != "SCRUM-351" {
		t.Errorf("Title = %q, want unchanged SCRUM-351", inst.Title)
	}
}

// TestReconcileTitleFromClaude_NoopWhenSyncDisabled: sync_title=false opts out.
func TestReconcileTitleFromClaude_NoopWhenSyncDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("sync_title = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedClaudeSession(t, home, "sid-4", "should-not-apply")

	inst := &Instance{ID: "i4", Title: "loupe", Tool: "claude"}
	if _, changed := inst.ReconcileTitleFromClaude("sid-4"); changed {
		t.Errorf("title changed despite sync_title=false")
	}
	if inst.Title != "loupe" {
		t.Errorf("Title = %q, want unchanged loupe", inst.Title)
	}
}

// TestReconcileTitleFromClaude_NoopWhenNoName: no Claude session file → no-op.
func TestReconcileTitleFromClaude_NoopWhenNoName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &Instance{ID: "i5", Title: "keep-me", Tool: "claude"}
	if _, changed := inst.ReconcileTitleFromClaude("no-such-sid"); changed {
		t.Errorf("title changed with no Claude name available")
	}
	if inst.Title != "keep-me" {
		t.Errorf("Title = %q, want unchanged keep-me", inst.Title)
	}
}
