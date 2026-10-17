package session

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #2674: the status pass ran the bootstrap $CODEX_HOME walk (bounded
// only by codexWalkDirTimeout = 5s) while holding i.mu, so every render-path
// GetStatusThreadSafe stalled behind it and the TUI froze.
func TestCodexBootstrapWalkDoesNotHoldStatusLock(t *testing.T) {
	for _, tc := range []struct {
		name        string
		supersede   bool
		wantBinding string
	}{
		{name: "publishes", wantBinding: "11111111-2222-3333-4444-555555555555"},
		{name: "superseded_by_hook", supersede: true, wantBinding: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			dayDir := filepath.Join(home, "sessions", "2026", "10", "17")
			if err := os.MkdirAll(dayDir, 0o755); err != nil {
				t.Fatal(err)
			}
			rollout := filepath.Join(dayDir, "rollout-2026-10-17T00-00-00-11111111-2222-3333-4444-555555555555.jsonl")
			if err := os.WriteFile(rollout, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			entered := make(chan struct{})
			release := make(chan struct{})
			origWalk, origPIDs := codexWalkDir, codexPaneProcessPIDs
			codexWalkDir = func(root string, fn fs.WalkDirFunc) error {
				close(entered)
				<-release
				return filepath.WalkDir(root, fn)
			}
			codexPaneProcessPIDs = func(*Instance) ([]int, error) { return nil, nil }
			t.Cleanup(func() { codexWalkDir, codexPaneProcessPIDs = origWalk, origPIDs })

			inst := &Instance{ID: "issue-2674", Tool: "codex", Status: StatusRunning,
				ProjectPath:      "/no-matching-rollout",
				CodexStartedAt:   time.Now().Add(-time.Hour).UnixMilli(),
				lastCodexProbeAt: time.Now(),
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				inst.mu.Lock()
				defer inst.mu.Unlock()
				inst.updateCodexSessionForPass(map[string]bool{}, false, &StatusUpdatePass{}, true)
			}()
			released := false
			t.Cleanup(func() {
				if !released {
					close(release)
				}
				<-done
			})

			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("bootstrap walk was not reached")
			}

			read := make(chan struct{})
			go func() { inst.GetStatusThreadSafe(); close(read) }()
			select {
			case <-read:
			case <-time.After(200 * time.Millisecond):
				t.Fatal("GetStatusThreadSafe blocked behind the Codex bootstrap walk")
			}

			if tc.supersede {
				inst.mu.Lock()
				inst.hookSessionID = "authoritative-hook-thread"
				inst.mu.Unlock()
			}

			close(release)
			released = true
			<-done

			inst.mu.RLock()
			got := inst.CodexSessionID
			inst.mu.RUnlock()
			if got != tc.wantBinding {
				t.Fatalf("CodexSessionID = %q, want %q", got, tc.wantBinding)
			}
		})
	}
}
