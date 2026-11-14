package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Issue #3030: ExactResponseContentVersion is the version the conditional
// archive fence compares. It must equal ResponseContentVersion (what
// `session output --json` hands out) for an owned transcript, and must be
// empty where only the loose resolver finds a same-id file.
func TestExactResponseContentVersion(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	write := func(project, id string) string {
		t.Helper()
		if err := os.MkdirAll(project, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(configDir, "projects", ConvertToClaudeDirName(project), id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{\"type\":\"assistant\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("owned transcript", func(t *testing.T) {
		const id = "3030bbbb-0000-4000-8000-000000000001"
		project := filepath.Join(home, "owned")
		want := write(project, id)
		inst := &Instance{ID: "w", Tool: "claude", ProjectPath: project, ClaudeSessionID: id}
		path, err := inst.ExactClaudeTranscriptPath(nil)
		if err != nil || path != want {
			t.Fatalf("ExactClaudeTranscriptPath = %q, %v; want %q", path, err, want)
		}
		exact, loose := inst.ExactResponseContentVersion(nil), inst.ResponseContentVersion(nil)
		if exact == "" || exact != loose {
			t.Fatalf("exact version %q must equal the session output version %q", exact, loose)
		}
	})

	t.Run("neighbour project only", func(t *testing.T) {
		const id = "3030bbbb-0000-4000-8000-000000000002"
		project := filepath.Join(home, "a")
		if err := os.MkdirAll(project, 0o700); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(home, "b"), id)
		inst := &Instance{ID: "w", Tool: "claude", ProjectPath: project, ClaudeSessionID: id}
		if inst.ResponseContentVersion(nil) == "" {
			t.Fatal("fixture: the loose resolver should find the neighbour file")
		}
		if _, err := inst.ExactClaudeTranscriptPath(nil); !errors.Is(err, errNoExactContextArtifact) {
			t.Fatalf("ExactClaudeTranscriptPath error = %v, want no exact artifact", err)
		}
		if v := inst.ExactResponseContentVersion(nil); v != "" {
			t.Fatalf("neighbour transcript produced exact version %q", v)
		}
	})

	t.Run("colliding live peer", func(t *testing.T) {
		const id = "3030bbbb-0000-4000-8000-000000000003"
		project := filepath.Join(home, "shared")
		write(project, id)
		inst := &Instance{ID: "w", Tool: "claude", ProjectPath: project, ClaudeSessionID: id}
		peer := &Instance{ID: "x", Tool: "claude", ProjectPath: project, ClaudeSessionID: id, Status: StatusRunning}
		if _, err := inst.ExactClaudeTranscriptPath([]*Instance{inst, peer}); err == nil {
			t.Fatal("colliding live peer was not refused")
		}
		if v := inst.ExactResponseContentVersion([]*Instance{inst, peer}); v != "" {
			t.Fatalf("colliding transcript produced exact version %q", v)
		}
	})
}
