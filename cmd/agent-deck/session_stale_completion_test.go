package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Stale-completion suite.
//
// The completion ledger is last-wins per child and has no notion of WHICH work
// a completion answers. A supervisor that sends round-2 work to a child which
// already reported "done" for round 1 therefore reads the round-1 report as the
// answer to round 2 — the child appears to replay an old completion message
// instead of doing the new work, and `--until-done` returns immediately.
//
// The discriminator is the last_sent_at clock `session send` stamps: a
// completion older than the last delivery answers earlier work.

func TestCompletionIsStale(t *testing.T) {
	base := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		finishedAt time.Time
		lastSentAt time.Time
		want       bool
	}{
		{"completion after the send is fresh", base.Add(time.Hour), base, false},
		{"completion before the send is stale", base, base.Add(time.Hour), true},
		{"never sent to: nothing to compare", base, time.Time{}, false},
		{"no completion timestamp: not provably old", time.Time{}, base, false},
		{
			"within the second-granularity grace: still the previous turn",
			base.Add(500 * time.Millisecond), base,
			true,
		},
		{
			"comfortably past the grace: a real answer",
			base.Add(2 * time.Second), base,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := completionIsStale(tc.finishedAt, tc.lastSentAt); got != tc.want {
				t.Errorf("completionIsStale(%s, %s) = %v, want %v",
					tc.finishedAt, tc.lastSentAt, got, tc.want)
			}
		})
	}
}

func TestResponseIsStale(t *testing.T) {
	sent := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	if !responseIsStale("2026-07-15T11:00:00Z", sent) {
		t.Error("a response from an hour before the send must be stale")
	}
	if !responseIsStale("2026-07-15T11:59:59.123456789Z", sent) {
		t.Error("nanosecond-precision timestamps must parse and compare")
	}
	if responseIsStale("2026-07-15T12:30:00Z", sent) {
		t.Error("a response after the send is the answer, not a replay")
	}
	if responseIsStale("", sent) {
		t.Error("an absent timestamp is unknown, not stale")
	}
	if responseIsStale("not-a-timestamp", sent) {
		t.Error("an unparseable timestamp is unknown, not stale")
	}
	if responseIsStale("2026-07-15T11:00:00Z", time.Time{}) {
		t.Error("with no send on record there is nothing to be stale against")
	}
}

// The bug in one assertion: a child that reported done, then got new work, is
// NOT finished. Before the fix childTerminal returned true here and
// `--until-done` completed without the new work ever being done.
func TestChildTerminal_StaleDoneIsNotTerminal(t *testing.T) {
	fresh := childRow{Status: "idle", DoneStatus: "ok", DoneAt: "2026-07-15T11:00:00Z"}
	if !childTerminal(fresh) {
		t.Fatal("a fresh completion is terminal")
	}

	stale := fresh
	stale.DoneStale = true
	if childTerminal(stale) {
		t.Fatal("a completion that predates the child's newest work must not end supervision")
	}

	// A dead child stays terminal regardless: no one is coming back to finish.
	staleButStopped := stale
	staleButStopped.Status = "stopped"
	if !childTerminal(staleButStopped) {
		t.Fatal("a stopped child is terminal even with a stale completion")
	}

	if allChildrenTerminal([]childRow{fresh, stale}) {
		t.Fatal("a fleet with one un-answered child is not all-terminal")
	}
}

// A stale ledger entry must not surface as a `done` event: consumers key off
// event==done to collect results, and re-announcing the previous turn's report
// is the replay this fixes. The staleness still rides along on snapshots.
func TestDiffChildEvents_StaleDoneEmitsNoDoneEvent(t *testing.T) {
	prev := []childRow{{ID: "a", Title: "child-a", Status: "running"}}
	curr := []childRow{{ID: "a", Title: "child-a", Status: "running",
		DoneStatus: "ok", DoneSummary: "round 1 shipped", DoneAt: "2026-07-15T10:00:00Z",
		DoneStale: true, LastSentAt: "2026-07-15T11:00:00Z"}}

	if events := diffChildEvents(prev, curr); len(events) != 0 {
		t.Errorf("expected no done event for a stale completion, got %+v", events)
	}

	// Once the child answers the NEW work, the fresh entry emits done again.
	answered := []childRow{{ID: "a", Title: "child-a", Status: "running",
		DoneStatus: "ok", DoneSummary: "round 2 shipped", DoneAt: "2026-07-15T11:30:00Z",
		LastSentAt: "2026-07-15T11:00:00Z"}}
	events := diffChildEvents(curr, answered)
	want := []followEvent{{Event: "done", ID: "a", Title: "child-a",
		DoneStatus: "ok", DoneSummary: "round 2 shipped", DoneAt: "2026-07-15T11:30:00Z",
		LastSentAt: "2026-07-15T11:00:00Z"}}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("got %+v, want %+v", events, want)
	}
}

