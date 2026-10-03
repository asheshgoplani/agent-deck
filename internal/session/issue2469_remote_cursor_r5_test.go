package session

// Incremental remote talkback, review round 5: a remote's top-level conductor
// is suppressed by its own producer (self_conductor), so its journal never
// crosses to the --into conductor, exactly as the legacy export never ships
// it. A parentless worker that is not a conductor (orphan) keeps crossing. The
// stdin export takes runExec's MaxSessions-refusal retry (#2355).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// saveFixtureRegistry rewrites the default profile's registry so the export
// sees the fixture's current parent links and titles.
func saveFixtureRegistry(t *testing.T, f *turnTestFixture) {
	t.Helper()
	st, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveWithGroups([]*Instance{f.parent, f.child}, nil); err != nil {
		t.Fatal(err)
	}
}

// remoteTurnCounts drives one real producer turn on the fixture's child and
// reports what each export and a cursor drain into conductor-x make of it.
func remoteTurnCounts(t *testing.T, f *turnTestFixture) (journal, legacy, cursor, written, wakes int) {
	t.Helper()
	f.appendTurn(t, fxHuman("u0", "telegram: status?"), fxAssistantText("a0", "All lanes green."))
	f.d.recordTerminalTurns("default", f.byID, map[string]string{f.child.ID: "waiting"}, nil)
	lines, err := ReadTurnJournal(f.child.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	full, err := ExportPendingRecords()
	if err != nil {
		t.Fatal(err)
	}
	exp, err := ExportRecordsAfter(RemoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", wakeCountingDeps(t, &wakes))
	if err != nil {
		t.Fatal(err)
	}
	return len(lines), len(full), len(exp.Records), res.Written, wakes
}

// R4-1: a top-level conductor on the remote (no parent, or itself as parent).
// Its producer drops every turn on purpose; neither export may ship it, and a
// drain must neither write nor wake.
func TestIssue2469PR3R5_RemoteTopLevelConductorTurnsStayHome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parent func(*Instance) string
	}{
		{"no parent", func(*Instance) string { return "" }},
		{"self parent", func(c *Instance) string { return c.ID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			f.child.Title = "conductor-remotebox"
			f.child.ParentSessionID = tc.parent(f.child)
			saveFixtureRegistry(t, f)
			journal, legacy, cursor, written, wakes := remoteTurnCounts(t, f)
			if journal != 1 || legacy != 0 {
				t.Fatalf("setup: want journal=1 legacy export=0, got journal=%d legacy=%d", journal, legacy)
			}
			if cursor != 0 || written != 0 || wakes != 0 {
				t.Fatalf("a remote conductor's own turn crossed: cursor export=%d written=%d wakes=%d", cursor, written, wakes)
			}
		})
	}
}

// Control: a parentless worker without a conductor title (the `remote add
// --no-parent` shape) is an orphan, not a suppressed conductor. The legacy
// export has nothing for it; the cursor export ships its turn.
func TestIssue2469PR3R5_RemoteOrphanTurnsStillCross(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.ParentSessionID = ""
	saveFixtureRegistry(t, f)
	journal, legacy, cursor, written, wakes := remoteTurnCounts(t, f)
	if journal != 1 || legacy != 0 {
		t.Fatalf("setup: want journal=1 legacy export=0, got journal=%d legacy=%d", journal, legacy)
	}
	if cursor != 1 || written != 1 || wakes != 0 {
		t.Fatalf("an orphan's turn must cross (info, no wake): cursor export=%d written=%d wakes=%d", cursor, written, wakes)
	}
}

// The cursor still moves past a suppressed conductor's lines, so a conductor
// later parented under the cross-host conductor ships only its new turns.
func TestIssue2469PR3R5_ReparentedConductorShipsOnlyNewTurns(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.Title = "conductor-remotebox"
	f.child.ParentSessionID = ""
	saveFixtureRegistry(t, f)
	wakes := 0
	deps := wakeCountingDeps(t, &wakes)
	f.appendTurn(t, fxHuman("u0", "telegram: status?"), fxAssistantText("a0", "All lanes green."))
	statuses := map[string]string{f.child.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps)
	if err != nil || res.Written != 0 || res.CursorAfter == nil || res.CursorAfter.Seqs[f.child.ID] != 1 {
		t.Fatalf("drain 1: want nothing written and the cursor at seq 1, got %+v %v", res, err)
	}

	parentOnOtherHost(t, f)
	f.appendTurn(t, fxHuman("u1", "merge lane B"), fxAssistantText("a1", "Lane B merged."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	res, err = RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || len(res.Stored) != 1 || res.Stored[0].Seq != 2 {
		t.Fatalf("after reparent: want only seq 2, got written=%d stored=%+v", res.Written, res.Stored)
	}
}

// #2355: the stdin export is a read-only verb, so a ControlMaster refusal is
// retried on a dedicated connection with the same stdin, as Run's reads are.
func TestIssue2469PR3R5_StdinExportRetriesChannelExhaustion(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*)
    printf '{"records":[],"writer":{"running":true},"cursor_next":'
    cat
    printf '}\n'
    exit 0
    ;;
esac
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
exit 255
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cursor := RemoteCursor{Seqs: map[string]int64{"w1": 4, "w2": 9}}
	r := &SSHRunner{Host: "host-a.example.com"}
	exp, err := r.FetchRecordsAfter(context.Background(), cursor)
	if err != nil {
		t.Fatalf("a refused shared attempt must be retried: %v", err)
	}
	want, _ := json.Marshal(cursor)
	got, _ := json.Marshal(exp.CursorNext)
	if string(got) != string(want) || exp.Writer == nil || !exp.Writer.Running {
		t.Fatalf("the retry lost the stdin cursor: got %s want %s", got, want)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(calls) != 2 || strings.Contains(calls[0], "ControlPath=none") || !strings.Contains(calls[1], "ControlPath=none") {
		t.Fatalf("want a shared attempt then one dedicated retry, got %q", calls)
	}
}
