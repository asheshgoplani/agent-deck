package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue1607_CreateSessionTitleLock verifies that the full create-dialog
// path locks a nonblank user title while quick-create and blank-name paths keep
// their existing unlocked behavior. The lock must also block the later Claude
// title reconciliation that originally replaced the title with a worktree
// folder basename.
func TestIssue1607_CreateSessionTitleLock(t *testing.T) {
	tests := []struct {
		name       string
		title      string
		autoName   bool
		wantLocked bool
	}{
		{name: "explicit user title", title: "Release prep", wantLocked: true},
		{name: "automatic title", title: "quiet-otter", autoName: true},
		{name: "blank title", title: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := createIssue1607Session(t, tt.title, tt.autoName)
			if inst.TitleLocked != tt.wantLocked {
				t.Fatalf("TitleLocked = %v, want %v", inst.TitleLocked, tt.wantLocked)
			}
			if inst.GetAutoName() != tt.autoName {
				t.Fatalf("AutoName = %v, want %v", inst.GetAutoName(), tt.autoName)
			}

			if tt.wantLocked {
				home := t.TempDir()
				t.Setenv("HOME", home)
				session.ClearUserConfigCache()
				t.Cleanup(session.ClearUserConfigCache)
				seedIssue1607ClaudeName(t, home, "sid-1607", "agent-deck-feature-branch")

				if name, changed := inst.ReconcileTitleFromClaude("sid-1607"); changed || name != "" {
					t.Fatalf("ReconcileTitleFromClaude = (%q, %v), want no-op", name, changed)
				}
				if inst.Title != tt.title {
					t.Fatalf("Title = %q after reconcile, want %q", inst.Title, tt.title)
				}
			}
		})
	}
}

func createIssue1607Session(t *testing.T, title string, autoName bool) *session.Instance {
	t.Helper()
	h := &Home{}
	msg := h.createSessionInGroupWithWorktreeAndOptions(
		title,
		t.TempDir(),
		"sleep 30",
		"test",
		"", "", "",
		false,
		false,
		nil,
		nil,
		"",
		"",
		false,
		nil,
		"", "",
		"",
		autoName,
	)().(sessionCreatedMsg)
	if msg.err != nil {
		t.Fatalf("create session: %v", msg.err)
	}
	t.Cleanup(func() {
		if err := msg.instance.KillAndWait(); err != nil {
			t.Errorf("cleanup session: %v", err)
		}
	})
	return msg.instance
}

func seedIssue1607ClaudeName(t *testing.T, home, sessionID, name string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir Claude sessions: %v", err)
	}
	data, err := json.Marshal(map[string]any{"sessionId": sessionID, "name": name})
	if err != nil {
		t.Fatalf("marshal Claude session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1607.json"), data, 0o644); err != nil {
		t.Fatalf("write Claude session: %v", err)
	}
}
