package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	b, err := json.Marshal(map[string]any{"sessionId": sessionID, "name": name})
	if err != nil {
		t.Fatalf("marshal session fields: %v", err)
	}
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

// seedClaudeSessionFile writes ~/.claude/sessions/<file> with explicit fields,
// for tests that need several per-PID entries for the same sessionId.
func seedClaudeSessionFile(t *testing.T, home, file string, fields map[string]any) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal session fields: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), b, 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
}

// TestClaudeSessionNameIn_FreshestEntryWins: a resumed session leaves one
// per-PID file per run, all sharing the sessionId. The entry with the highest
// updatedAt is authoritative — not whichever sorts first in the directory.
func TestClaudeSessionNameIn_FreshestEntryWins(t *testing.T) {
	home := t.TempDir()
	// "1111.json" sorts before "2222.json"; the old behavior returned its name.
	seedClaudeSessionFile(t, home, "1111.json", map[string]any{
		"sessionId": "sid-x", "name": "stale plan title", "updatedAt": int64(1000),
	})
	seedClaudeSessionFile(t, home, "2222.json", map[string]any{
		"sessionId": "sid-x", "name": "current name", "updatedAt": int64(2000),
	})

	got := ClaudeSessionNameIn(filepath.Join(home, ".claude"), "sid-x")
	if got != "current name" {
		t.Errorf("ClaudeSessionNameIn = %q, want %q", got, "current name")
	}
}

// TestClaudeSessionNameIn_FreshestUnnamedSuppressesStaleName: when the live
// (freshest) process has no name, a stale named entry must not resurrect the
// old name.
func TestClaudeSessionNameIn_FreshestUnnamedSuppressesStaleName(t *testing.T) {
	home := t.TempDir()
	seedClaudeSessionFile(t, home, "1111.json", map[string]any{
		"sessionId": "sid-y", "name": "old name", "updatedAt": int64(1000),
	})
	seedClaudeSessionFile(t, home, "2222.json", map[string]any{
		"sessionId": "sid-y", "updatedAt": int64(2000),
	})

	if got := ClaudeSessionNameIn(filepath.Join(home, ".claude"), "sid-y"); got != "" {
		t.Errorf("ClaudeSessionNameIn = %q, want empty (freshest entry has no name)", got)
	}
}

// TestClaudeSessionNameIn_MtimeFallbackWhenNoUpdatedAt: entries without
// updatedAt (older Claude versions) fall back to file mtime for ordering.
func TestClaudeSessionNameIn_MtimeFallbackWhenNoUpdatedAt(t *testing.T) {
	home := t.TempDir()
	seedClaudeSessionFile(t, home, "1111.json", map[string]any{
		"sessionId": "sid-z", "name": "older",
	})
	seedClaudeSessionFile(t, home, "2222.json", map[string]any{
		"sessionId": "sid-z", "name": "newer",
	})
	dir := filepath.Join(home, ".claude", "sessions")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "1111.json"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if got := ClaudeSessionNameIn(filepath.Join(home, ".claude"), "sid-z"); got != "newer" {
		t.Errorf("ClaudeSessionNameIn = %q, want %q", got, "newer")
	}
}

// TestClaudeSessionNameIn_DerivedNameIgnored: Claude Code 2.1.19x auto-derives a
// session name from the cwd folder and stamps nameSource="derived". That is not a
// user rename, so the title sync (#572) must ignore it — otherwise every quick
// session's auto_name handle gets clobbered by the folder name.
func TestClaudeSessionNameIn_DerivedNameIgnored(t *testing.T) {
	home := t.TempDir()
	seedClaudeSessionFile(t, home, "1234.json", map[string]any{
		"sessionId": "sid-d", "name": "doozyx-apps-5d", "nameSource": "derived", "updatedAt": int64(1000),
	})

	if got := ClaudeSessionNameIn(filepath.Join(home, ".claude"), "sid-d"); got != "" {
		t.Errorf("ClaudeSessionNameIn = %q, want empty (derived names are not user renames)", got)
	}
}

// TestClaudeSessionNameIn_DerivedFreshestSuppressesStaleUserName: a freshest
// derived entry must suppress an older user-chosen name (mirrors the
// freshest-unnamed-suppresses rule) so the sync neither resurrects the stale
// name nor adopts the derived one.
func TestClaudeSessionNameIn_DerivedFreshestSuppressesStaleUserName(t *testing.T) {
	home := t.TempDir()
	seedClaudeSessionFile(t, home, "1111.json", map[string]any{
		"sessionId": "sid-e", "name": "my chosen name", "updatedAt": int64(1000),
	})
	seedClaudeSessionFile(t, home, "2222.json", map[string]any{
		"sessionId": "sid-e", "name": "ui-c1", "nameSource": "derived", "updatedAt": int64(2000),
	})

	if got := ClaudeSessionNameIn(filepath.Join(home, ".claude"), "sid-e"); got != "" {
		t.Errorf("ClaudeSessionNameIn = %q, want empty (derived freshest suppresses stale user name)", got)
	}
}

// TestClaudeSessionNameIn_UserNameSourceHonored: an explicit nameSource="user"
// (claude --name / /rename) is still synced.
func TestClaudeSessionNameIn_UserNameSourceHonored(t *testing.T) {
	home := t.TempDir()
	seedClaudeSessionFile(t, home, "1234.json", map[string]any{
		"sessionId": "sid-u", "name": "Sprint Planning", "nameSource": "user", "updatedAt": int64(1000),
	})

	if got := ClaudeSessionNameIn(filepath.Join(home, ".claude"), "sid-u"); got != "Sprint Planning" {
		t.Errorf("ClaudeSessionNameIn = %q, want %q", got, "Sprint Planning")
	}
}

// TestClaudeSessionNameIn_NoNameSourceBackCompat: older Claude versions wrote a
// name with no nameSource field; those were always user-set, so honor them.
func TestClaudeSessionNameIn_NoNameSourceBackCompat(t *testing.T) {
	home := t.TempDir()
	seedClaudeSessionFile(t, home, "1234.json", map[string]any{
		"sessionId": "sid-b", "name": "legacy name", "updatedAt": int64(1000),
	})

	if got := ClaudeSessionNameIn(filepath.Join(home, ".claude"), "sid-b"); got != "legacy name" {
		t.Errorf("ClaudeSessionNameIn = %q, want %q (no nameSource = legacy user name)", got, "legacy name")
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
