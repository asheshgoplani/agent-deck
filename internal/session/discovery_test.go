package session

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestDiscoverExistingTmuxSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Should not error even with no existing instances
	discovered, err := DiscoverExistingTmuxSessions([]*Instance{})
	if err != nil {
		t.Logf("DiscoverExistingTmuxSessions error (may be expected): %v", err)
	}
	_ = discovered
}

func TestDiscoverSkipsAgentDeckSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create a mock existing instance
	existing := []*Instance{
		{
			ID:          "test-123",
			Title:       "existing-session",
			ProjectPath: "/tmp",
		},
	}

	discovered, err := DiscoverExistingTmuxSessions(existing)
	if err != nil {
		t.Logf("Error (may be expected): %v", err)
	}

	// Should not include sessions that are already tracked
	for _, d := range discovered {
		if d.Title == "existing-session" {
			t.Error("Should not discover already tracked sessions")
		}
	}
}

func TestGroupByProjectDeep(t *testing.T) {
	instances := []*Instance{
		{Title: "s1", ProjectPath: "/home/user/projects/devops"},
		{Title: "s2", ProjectPath: "/home/user/projects/frontend"},
		{Title: "s3", ProjectPath: "/home/user/personal/blog"},
		{Title: "s4", ProjectPath: "/tmp"},
	}

	groups := GroupByProject(instances)

	// Check grouping
	if _, ok := groups["projects"]; !ok {
		t.Error("Expected 'projects' group")
	}
	if _, ok := groups["personal"]; !ok {
		t.Error("Expected 'personal' group")
	}
}

func TestFilterByQueryCaseInsensitive(t *testing.T) {
	instances := []*Instance{
		{Title: "DevOps-Claude", ProjectPath: "/tmp", Tool: "claude"},
		{Title: "frontend-shell", ProjectPath: "/tmp", Tool: "shell"},
	}

	// Should match case-insensitively
	result := FilterByQuery(instances, "DEVOPS")
	if len(result) != 1 {
		t.Errorf("Expected 1 result for 'DEVOPS', got %d", len(result))
	}

	result = FilterByQuery(instances, "Claude")
	if len(result) != 1 {
		t.Errorf("Expected 1 result for 'Claude', got %d", len(result))
	}
}

func TestDetectToolFromName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Claude uppercase", "CLAUDE-session", "claude"},
		{"claude lowercase", "my-claude-session", "claude"},
		{"Gemini mixed case", "Gemini-AI", "gemini"},
		{"OpenCode", "opencode-session", "opencode"},
		{"Codex", "codex-test", "codex"},
		{"Unknown", "random-session", "shell"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectToolFromName(tt.input)
			if result != tt.expected {
				t.Errorf("detectToolFromName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractProjectName(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"Deep path", "/home/user/projects/devops", "projects"},
		{"Home path", "/home/user/personal/blog", "personal"},
		{"Root level", "/tmp", "tmp"},
		{"Single level", "/home", "home"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractProjectName(tt.path)
			if result != tt.expected {
				t.Errorf("extractProjectName(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

// TestUntrackedTmuxSessions_FixtureTwoTrackedOneUntracked is finding 1's
// fixture (2026-09-18 live UI audit): a superseded "Restart with new session
// ID" source, or any other leftover, leaves a live agentdeck_-prefixed tmux
// session with no tracked Instance behind it. It must never inflate any
// aggregate (status --json, header counts, group previews — all now derive
// from session.VisibleInstances) but must still be discoverable so an
// operator can decide whether to clean it up (`doctor`/`health --json`).
//
// This spins up three REAL tmux sessions on the isolated test socket
// (TestMain's testutil.IsolateTmuxSocket): two tracked by Instance records,
// one deliberately not referenced by any Instance.
func TestUntrackedTmuxSessions_FixtureTwoTrackedOneUntracked(t *testing.T) {
	skipIfNoTmuxBinary(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	trackedOne := tmux.SessionPrefix + "tracked-one_" + suffix
	trackedTwo := tmux.SessionPrefix + "tracked-two_" + suffix
	leftover := tmux.SessionPrefix + "leftover-orphan_" + suffix

	for _, name := range []string{trackedOne, trackedTwo, leftover} {
		name := name
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "sh", "-c", "sleep 3600").CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %s: %v (%s)", name, err, out)
		}
		t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	}

	instances := []*Instance{
		{ID: "tracked-1", Title: "tracked-one"},
		{ID: "tracked-2", Title: "tracked-two"},
	}
	instances[0].SetTmuxSessionForTest(&tmux.Session{Name: trackedOne})
	instances[1].SetTmuxSessionForTest(&tmux.Session{Name: trackedTwo})

	// The tracked set (what list --json enumerates) is exactly these two —
	// counts derived from it must equal it, not the three live tmux sessions.
	if got := len(VisibleInstances(instances)); got != 2 {
		t.Fatalf("VisibleInstances = %d, want 2 (the tracked set)", got)
	}

	untracked, err := UntrackedTmuxSessions(instances)
	if err != nil {
		t.Fatalf("UntrackedTmuxSessions: %v", err)
	}
	var names []string
	for _, u := range untracked {
		names = append(names, u.Name)
	}
	foundLeftover, foundTrackedOne, foundTrackedTwo := false, false, false
	for _, n := range names {
		switch n {
		case leftover:
			foundLeftover = true
		case trackedOne:
			foundTrackedOne = true
		case trackedTwo:
			foundTrackedTwo = true
		}
	}
	if !foundLeftover {
		t.Fatalf("UntrackedTmuxSessions = %v, want it to include the leftover %q", names, leftover)
	}
	if foundTrackedOne || foundTrackedTwo {
		t.Fatalf("UntrackedTmuxSessions = %v, must not include tracked sessions %q/%q", names, trackedOne, trackedTwo)
	}
}
