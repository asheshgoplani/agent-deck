package session

import (
	"testing"
	"time"
)

// Issue #2481 item 2: the same turn uuid was journaled as info by the hook or
// recorded-turn path and then re-journaled as urgent 0 to 4 s later by the
// snapshot edge (the observed flip forced the noise verdict to urgent). 49% of
// live journal entries were such copies, the notifier dropped the urgent copy,
// and records_urgent counted it anyway. These tests replay that sequence.

// The live pattern: a plain reply is recorded as info by the recorded-turn
// path, then the snapshot edge observes the same run's running->waiting flip.
func TestIssue2481_SnapshotEdgeAfterHookRecordJournalsTurnOnce(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantText("a1", "Lane C merged; verifier running."))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}

	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)

	journal, err := ReadTurnJournal(f.child.ID, 0)
	if err != nil {
		t.Fatalf("ReadTurnJournal: %v", err)
	}
	if len(journal) != 1 {
		t.Fatalf("one turn uuid must be ONE journal entry, got %d: %+v", len(journal), journal)
	}
	if journal[0].Tier != TurnTierInfo || journal[0].UUID != "a1" {
		t.Fatalf("the plain reply stays info: %+v", journal[0])
	}
	recs := f.inboxRecords(t)
	if len(recs) != 1 || recs[0].Tier != TurnTierInfo {
		t.Fatalf("want one info record, got %+v", recs)
	}
	st, _ := ReadInboxStats(f.parent.ID)
	if st.RecordsUrgent != 0 || st.RecordsInfo != 1 {
		t.Fatalf("stats must count the one committed record, not observations: urgent=%d info=%d", st.RecordsUrgent, st.RecordsInfo)
	}
	if *f.sends != 0 {
		t.Fatalf("an info turn must not wake, sends=%d", *f.sends)
	}
}

// records_urgent / records_info count commits: a turn the notifier drops (here
// the snapshot edge re-observing an urgent turn the hook path already
// committed) must not be counted a second time.
func TestIssue2481_StatsCountCommittedRecordsOnly(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u1", "status?"), fxAssistantText("a1", "Blocked on the schema. Should I drop the old column?"))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}

	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)

	recs := f.inboxRecords(t)
	if len(recs) != 1 || recs[0].Tier != TurnTierUrgent {
		t.Fatalf("want one urgent record, got %+v", recs)
	}
	st, _ := ReadInboxStats(f.parent.ID)
	if st.RecordsUrgent != 1 || st.RecordsInfo != 0 {
		t.Fatalf("records_urgent must equal committed urgent records (1): urgent=%d info=%d", st.RecordsUrgent, st.RecordsInfo)
	}
	if journal, _ := ReadTurnJournal(f.child.ID, 0); len(journal) != 1 {
		t.Fatalf("one journal entry per turn uuid, got %+v", journal)
	}
	if *f.sends != 1 {
		t.Fatalf("the urgent turn wakes exactly once, sends=%d", *f.sends)
	}
}

// A genuine escalation of one turn (recorded as info, then the same turn
// settles into error) upgrades the single journal entry and the single
// pending record instead of adding a second, and still wakes the parent.
func TestIssue2481_EscalationUpgradesSingleEntryAndWakes(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u1", "run it"), fxAssistantText("a1", "Running the migration now."))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}

	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if *f.sends != 0 {
		t.Fatalf("precondition: info does not wake, sends=%d", *f.sends)
	}
	f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), false)

	journal, _ := ReadTurnJournal(f.child.ID, 0)
	if len(journal) != 1 {
		t.Fatalf("an escalation must upgrade the entry, not add one: %+v", journal)
	}
	if journal[0].Tier != TurnTierUrgent || journal[0].Status != "error" || journal[0].UUID != "a1" {
		t.Fatalf("upgraded entry: %+v", journal[0])
	}
	recs := f.inboxRecords(t)
	if len(recs) != 1 || recs[0].Tier != TurnTierUrgent || recs[0].TurnUUID != "a1" {
		t.Fatalf("the pending info record must be upgraded to one urgent record, got %+v", recs)
	}
	if *f.sends != 1 {
		t.Fatalf("the escalation must wake the parent once, sends=%d", *f.sends)
	}
}
