package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #3030: the conditional archive fence must read the worker's own
// Claude transcript only, never a same-id file the loose resolver finds in a
// neighbouring project or in the process-wide config dir.

const (
	exactFenceDone    = "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"===AGENTDECK_DONE=== status=ok summary=complete\"}]}}\n"
	exactFencePending = "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"new prompt\"}}\n"
)

// exactFenceHome isolates HOME and the Claude config dir. The temp dir is
// resolved so macOS /var -> /private/var cannot change the project keys.
func exactFenceHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home
}

func writeExactFenceTranscript(t *testing.T, configDir, projectPath, sessionID, body string, mod time.Time) string {
	t.Helper()
	path := filepath.Join(configDir, "projects", session.ConvertToClaudeDirName(projectPath), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func mkdirExactFence(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConditionalArchiveRejectsNeighbourProjectTranscript(t *testing.T) {
	home := exactFenceHome(t)
	const id = "3030aaaa-0000-4000-8000-000000000001"
	projectA := mkdirExactFence(t, filepath.Join(home, "work", "a"))
	projectB := mkdirExactFence(t, filepath.Join(home, "work", "b"))
	// Worker A has no transcript under its own key; B holds a finished
	// conversation under the same id.
	writeExactFenceTranscript(t, filepath.Join(home, ".claude"), projectB, id, exactFenceDone, time.Now())
	inst := &session.Instance{ID: "worker-a", Tool: "claude", ParentSessionID: "p",
		ProjectPath: projectA, ClaudeSessionID: id, Status: session.StatusWaiting}
	// Whatever the loose resolver hands out (B's file) must not be accepted.
	for _, version := range []string{inst.ResponseContentVersion(nil), "v1-any"} {
		if err := validateConditionalArchive(inst, nil, "p", version); err == nil {
			t.Fatalf("neighbour project transcript accepted as worker A's completed turn (version %q)", version)
		}
	}
}

func TestConditionalArchiveRejectsProcessWideConfigDirTranscript(t *testing.T) {
	home := exactFenceHome(t)
	const id = "3030aaaa-0000-4000-8000-000000000002"
	project := mkdirExactFence(t, filepath.Join(home, "work", "a"))
	accountDir := mkdirExactFence(t, filepath.Join(home, "account-dir"))
	configPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	mkdirExactFence(t, filepath.Dir(configPath))
	config := "[groups.\"scoped\".claude]\nconfig_dir = \"" + accountDir + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	inst := &session.Instance{ID: "worker-a", Tool: "claude", ParentSessionID: "p", GroupPath: "scoped",
		ProjectPath: project, ClaudeSessionID: id, Status: session.StatusWaiting}
	if got := session.GetClaudeConfigDirForInstance(inst); got != accountDir {
		t.Fatalf("fixture: instance config dir = %q, want %q", got, accountDir)
	}
	// The exact project key, but in the process-wide dir, not the worker's.
	writeExactFenceTranscript(t, filepath.Join(home, ".claude"), project, id, exactFenceDone, time.Now())
	for _, version := range []string{inst.ResponseContentVersion(nil), "v1-any"} {
		if err := validateConditionalArchive(inst, nil, "p", version); err == nil {
			t.Fatalf("process-wide config dir transcript accepted for a scoped worker (version %q)", version)
		}
	}
}

func TestConditionalArchiveOwnedTranscriptMatchesSessionOutputVersion(t *testing.T) {
	home := exactFenceHome(t)
	const id = "3030aaaa-0000-4000-8000-000000000003"
	project := mkdirExactFence(t, filepath.Join(home, "work", "a"))
	writeExactFenceTranscript(t, filepath.Join(home, ".claude"), project, id, exactFenceDone, time.Now())
	inst := &session.Instance{ID: "worker-a", Tool: "claude", ParentSessionID: "p",
		ProjectPath: project, ClaudeSessionID: id, Status: session.StatusWaiting}
	// `session output --json` hands out ResponseContentVersion as
	// content_version; the parent passes it back as --expected-content-version.
	version := inst.ResponseContentVersion(nil)
	if version == "" {
		t.Fatal("fixture: owned transcript has no content version")
	}
	if err := validateConditionalArchive(inst, nil, "p", version); err != nil {
		t.Fatalf("owned transcript with the session output version refused: %v", err)
	}
}

func TestConditionalArchiveNewestOwnedCopyWins(t *testing.T) {
	home := exactFenceHome(t)
	const id = "3030aaaa-0000-4000-8000-000000000004"
	real := mkdirExactFence(t, filepath.Join(home, "work", "real"))
	link := filepath.Join(home, "work", "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// Claude may key the conversation by either spelling of the working
	// directory; both keys belong to this worker.
	configDir := filepath.Join(home, ".claude")
	old, recent := time.Now().Add(-time.Hour), time.Now()
	inst := &session.Instance{ID: "worker-a", Tool: "claude", ParentSessionID: "p",
		ProjectPath: link, ClaudeSessionID: id, Status: session.StatusWaiting}

	t.Run("newest copy finished", func(t *testing.T) {
		writeExactFenceTranscript(t, configDir, link, id, exactFenceDone+exactFencePending, old)
		writeExactFenceTranscript(t, configDir, real, id, exactFenceDone, recent)
		if err := validateConditionalArchive(inst, nil, "p", inst.ResponseContentVersion(nil)); err != nil {
			t.Fatalf("newest finished copy refused: %v", err)
		}
	})
	t.Run("newest copy pending", func(t *testing.T) {
		writeExactFenceTranscript(t, configDir, link, id, exactFenceDone, old)
		writeExactFenceTranscript(t, configDir, real, id, exactFenceDone+exactFencePending, recent)
		if err := validateConditionalArchive(inst, nil, "p", inst.ResponseContentVersion(nil)); err == nil {
			t.Fatal("stale finished copy accepted over a newer pending one")
		}
	})
}
