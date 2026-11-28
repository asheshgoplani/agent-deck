package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// PR #3166 review (tarekrached, ntmspavan): when a nested worker's launcher
// is no longer registered, its inbox record falls back to the tree parent
// (reportTargetIDIn). The ledger must agree: the turn and status records'
// recipient and the observers of a send name the tree parent, not the
// removed launcher.
func TestIssue3165_GoneLauncherLedgerRecordsNameTheTreeParent(t *testing.T) {
	f := newCommsFixture(t)
	f.child.SetReportsTo("coord-gone") // nested under f.parent, launcher removed
	if _, ok := f.byID["coord-gone"]; ok {
		t.Fatal("fixture registers the launcher")
	}
	if got := f.child.reportTargetIDIn(f.byID); got != f.parent.ID {
		t.Fatalf("inbox target %q, want the tree parent %q", got, f.parent.ID)
	}

	f.appendTurn(t, fxHuman("u0", "run the board"), fxAssistantText("a0", "Starting 13 lanes."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, SessionID: f.child.ClaudeSessionID,
		Text: "Starting 13 lanes.", TranscriptPath: f.transcript, TSignal: time.Now().UnixMilli() - 20})
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "status", Edge: CommsEdgeStatus, Instance: f.child.ID,
		From: "running", State: "waiting", TSignal: time.Now().UnixMilli() - 10})
	// The worker sends to a sibling: its own report target observes.
	SpoolCommsSend(f.child.ID, f.codex.ID, "please rebase on main", "tmux", "")
	f.d.ingestCommsSpool("default", f.byID)

	seen := map[string]bool{}
	for _, r := range f.ledgerRecords(t) {
		switch {
		case r.Kind == comms.KindTurn && r.From == f.child.ID,
			r.Kind == comms.KindStatus && r.From == f.child.ID,
			r.Kind == comms.KindSend && r.From == f.child.ID:
			seen[r.Kind] = true
			for _, to := range r.To {
				if to == "coord-gone" {
					t.Errorf("%s record To=%v names the removed launcher; want the tree parent %q", r.Kind, r.To, f.parent.ID)
				}
			}
			if !containsID(r.To, f.parent.ID) {
				t.Errorf("%s record To=%v lacks the tree parent %q", r.Kind, r.To, f.parent.ID)
			}
		}
	}
	for _, k := range []string{comms.KindTurn, comms.KindStatus, comms.KindSend} {
		if !seen[k] {
			t.Errorf("no %s record from the worker in the ledger", k)
		}
	}
}

// PR #3166 review: a default (top-level) worker has reports_to and no tree
// parent. Promote in the TUI must still detach it from its launcher.
func TestIssue3165_TUIPromoteDetachesATopLevelWorker(t *testing.T) {
	root, coord := issue3165Fleet()
	worker := issue3165Worker("wrk-3165", "", coord.ID)
	tree := NewGroupTreeWithGroups([]*Instance{root, coord, worker}, nil)
	tree.PromoteSession(worker)
	if worker.ReportsTo != "" || worker.ReportTargetID() != "" {
		t.Fatalf("after promote reports_to=%q target=%q, want the launcher link cleared", worker.ReportsTo, worker.ReportTargetID())
	}
}

// The registry-aware stats bucket agrees with the inbox: a gone launcher
// falls back to a registered tree parent; a top-level worker keeps the
// launcher id, like a child of a removed parent.
func TestIssue3165_StatsParentForInFallsBack(t *testing.T) {
	root, coord := issue3165Fleet()
	nested := issue3165Worker("wrk-n", root.ID, coord.ID)
	top := issue3165Worker("wrk-t", "", coord.ID)
	byID := map[string]*Instance{root.ID: root, coord.ID: coord, nested.ID: nested, top.ID: top}
	if got := statsParentForIn(nested, byID); got != coord.ID {
		t.Fatalf("launcher present: %q, want %q", got, coord.ID)
	}
	delete(byID, coord.ID)
	if got := statsParentForIn(nested, byID); got != root.ID {
		t.Fatalf("nested, launcher gone: %q, want the root %q", got, root.ID)
	}
	if got := statsParentForIn(top, byID); got != coord.ID {
		t.Fatalf("top-level, launcher gone: %q, want the launcher id %q", got, coord.ID)
	}
	if got := statsParentForIn(&Instance{ID: "lone"}, byID); got != UnownedInboxID {
		t.Fatalf("no target: %q, want the unowned ledger", got)
	}
}
