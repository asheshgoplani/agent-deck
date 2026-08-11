package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestChildrenOfFiltersByParent(t *testing.T) {
	a := &session.Instance{ID: "p"}
	b := &session.Instance{ID: "c1", ParentSessionID: "p"}
	c := &session.Instance{ID: "c2", ParentSessionID: "other"}
	d := &session.Instance{ID: "c3", ParentSessionID: "p"}
	archived := &session.Instance{ID: "archived", ParentSessionID: "p", ArchivedAt: time.Now()}
	got := childrenOf("p", []*session.Instance{a, b, c, d, archived})
	if len(got) != 2 {
		t.Fatalf("expected 2 children, got %d", len(got))
	}
	if got[0].ID != "c1" || got[1].ID != "c3" {
		t.Fatalf("unexpected children: %v %v", got[0].ID, got[1].ID)
	}
}

func TestSessionChildrenExcludesArchivedByDefaultWithOptIn(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	parentID := addTestSession(t, home, filepath.Join(home, "parent"), "parent")
	activeID := addTestSession(t, home, filepath.Join(home, "active"), "active-child")
	archivedID := addTestSession(t, home, filepath.Join(home, "archived"), "archived-child")
	for _, childID := range []string{activeID, archivedID} {
		if stdout, stderr, code := runAgentDeck(t, home,
			"session", "set-parent", childID, parentID, "--json"); code != 0 {
			t.Fatalf("set-parent %s failed (%d)\nstdout: %s\nstderr: %s", childID, code, stdout, stderr)
		}
	}
	forceSetStatus(t, home, activeID, session.StatusStopped)
	forceSetStatus(t, home, archivedID, session.StatusStopped)
	if stdout, stderr, code := runAgentDeck(t, home,
		"session", "archive", archivedID, "--json"); code != 0 {
		t.Fatalf("archive child failed (%d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	readChildren := func(args ...string) []map[string]interface{} {
		t.Helper()
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("agent-deck %v failed (%d)\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		var payload struct {
			Children []map[string]interface{} `json:"children"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("parse children JSON: %v\noutput: %s", err, stdout)
		}
		return payload.Children
	}

	active := readChildren("session", "children", parentID, "--json")
	if len(active) != 1 || active[0]["id"] != activeID {
		t.Fatalf("default children = %#v, want only active child %s", active, activeID)
	}
	if _, present := active[0]["archived"]; present {
		t.Fatalf("active child JSON gained an archived key: %#v", active[0])
	}

	all := readChildren("session", "children", parentID, "--include-archived", "--json")
	if len(all) != 2 {
		t.Fatalf("included children = %#v, want active and archived", all)
	}
	byID := make(map[string]map[string]interface{}, len(all))
	for _, row := range all {
		byID[row["id"].(string)] = row
	}
	if got := byID[archivedID]["archived"]; got != true {
		t.Fatalf("archived child marker = %#v, want true; row=%#v", got, byID[archivedID])
	}
	if _, present := byID[activeID]["archived"]; present {
		t.Fatalf("active child JSON gained an archived key under opt-in: %#v", byID[activeID])
	}

	readFollowSnapshots := func(extra ...string) map[string]map[string]interface{} {
		t.Helper()
		args := []string{"session", "children", parentID, "--follow", "--until-done", "--interval", "1ms", "--heartbeat", "0"}
		args = append(args, extra...)
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("agent-deck %v failed (%d)\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		snapshots := make(map[string]map[string]interface{})
		for _, line := range splitNonEmptyLines(stdout) {
			var event map[string]interface{}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("parse follow line %q: %v", line, err)
			}
			if event["event"] == "snapshot" {
				snapshots[event["id"].(string)] = event
			}
		}
		return snapshots
	}

	activeSnapshots := readFollowSnapshots()
	if len(activeSnapshots) != 1 || activeSnapshots[activeID] == nil || activeSnapshots[archivedID] != nil {
		t.Fatalf("default follow snapshots = %#v, want only active child", activeSnapshots)
	}
	allSnapshots := readFollowSnapshots("--include-archived")
	if len(allSnapshots) != 2 || allSnapshots[activeID] == nil || allSnapshots[archivedID] == nil {
		t.Fatalf("included follow snapshots = %#v, want active and archived children", allSnapshots)
	}
	if got := allSnapshots[archivedID]["archived"]; got != true {
		t.Fatalf("archived follow marker = %#v, want true", got)
	}
	if _, present := allSnapshots[activeID]["archived"]; present {
		t.Fatalf("active follow JSON gained an archived key under opt-in: %#v", allSnapshots[activeID])
	}
}

func splitNonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
