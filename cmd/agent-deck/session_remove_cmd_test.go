package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// addTestSession adds a session under the isolated HOME and returns its id.
// Mirrors sessionMoveAddSession but without the claude-project seeding side
// effect, so call sites that want a seeded transcript dir can do it
// separately via seedClaudeProjectDir.
func addTestSession(t *testing.T, home, workPath, title string) string {
	t.Helper()
	if err := os.MkdirAll(workPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", title,
		"-c", "claude",
		"--no-parent",
		"--json",
		workPath,
	)
	if code != 0 {
		t.Fatalf("agent-deck add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if resp.ID == "" {
		t.Fatalf("add returned empty id")
	}
	return resp.ID
}

// forceSetStatus opens storage directly under the isolated HOME and writes
// the target status onto the named instance. We can't use `agent-deck
// session set` because it doesn't accept status as a settable field (see
// handleSessionSet validFields map). Direct storage mutation is the
// standard test pattern for driving the registry into a specific state
// without racing the status worker.
func forceSetStatus(t *testing.T, home, id string, status session.Status) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")

	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var target *session.Instance
	for _, inst := range instances {
		if inst.ID == id {
			target = inst
			break
		}
	}
	if target == nil {
		t.Fatalf("instance %s not found (had %d instances)", id, len(instances))
	}
	target.Status = status
	tree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// TestSessionRemove_StoppedSessionSucceeds is the happy path.
func TestSessionRemove_StoppedSessionSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-basic")
	forceSetStatus(t, home, id, session.StatusStopped)

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "remove", id, "--json",
	)
	if code != 0 {
		t.Fatalf("session remove failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, id) {
		t.Errorf("session %s still present after remove; list:\n%s", id, listJSON)
	}
}

// TestSessionRemove_RunningWithoutForce_Rejected enforces the safety gate.
func TestSessionRemove_RunningWithoutForce_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-running")
	forceSetStatus(t, home, id, session.StatusRunning)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code == 0 {
		t.Fatalf("expected non-zero exit for running-without-force; stdout=%s stderr=%s", stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, id) {
		t.Errorf("running session was removed without --force; list:\n%s", listJSON)
	}
}

// TestSessionRemove_RunningWithForce_Succeeds confirms --force bypasses the gate.
func TestSessionRemove_RunningWithForce_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-forced")
	forceSetStatus(t, home, id, session.StatusRunning)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--force", "--json")
	if code != 0 {
		t.Fatalf("--force remove failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, id) {
		t.Errorf("forced remove did not take effect; list:\n%s", listJSON)
	}
}

// TestSessionRemove_AllErrored_RemovesOnlyErrored — bulk path respects
// status filtering. Non-errored sessions must NOT be touched.
func TestSessionRemove_AllErrored_RemovesOnlyErrored(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	errID := addTestSession(t, home, filepath.Join(home, "err-proj"), "err-one")
	forceSetStatus(t, home, errID, session.StatusError)
	stoppedID := addTestSession(t, home, filepath.Join(home, "stop-proj"), "stopped-one")
	forceSetStatus(t, home, stoppedID, session.StatusStopped)
	idleID := addTestSession(t, home, filepath.Join(home, "idle-proj"), "idle-one")
	forceSetStatus(t, home, idleID, session.StatusIdle)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json")
	if code != 0 {
		t.Fatalf("--all-errored failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, errID) {
		t.Errorf("errored session was NOT removed; list:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, stoppedID) {
		t.Errorf("stopped session got removed by --all-errored (over-broad); list:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, idleID) {
		t.Errorf("idle session got removed by --all-errored (over-broad); list:\n%s", listJSON)
	}
}

// TestSessionRemove_PreservesTranscripts is the hard invariant: registry
// removal must NOT touch ~/.claude/projects/<slug>/.
func TestSessionRemove_PreservesTranscripts(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-transcript")
	forceSetStatus(t, home, id, session.StatusStopped)

	// Seed the Claude transcript dir with a sentinel file.
	transcriptDir := seedClaudeProjectDir(t, home, workPath, "sentinel-transcript")
	sentinelPath := filepath.Join(transcriptDir, "abc-123.jsonl")

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code != 0 {
		t.Fatalf("remove failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Errorf("transcript sentinel missing after remove: %v", err)
	}
}

// TestSessionRemove_NotFound_Exit2 mirrors `session stop`'s convention.
func TestSessionRemove_NotFound_Exit2(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	_, _, code := runAgentDeck(t, home, "session", "remove", "does-not-exist", "--json")
	if code != 2 {
		t.Fatalf("expected exit 2 for not-found, got %d", code)
	}
}
