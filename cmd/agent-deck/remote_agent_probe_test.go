package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// The in-process probe must produce exactly what `list --json` and `group
// list --json` print, since the TUI applies a pushed listing in place of a
// fetched one: same builders, same bytes.
func TestRemoteAgent_InProcessProbeMatchesCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"group", "create", "work", "--json"},
		{"group", "create", "old", "--parent", "work", "--json"},
		{"add", project, "--title", "probe-me", "--group", "work", "--no-parent", "--json"},
	} {
		if stdout, stderr, code := runAgentDeck(t, home, args...); code != 0 {
			t.Fatalf("%v failed (%d)\n%s\n%s", args, code, stdout, stderr)
		}
	}
	wantList, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list --json failed (%d): %s", code, stderr)
	}
	wantGroups, stderr, code := runAgentDeck(t, home, "group", "list", "--json")
	if code != 0 {
		t.Fatalf("group list --json failed (%d): %s", code, stderr)
	}

	// Same isolation runAgentDeck gives the subprocess, applied to this
	// process for the in-process probe.
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	probe, closeProbe, err := newRemoteAgentProbe("ch_support_test")
	if err != nil {
		t.Fatalf("newRemoteAgentProbe: %v", err)
	}
	defer closeProbe()
	// The probe is read-only: it relies on no global state DB being
	// registered in the agent process, and it must leave the stamp the
	// watcher compares untouched (finding 2: a probe that moved the stamp
	// would trigger itself).
	if statedb.GetGlobal() != nil {
		t.Fatal("the agent process must not register a global state DB")
	}
	dbPath, err := session.GetDBPathForProfile("ch_support_test")
	if err != nil {
		t.Fatal(err)
	}
	stampBefore := remoteAgentStamp(dbPath)
	gotList, gotGroups, err := probe()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, _, err := probe(); err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if got := remoteAgentStamp(dbPath); got != stampBefore {
		t.Fatalf("a probe must not write to the state DB: stamp %q -> %q", stampBefore, got)
	}
	if gotList != wantList {
		t.Errorf("probe list differs from `list --json`:\n--- probe\n%s\n--- cli\n%s", gotList, wantList)
	}
	if gotGroups != wantGroups {
		t.Errorf("probe groups differ from `group list --json`:\n--- probe\n%s\n--- cli\n%s", gotGroups, wantGroups)
	}
	if len(gotList) < 3 || gotList[0] != '[' {
		t.Fatalf("probe list is not a JSON array: %q", gotList)
	}
}