func TestSummarizeChildren_StaleDoneCountedSeparately(t *testing.T) {
	rows := []childRow{
		{ID: "a", Status: "idle", DoneStatus: "ok"},
		{ID: "b", Status: "idle", DoneStatus: "ok", DoneStale: true},
		{ID: "c", Status: "idle", DoneStatus: "fail", DoneStale: true},
	}
	got := summarizeChildren("heartbeat", rows)
	if got.DoneOK != 1 || got.DoneFail != 0 || got.DoneStale != 2 {
		t.Errorf("got done_ok=%d done_fail=%d done_stale=%d, want 1/0/2",
			got.DoneOK, got.DoneFail, got.DoneStale)
	}
}

// End-to-end through the real ledger + state DB: the row a supervisor polls
// carries the staleness verdict and the clock it was derived from.
func TestBuildChildRows_MarksCompletionsOlderThanTheLastSend(t *testing.T) {
	db := newTempStateDB(t)

	child := session.NewInstanceWithTool("stale-child", t.TempDir(), "claude")
	if err := db.SaveInstances([]*statedb.InstanceRow{{
		ID:    child.ID,
		Title: child.Title,
		Tool:  "claude",
	}}); err != nil {
		t.Fatalf("seed instance row: %v", err)
	}

	finishedAt := time.Now().Add(-10 * time.Minute)
	if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{
		ChildID:    child.ID,
		Status:     "ok",
		Summary:    "round 1 shipped",
		FinishedAt: finishedAt,
	}); err != nil {
		t.Fatalf("write ledger entry: %v", err)
	}

	// No delivery on record yet: the completion is all we know, so it stands.
	rows := buildChildRows([]*session.Instance{child}, db)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].DoneStale {
		t.Error("with no send on record the completion must not be marked stale")
	}
	if !childTerminal(rows[0]) {
		t.Error("an unchallenged completion is terminal")
	}

	// The supervisor now sends round-2 work. The round-1 report no longer
	// answers anything outstanding.
	sentAt := time.Now()
	if err := db.WriteLastSentAt(child.ID, sentAt.Unix()); err != nil {
		t.Fatalf("write last_sent_at: %v", err)
	}

	rows = buildChildRows([]*session.Instance{child}, db)
	if !rows[0].DoneStale {
		t.Fatal("a completion from before the newest delivery must be marked stale")
	}
	if rows[0].DoneSummary != "round 1 shipped" {
		t.Errorf("the report itself must still be visible, got %q", rows[0].DoneSummary)
	}
	if rows[0].LastSentAt == "" {
		t.Error("the row must carry the clock the verdict came from")
	}
	if childTerminal(rows[0]) {
		t.Fatal("a child with outstanding work is not terminal")
	}

	// And once it reports again, supervision can end.
	if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{
		ChildID:    child.ID,
		Status:     "ok",
		Summary:    "round 2 shipped",
		FinishedAt: sentAt.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("write second ledger entry: %v", err)
	}
	rows = buildChildRows([]*session.Instance{child}, db)
	if rows[0].DoneStale {
		t.Fatal("a completion after the delivery answers it")
	}
	if !childTerminal(rows[0]) {
		t.Fatal("the answered child is terminal")
	}
}

// buildChildRows must keep working without a state DB (no clock => no verdict),
// so callers that cannot open one degrade to the pre-existing behavior.
func TestBuildChildRows_NilStateDBNeverMarksStale(t *testing.T) {
	child := session.NewInstanceWithTool("no-db-child", t.TempDir(), "claude")
	if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{
		ChildID:    child.ID,
		Status:     "ok",
		Summary:    "done",
		FinishedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("write ledger entry: %v", err)
	}

	rows := buildChildRows([]*session.Instance{child}, nil)
	if len(rows) != 1 || rows[0].DoneStale || rows[0].LastSentAt != "" {
		t.Fatalf("without a state DB no staleness verdict is possible, got %+v", rows)
	}
}

// The hook-injected fleet summary rides on the same rows: a stale completion
// must not be announced to the parent as a result to collect.
func TestFormatChildrenContext_StaleCompletionIsNotAnnouncedAsDone(t *testing.T) {
	fresh := formatChildrenContext([]childRow{
		{ID: "a", Title: "child-a", Status: "idle", DoneStatus: "ok", DoneSummary: "round 1 shipped"},
	})
	if !strings.Contains(fresh, "completed: child-a → ok") {
		t.Fatalf("a fresh completion must be announced, got:\n%s", fresh)
	}

	stale := formatChildrenContext([]childRow{
		{ID: "a", Title: "child-a", Status: "running", DoneStatus: "ok",
			DoneSummary: "round 1 shipped", DoneStale: true},
	})
	if strings.Contains(stale, "completed:") {
		t.Fatalf("a stale completion must not be announced as a result, got:\n%s", stale)
	}
	if !strings.Contains(stale, "1 running") {
		t.Fatalf("the child should be counted by its live status, got:\n%s", stale)
	}
}
