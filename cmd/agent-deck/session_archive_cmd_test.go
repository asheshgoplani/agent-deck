package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// archivedFlag parses the full inventory view and returns the archived flag
// for the session with the given id. The default list intentionally omits
// archived sessions.
func archivedFlag(t *testing.T, home, id string) bool {
	t.Helper()
	listJSON, stderr, code := runAgentDeck(t, home, "list", "--json", "--include-archived")
	if code != 0 {
		t.Fatalf("agent-deck list --include-archived --json failed (exit %d): %s", code, stderr)
	}
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

func TestList_ExcludesArchivedUnlessRequested(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	archivedID := addTestSession(t, home, filepath.Join(home, "archived-proj"), "archived-list")
	activeID := addTestSession(t, home, filepath.Join(home, "active-proj"), "active-list")

	if _, stderr, code := runAgentDeck(t, home, "session", "archive", archivedID, "--json"); code != 0 {
		t.Fatalf("archive setup failed (exit %d): %s", code, stderr)
	}

	assertListIDs := func(args ...string) map[string]bool {
		t.Helper()
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("agent-deck %v failed (exit %d): %s", args, code, stderr)
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("parse list JSON: %v\noutput: %s", err, stdout)
		}
		ids := make(map[string]bool, len(rows))
		for _, row := range rows {
			ids[row.ID] = true
		}
		return ids
	}

	active := assertListIDs("list", "--json")
	if !active[activeID] || active[archivedID] || len(active) != 1 {
		t.Errorf("default list ids = %v, want only active %s", active, activeID)
	}

	archived := assertListIDs("list", "--json", "--archived")
	if !archived[archivedID] || archived[activeID] || len(archived) != 1 {
		t.Errorf("archived list ids = %v, want only archived %s", archived, archivedID)
	}

	all := assertListIDs("list", "--json", "--include-archived")
	if !all[activeID] || !all[archivedID] || len(all) != 2 {
		t.Errorf("full list ids = %v, want active %s and archived %s", all, activeID, archivedID)
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
