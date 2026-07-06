package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// archivedFlag parses `list --json` and returns the archived flag for the
// session with the given id. Fails the test if the id is absent.
func archivedFlag(t *testing.T, home, id string) bool {
	t.Helper()
	listJSON := readSessionsJSON(t, home)
	var sessions []struct {
		ID       string `json:"id"`
		Archived bool   `json:"archived"`
	}
	if err := json.Unmarshal([]byte(listJSON), &sessions); err != nil {
		t.Fatalf("parse list --json: %v\njson: %s", err, listJSON)
	}
	for _, s := range sessions {
		if s.ID == id {
			return s.Archived
		}
	}
	t.Fatalf("session %s not found in list; json:\n%s", id, listJSON)
	return false
}

// TestSessionArchive_MarksArchived is the happy path: archiving a stopped
// session flags it archived without removing it from the registry.
func TestSessionArchive_MarksArchived(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "archive-basic")

	if archivedFlag(t, home, id) {
		t.Fatalf("session %s archived before archive command ran", id)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "archive", id, "--json")
	if code != 0 {
		t.Fatalf("session archive failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !archivedFlag(t, home, id) {
		t.Errorf("session %s not archived after archive command", id)
	}
}

// TestSessionUnarchive_ClearsArchived confirms unarchive reverses archive.
func TestSessionUnarchive_ClearsArchived(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "unarchive-basic")

	if _, stderr, code := runAgentDeck(t, home, "session", "archive", id, "--json"); code != 0 {
		t.Fatalf("archive setup failed (exit %d): %s", code, stderr)
	}
	if !archivedFlag(t, home, id) {
		t.Fatalf("archive setup did not take effect for %s", id)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "unarchive", id, "--json")
	if code != 0 {
		t.Fatalf("session unarchive failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if archivedFlag(t, home, id) {
		t.Errorf("session %s still archived after unarchive command", id)
	}
}

// TestSessionArchive_NotFound_Exit2 mirrors other resolve-by-id commands:
// an unknown session id exits 2.
func TestSessionArchive_NotFound_Exit2(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	// Seed one real session so storage exists but the target id is absent.
	addTestSession(t, home, filepath.Join(home, "proj"), "archive-notfound")

	_, _, code := runAgentDeck(t, home, "session", "archive", "does-not-exist", "--json")
	if code != 2 {
		t.Fatalf("expected exit 2 for unknown session, got %d", code)
	}
}

// TestSessionUnarchive_NotArchived_Rejected: unarchiving a session that is not
// archived is an error (mirrors WebMutator.UnarchiveSession).
func TestSessionUnarchive_NotArchived_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	id := addTestSession(t, home, filepath.Join(home, "proj"), "unarchive-noop")

	_, _, code := runAgentDeck(t, home, "session", "unarchive", id, "--json")
	if code != 1 {
		t.Fatalf("expected exit 1 (INVALID_OPERATION) unarchiving a non-archived session, got %d", code)
	}
}

// TestSessionArchive_AlreadyArchived_Rejected: archiving twice is an error so
// the caller notices the no-op rather than silently re-stamping.
func TestSessionArchive_AlreadyArchived_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	id := addTestSession(t, home, filepath.Join(home, "proj"), "archive-twice")

	if _, stderr, code := runAgentDeck(t, home, "session", "archive", id, "--json"); code != 0 {
		t.Fatalf("first archive failed (exit %d): %s", code, stderr)
	}
	_, _, code := runAgentDeck(t, home, "session", "archive", id, "--json")
	if code != 1 {
		t.Fatalf("expected exit 1 (INVALID_OPERATION) archiving an already-archived session, got %d", code)
	}
}

// A missing <id|title> is a usage error (exit 1), distinct from the NOT_FOUND
// exit 2 reserved for a genuinely unknown session.
func TestSessionArchive_MissingArg_Exit1(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	addTestSession(t, home, filepath.Join(home, "proj"), "archive-missing-arg")

	_, _, code := runAgentDeck(t, home, "session", "archive", "--json")
	if code != 1 {
		t.Fatalf("expected exit 1 for archive with no id, got %d", code)
	}
}

func TestSessionUnarchive_MissingArg_Exit1(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	addTestSession(t, home, filepath.Join(home, "proj"), "unarchive-missing-arg")

	_, _, code := runAgentDeck(t, home, "session", "unarchive", "--json")
	if code != 1 {
		t.Fatalf("expected exit 1 for unarchive with no id, got %d", code)
	}
}
