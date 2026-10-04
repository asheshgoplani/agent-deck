package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2481 item 1: a dashboard re-read `session output --json` for every
// session every ~31 s, dead and idle ones included, and each read parsed the
// whole transcript and appended a read-log line although nothing had changed.
// `--json` now carries an opaque content_version for the transcript file the
// response was parsed from, and `--if-version <v>` answers {"unchanged":true}
// from a stat alone, without parsing the transcript or logging a read.

func issue2481ReadLogLines(t *testing.T, home string) int {
	t.Helper()
	var n int
	_ = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "session-output-reads.jsonl" {
			data, _ := os.ReadFile(path)
			n += strings.Count(string(data), "\n")
		}
		return nil
	})
	return n
}

func issue2481OutputJSON(t *testing.T, home string, args ...string) map[string]any {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, append([]string{"session", "output"}, args...)...)
	if code != 0 {
		t.Fatalf("session output %v failed (exit %d)\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("session output %v: not JSON: %v\n%s", args, err, stdout)
	}
	return out
}

func TestIssue2481_SessionOutputIfVersionSkipsUnchangedTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "proj")
	id := sessionMoveAddSession(t, home, project, "idle-worker")
	const claudeID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	if stdout, stderr, code := runAgentDeck(t, home, "session", "set", id, "claude-session-id", claudeID); code != 0 {
		t.Fatalf("set claude-session-id (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	resolved := project
	if r, err := filepath.EvalSymlinks(project); err == nil {
		resolved = r
	}
	projectsDir := filepath.Join(home, ".claude", "projects", session.ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeClaudeJSONL(t, projectsDir, claudeID, "hi", "FIRST REPLY", "2026-10-04T08:00:00Z")

	first := issue2481OutputJSON(t, home, id, "--json")
	if first["content"] != "FIRST REPLY" {
		t.Fatalf("first read content = %v, want FIRST REPLY", first["content"])
	}
	version, _ := first["content_version"].(string)
	if version == "" {
		t.Fatalf("first read carries no content_version: %v", first)
	}
	if _, ok := first["unchanged"]; ok {
		t.Fatalf("a full read must not carry unchanged: %v", first)
	}
	if got := issue2481ReadLogLines(t, home); got != 1 {
		t.Fatalf("read log lines after first read = %d, want 1", got)
	}

	// Unchanged transcript: answered without content and without a logged read.
	for range 3 {
		probe := issue2481OutputJSON(t, home, id, "--json", "--if-version", version)
		if probe["unchanged"] != true || probe["content_version"] != version {
			t.Fatalf("probe on unchanged transcript = %v, want unchanged with the same version", probe)
		}
		if _, ok := probe["content"]; ok {
			t.Fatalf("unchanged probe must not carry content: %v", probe)
		}
		if probe["session_id"] != id || probe["success"] != true {
			t.Fatalf("unchanged probe identity = %v", probe)
		}
	}
	if got := issue2481ReadLogLines(t, home); got != 1 {
		t.Fatalf("read log lines after 3 unchanged probes = %d, want 1 (probes are not reads)", got)
	}

	// The transcript moves on: the same probe now returns the new reply and a
	// new version, and that one is a logged read.
	writeClaudeJSONL(t, projectsDir, claudeID, "more", "SECOND REPLY, LONGER", "2026-10-04T08:05:00Z")
	next := issue2481OutputJSON(t, home, id, "--json", "--if-version", version)
	if next["content"] != "SECOND REPLY, LONGER" {
		t.Fatalf("read after change content = %v, want SECOND REPLY, LONGER", next["content"])
	}
	if v, _ := next["content_version"].(string); v == "" || v == version {
		t.Fatalf("read after change content_version = %q, want a new non-empty version (old %q)", v, version)
	}
	if _, ok := next["unchanged"]; ok {
		t.Fatalf("a changed read must not carry unchanged: %v", next)
	}
	if got := issue2481ReadLogLines(t, home); got != 2 {
		t.Fatalf("read log lines after changed read = %d, want 2", got)
	}
}

// Without a transcript file the response comes from a fallback whose source is
// not a versioned file: no content_version is emitted and --if-version can
// never short-circuit, so a caller can only ever skip a provably unchanged read.
func TestIssue2481_SessionOutputNoVersionWithoutTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	id := sessionMoveAddSession(t, home, filepath.Join(home, "proj"), "no-transcript")
	out := issue2481OutputJSON(t, home, id, "--json")
	if _, ok := out["content_version"]; ok {
		t.Fatalf("fallback read carries content_version: %v", out)
	}
	probe := issue2481OutputJSON(t, home, id, "--json", "--if-version", "v1-anything")
	if _, ok := probe["unchanged"]; ok {
		t.Fatalf("--if-version short-circuited without a versioned transcript: %v", probe)
	}
	if got := issue2481ReadLogLines(t, home); got != 2 {
		t.Fatalf("read log lines = %d, want 2 (both were real reads)", got)
	}
}

func TestIssue2481_SessionOutputIfVersionRequiresJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	id := sessionMoveAddSession(t, home, filepath.Join(home, "proj"), "flags")
	for _, args := range [][]string{
		{id, "--if-version", "v1-x"},
		{id, "--if-version", "v1-x", "-q"},
		{id, "--if-version", "v1-x", "--json", "--pane"},
		{id, "--if-version", "v1-x", "--json", "--copy"},
	} {
		stdout, stderr, code := runAgentDeck(t, home, append([]string{"session", "output"}, args...)...)
		if code == 0 {
			t.Fatalf("session output %v exited 0, want a usage error", args)
		}
		// --json errors go to stdout, text errors to stderr.
		if !strings.Contains(stdout+stderr, "--if-version") {
			t.Fatalf("session output %v error does not name --if-version\nstdout: %s\nstderr: %s", args, stdout, stderr)
		}
	}
}
