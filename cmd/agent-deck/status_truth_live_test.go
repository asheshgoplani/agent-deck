package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// forceSetStartedStatus is forceSetStatus for a session that did run: it
// also writes the persisted start stamp a real start leaves, so a reload
// does not read the row as never started. An errored row is a row that ran. Its tmux session never existed, so
// the live status of a running/starting row is the dead-pane error list
// shows.
func forceSetStartedStatus(t *testing.T, home, id string, status session.Status, startedAt time.Time) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer storage.Close()
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	found := false
	for _, inst := range instances {
		if inst.ID == id {
			inst.Status = status
			inst.LastStartedAt = startedAt
			// Created before it started, and past the 1.5 s start grace.
			inst.CreatedAt = startedAt.Add(-time.Minute)
			found = true
		}
	}
	if !found {
		t.Fatalf("instance %s not found", id)
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// listStatusOf returns the status `list --json` reports for id.
func listStatusOf(t *testing.T, home, id string) string {
	t.Helper()
	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	raw := readSessionsJSON(t, home)
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("parse list --json: %v\n%s", err, raw)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Status
		}
	}
	t.Fatalf("session %s not in list --json:\n%s", id, raw)
	return ""
}

// `session remove` refused a session `list` showed as error, quoting the
// stored status ("is in state 'running'"). It must judge by the status list
// shows.
func TestSessionRemove_JudgesTheStatusListShows(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	id := addTestSession(t, home, filepath.Join(home, "proj"), "died-while-running")
	forceSetStartedStatus(t, home, id, session.StatusRunning, time.Now().Add(-time.Hour))
	if got := listStatusOf(t, home, id); got != "error" {
		t.Fatalf("precondition: list shows %q, want error for a started session whose tmux is gone", got)
	}
	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code != 0 {
		t.Fatalf("session remove refused a session list shows as error (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}

// `session remove --all-errored` is a bulk delete: a row goes only when
// its stored status AND the status list shows are both error. A row that
// was added but never started (list shows idle) stays, and so does a row
// stored healthy whose tmux is gone for one sample (another process
// mid-restart), even though list shows it as error at that instant.
func TestSessionRemove_AllErroredJudgesTheStatusListShows(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	deadID := addTestSession(t, home, filepath.Join(home, "dead"), "died")
	forceSetStartedStatus(t, home, deadID, session.StatusError, time.Now().Add(-time.Hour))
	freshID := addTestSession(t, home, filepath.Join(home, "fresh"), "never-started")
	blinkID := addTestSession(t, home, filepath.Join(home, "blink"), "mid-restart")
	forceSetStartedStatus(t, home, blinkID, session.StatusIdle, time.Now().Add(-time.Hour))
	if got := listStatusOf(t, home, freshID); got != "idle" {
		t.Fatalf("list shows a never-started session as %q, want idle", got)
	}
	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json")
	if code != 0 {
		t.Fatalf("--all-errored failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, deadID) {
		t.Errorf("a session stored and shown as error was not removed:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, freshID) {
		t.Errorf("a never-started session was removed as errored:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, blinkID) {
		t.Errorf("a session stored idle was removed on one live sample:\n%s", listJSON)
	}
}

// `switch --json` listed candidates with their stored status while list
// showed another, and ranked on it.
func TestSwitch_CandidatesCarryTheStatusListShows(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	a := addTestSession(t, home, filepath.Join(home, "a"), "alpha api")
	b := addTestSession(t, home, filepath.Join(home, "b"), "alpha web")
	forceSetStartedStatus(t, home, a, session.StatusRunning, time.Now().Add(-time.Hour))
	forceSetStartedStatus(t, home, b, session.StatusRunning, time.Now().Add(-time.Hour))
	want := map[string]string{a: listStatusOf(t, home, a), b: listStatusOf(t, home, b)}

	stdout, stderr, _ := runAgentDeck(t, home, "switch", "alpha", "--json", "--no-attach")
	var res struct {
		Status     string `json:"status"`
		Candidates []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse switch --json: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("want 2 candidates, got %+v (stdout %s)", res.Candidates, stdout)
	}
	for _, c := range res.Candidates {
		if c.Status != want[c.ID] {
			t.Errorf("switch candidate %s has status %q, list shows %q", c.ID, c.Status, want[c.ID])
		}
	}
}

// The restart freshness guard skipped a restart with "session is starting"
// for a session list showed as error (its tmux was gone). A dead session
// must be restartable; the guard judges by the status list shows.
func TestSessionRestart_GuardJudgesTheStatusListShows(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexec sleep 120\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	id := addTestSession(t, home, filepath.Join(home, "proj"), "starting-but-gone")
	forceSetStartedStatus(t, home, id, session.StatusStarting, time.Now().Add(-5*time.Second))
	t.Cleanup(func() { _, _, _ = runAgentDeck(t, home, "session", "stop", id) })
	if got := listStatusOf(t, home, id); got != "error" {
		t.Fatalf("precondition: list shows %q, want error", got)
	}
	stdout, stderr, _ := runAgentDeck(t, home, "session", "restart", id, "--json")
	if strings.Contains(stdout, `"skipped": true`) || strings.Contains(stdout, `"skipped":true`) {
		t.Fatalf("restart of a session list shows as error was skipped by the freshness guard\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// The other half of the guard: a session that is genuinely fresh (live pane,
// started seconds ago) is still skipped, also after the 1.5 s start grace, so
// the verdict does not depend on how fast the restart runs.
func TestSessionRestart_GuardStillSkipsALiveFreshSession(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexec sleep 120\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	id := addTestSession(t, home, filepath.Join(home, "proj"), "fresh-and-live")
	t.Cleanup(func() { _, _, _ = runAgentDeck(t, home, "session", "stop", id) })
	if _, stderr, code := runAgentDeck(t, home, "session", "start", id); code != 0 {
		t.Fatalf("session start: exit %d: %s", code, stderr)
	}
	forceSetStartedStatus(t, home, id, session.StatusIdle, time.Now().Add(-5*time.Second))
	if got := listStatusOf(t, home, id); got == "error" || got == "stopped" {
		t.Fatalf("precondition: list shows %q for a live session", got)
	}
	stdout, stderr, _ := runAgentDeck(t, home, "session", "restart", id, "--json")
	if !strings.Contains(stdout, `"skipped": true`) && !strings.Contains(stdout, `"skipped":true`) {
		t.Fatalf("restart of a live session started seconds ago was not skipped by the freshness guard\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}
