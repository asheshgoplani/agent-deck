package testutil_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestReapStaleTmuxDirs covers the janitor for the socket-dir half of the
// 2026-08-10 temp-leak incident.
//
// IsolateTmuxSocket and ShortTmuxSocket create their dirs under a SHORT base
// (/tmp, for the darwin sun_path limit), not under $TMPDIR — so
// ReapStaleTempDirs, which only ever looks at os.TempDir(), never saw them. 738
// ad-tmux-* dirs had accumulated under /tmp, 685 of them empty.
//
// The rule is deliberately narrower than the $TMPDIR janitor: EMPTY directories
// only. A non-empty socket dir may hold a live tmux server, and unlinking a live
// socket does not kill the server — it strands it, holding a pty forever with no
// socket path left to reach it (2026-07-18 pty exhaustion). An empty dir is
// provably socket-free, which is the dominant case anyway.
func TestReapStaleTmuxDirs(t *testing.T) {
	base := t.TempDir()
	stale := time.Now().Add(-48 * time.Hour)

	mk := func(name string, age time.Time, contents string) string {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if contents != "" {
			if err := os.MkdirAll(filepath.Join(p, contents), 0o755); err != nil {
				t.Fatalf("populate %s: %v", name, err)
			}
		}
		if err := os.Chtimes(p, age, age); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
		return p
	}

	oldEmptyTmux := mk("ad-tmux-111", stale, "")
	oldEmptySock := mk("ad-sock-222", stale, "")
	freshEmpty := mk("ad-tmux-333", time.Now(), "")
	// Non-empty: tmux nests <dir>/tmux-<uid>/<socket>, so this shape is exactly
	// what a dir holding a (possibly live) server looks like.
	oldWithSocket := mk("ad-tmux-444", stale, "tmux-501")
	foreign := mk("some-other-tool-555", stale, "")
	// Shares a leading substring but belongs to a different, longer prefix.
	similar := mk("ad-tmuxish-666", stale, "")

	n := testutil.ReapStaleTmuxDirs(base, 24*time.Hour)
	if n != 2 {
		t.Fatalf("ReapStaleTmuxDirs removed %d entries, want 2", n)
	}
	for _, gone := range []string{oldEmptyTmux, oldEmptySock} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("stale empty socket dir survived: %s (%v)", gone, err)
		}
	}
	for _, keep := range []string{freshEmpty, foreign, similar} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("janitor removed %s, which it does not own or is too new: %v", keep, err)
		}
	}
	if _, err := os.Stat(oldWithSocket); err != nil {
		t.Errorf("janitor removed a NON-EMPTY socket dir (%s): %v.\n\n"+
			"A non-empty dir may hold a live tmux server. Unlinking its socket does "+
			"not kill the server — it strands it, holding a pty with no path left to "+
			"reach it (2026-07-18 pty exhaustion).", oldWithSocket, err)
	}
}

// TestReapStaleTmuxDirsRespectsOptOut keeps an audit run from destroying the
// evidence it is measuring.
func TestReapStaleTmuxDirsRespectsOptOut(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "ad-tmux-777")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	t.Setenv("AGENT_DECK_TEST_NO_TEMP_REAP", "1")
	if n := testutil.ReapStaleTmuxDirs(base, time.Hour); n != 0 {
		t.Fatalf("opt-out ignored: removed %d entries", n)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("opt-out ignored: %v", err)
	}
}
