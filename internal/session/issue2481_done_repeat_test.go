package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #2481: a finished worker kept re-printing its identical completion
// sentinel from leftover scheduled checks (/loop wakeups, background task
// notifications), and every repeat reached the parent as a new urgent
// finished record that woke it. Measured on the live turn journal: 20 of 34
// done records were identical repeats of the same child's done within an
// hour. Contract: one delivered done per child per identical status+summary
// within doneRepeatWindow; later repeats are counted on the completion ledger
// entry and in inbox stats, never committed and never woken. A changed status
// or summary is a new completion and is delivered.

// fxScheduledWake mirrors the user record Claude Code writes when a /loop
// (ScheduleWakeup) fires: isMeta, turnOrigin "scheduled", promptSource system.
func fxScheduledWake(uuid, text string) string {
	return fxUser(uuid, text, map[string]any{"isMeta": true, "turnOrigin": "scheduled", "promptSource": "system"})
}

const doneLine = "\n===AGENTDECK_DONE=== status=ok summary=board at zero, 13 closed"

func (f *turnTestFixture) drain(t *testing.T) []TransitionNotificationEvent {
	t.Helper()
	drained, err := DrainInboxForParent(f.parent.ID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	return drained
}

func TestIssue2481_IdenticalDoneRepeatsAreCountedNotDelivered(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}

	f.appendTurn(t, fxTaskNotification("u0"), fxAssistantText("a0", "All lanes merged."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.drain(t); len(got) != 1 || got[0].Kind != transitionKindFinished {
		t.Fatalf("first completion must be delivered once: %+v", got)
	}
	if *f.sends != 1 {
		t.Fatalf("first completion wakes once, sends=%d", *f.sends)
	}

	// Three leftover /loop wakes and one background notification, each a new
	// turn re-printing the same sentinel with different surrounding words.
	repeats := []struct{ user, uuid, text string }{
		{fxScheduledWake("u1", "/loop check the board"), "a1", "Nothing new; still done."},
		{fxScheduledWake("u2", "/loop check the board"), "a2", "Re-checked, no change."},
		{fxTaskNotification("u3"), "a3", "Monitor exited."},
		{fxScheduledWake("u4", "/loop check the board"), "a4", "Still at zero."},
	}
	for _, r := range repeats {
		f.appendTurn(t, r.user, fxAssistantText(r.uuid, r.text+doneLine))
		// Every observation path, several times: one repeat turn must be
		// counted once, however often it is observed.
		for i := 0; i < 3; i++ {
			f.d.recordTerminalTurns("default", f.byID, statuses, nil)
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		}
	}
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("identical done repeats must not reach the inbox, got %d: %+v", len(got), got)
	}
	if *f.sends != 1 {
		t.Fatalf("identical done repeats must not wake the parent, sends=%d", *f.sends)
	}
	entry, ok := ReadLedgerEntry(f.child.ID)
	if !ok || entry.Repeats != len(repeats) || entry.LastRepeatAt.IsZero() {
		t.Fatalf("ledger must count %d repeats: ok=%v %+v", len(repeats), ok, entry)
	}
	st, _ := ReadInboxStats(f.parent.ID)
	if st.DoneRepeats != int64(len(repeats)) || st.RecordsUrgent != 1 {
		t.Fatalf("inbox stats must count repeats and one urgent record: %+v", st)
	}

	// A changed summary is a new completion: delivered and the count resets.
	f.appendTurn(t, fxScheduledWake("u5", "/loop check the board"),
		fxAssistantText("a5", "Reopened one.\n===AGENTDECK_DONE=== status=ok summary=board at zero, 14 closed"))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.drain(t); len(got) != 1 || got[0].DoneSummary != "board at zero, 14 closed" {
		t.Fatalf("changed summary must be delivered: %+v", got)
	}
	if entry, _ := ReadLedgerEntry(f.child.ID); entry.Repeats != 0 || entry.Summary != "board at zero, 14 closed" {
		t.Fatalf("a delivered completion starts a fresh count: %+v", entry)
	}

	// A changed status with the same summary is also a new completion.
	f.appendTurn(t, fxScheduledWake("u6", "/loop check the board"),
		fxAssistantText("a6", "CI went red.\n===AGENTDECK_DONE=== status=fail summary=board at zero, 14 closed"))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.drain(t); len(got) != 1 || got[0].DoneStatus != "fail" {
		t.Fatalf("changed status must be delivered: %+v", got)
	}
	if *f.sends != 3 {
		t.Fatalf("each new completion wakes once, sends=%d", *f.sends)
	}
}

func TestIssue2481_IdenticalDoneAfterWindowIsDeliveredAgain(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxTaskNotification("u0"), fxAssistantText("a0", "Done."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.drain(t)

	// Age the delivered completion past the window.
	entry, _ := ReadLedgerEntry(f.child.ID)
	entry.FinishedAt = time.Now().Add(-doneRepeatWindow - time.Minute)
	if err := WriteLedgerEntry(entry); err != nil {
		t.Fatalf("WriteLedgerEntry: %v", err)
	}
	f.appendTurn(t, fxScheduledWake("u1", "/loop"), fxAssistantText("a1", "Still done."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.drain(t); len(got) != 1 {
		t.Fatalf("an identical done past the window is delivered once more: %+v", got)
	}
}

// A turn a person or a parent send started is news even when the worker
// closes it with the same sentinel (the #2469 rule for replies), so it is
// still delivered inside the window.
func TestIssue2481_IdenticalDoneOnHumanTurnIsDelivered(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxTaskNotification("u0"), fxAssistantText("a0", "Done."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.drain(t)
	f.appendTurn(t, fxHuman("u1", "did you also close #12?"), fxAssistantText("a1", "Yes, #12 is closed."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.drain(t); len(got) != 1 || got[0].TurnUUID != "a1" {
		t.Fatalf("a human-started turn with the same sentinel is delivered: %+v", got)
	}
}

// The SAME turn observed again after its record was drained (a forced-urgent
// snapshot edge, a hook re-fire hours later) is never a second completion,
// whatever its age.
func TestIssue2481_SameDoneTurnIsNeverRedelivered(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxHuman("u0", "ship it"), fxAssistantText("a0", "Shipped."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.drain(t)
	entry, _ := ReadLedgerEntry(f.child.ID)
	entry.FinishedAt = time.Now().Add(-12 * time.Hour)
	_ = WriteLedgerEntry(entry)

	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("the same done turn must not be delivered twice: %+v", got)
	}
	if *f.sends != 1 {
		t.Fatalf("sends=%d", *f.sends)
	}
}

// Hook-file path (tools without a readable transcript, or a daemon restart
// that lost its in-memory lastDone): the durable ledger still recognises a
// repeat inside the window.
func TestIssue2481_HookPathRepeatAfterRestartIsCounted(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.ClaudeSessionID = ""
	hs := &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: time.Now(), DoneStatus: "ok", DoneSummary: "board at zero, 13 closed"}
	f.d.emitDoneSignals("default", f.byID, map[string]*HookStatus{f.child.ID: hs})
	if got := f.drain(t); len(got) != 1 {
		t.Fatalf("first hook completion delivered: %+v", got)
	}
	f.d.lastDone = map[string]map[string]DoneSignal{} // daemon restart
	hs2 := *hs
	hs2.UpdatedAt = time.Now()
	for i := 0; i < 3; i++ {
		f.d.emitDoneSignals("default", f.byID, map[string]*HookStatus{f.child.ID: &hs2})
	}
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("hook repeat must not be delivered: %+v", got)
	}
	if entry, _ := ReadLedgerEntry(f.child.ID); entry.Repeats != 1 {
		t.Fatalf("hook repeat counted once: %+v", entry)
	}
}

// Mixed versions: a ledger file written by an older build (no repeat fields)
// still parses, and a new entry still parses with the old field set.
func TestIssue2481_LedgerCompatAcrossVersions(t *testing.T) {
	reviewTestHome(t, "default")
	old := `{"child_id":"c-old","profile":"default","status":"ok","summary":"s","finished_at":"2026-10-03T11:00:00Z"}`
	path, err := completionLedgerPath("c-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	e, ok := ReadLedgerEntry("c-old")
	if !ok || e.Status != "ok" || e.Repeats != 0 || e.TurnUUID != "" {
		t.Fatalf("old ledger entry must parse: %v %+v", ok, e)
	}
	if e.DisplaySummary() != "s" {
		t.Fatalf("display of an entry with no repeats is the summary: %q", e.DisplaySummary())
	}

	e.Repeats, e.TurnUUID, e.LastRepeatAt = 3, "a0", time.Now()
	data, _ := json.Marshal(e)
	var legacy struct {
		ChildID    string    `json:"child_id"`
		Status     string    `json:"status"`
		Summary    string    `json:"summary"`
		FinishedAt time.Time `json:"finished_at"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil || legacy.ChildID != "c-old" || legacy.Summary != "s" {
		t.Fatalf("an old reader must parse a new entry: %v %+v", err, legacy)
	}
	if got := e.DisplaySummary(); got != "s (repeated 3x, not delivered)" {
		t.Fatalf("display must show the repeat count: %q", got)
	}
}

// Review round 1 (MAJOR 1): a worker reused for a second job by a tagged send
// whose job is long enough that the send record falls outside the transcript
// tail window classifies as trigger "unknown". Unknown is not background
// (the ClassifyTurnTier rule), so the same summary is a new completion.
func TestIssue2481_LongSendJobSameSummaryIsDelivered(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxTaskNotification("u0"), fxAssistantText("a0", "All lanes merged."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.drain(t)

	lines := []string{fxUser("u1", "[agent-deck from:parent-2469] second job: redo the board", nil)}
	for i := 0; i < 120; i++ {
		lines = append(lines, fxAssistantToolUse(fmt.Sprintf("tu%d", i)), fxToolResult(fmt.Sprintf("tr%d", i)))
	}
	lines = append(lines, fxAssistantText("a1", "Second job finished."+doneLine))
	f.appendTurn(t, lines...)
	if facts, _ := instanceTurnFacts(f.child); facts.Trigger != TurnTriggerUnknown {
		t.Fatalf("fixture must exercise trigger unknown, got %q", facts.Trigger)
	}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.drain(t); len(got) != 1 || got[0].TurnUUID != "a1" {
		t.Fatalf("a long send-started job with the same summary must be delivered: %+v", got)
	}
}

// Prompt commands and skills write a command record followed by a meta
// expansion. Only the command record distinguishes typed from scheduled work.
func TestIssue2481_TypedSlashCommandJobSameSummaryIsDelivered(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin map[string]any
		typed  bool
	}{
		{"typed skill", nil, true},
		{"scheduled loop", map[string]any{"turnOrigin": "scheduled"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
			f.appendTurn(t, fxTaskNotification("u0"), fxAssistantText("a0", "All lanes merged."+doneLine))
			f.d.recordTerminalTurns("default", f.byID, statuses, nil)
			f.drain(t)

			command, args, body := "ship", "board 2", "Base directory for this skill: /x/ship\n\nShip the board."
			if !tc.typed {
				command, args, body = "loop", "tick", "# /loop schedule a recurring prompt"
			}
			f.appendTurn(t,
				fxUser("u1", "<command-message>"+command+"</command-message>\n<command-name>/"+command+"</command-name>\n<command-args>"+args+"</command-args>", tc.origin),
				fxUser("u1m", body, map[string]any{"isMeta": true}),
				fxAssistantText("a1", "Finished."+doneLine))
			facts, _ := instanceTurnFacts(f.child)
			f.d.recordTerminalTurns("default", f.byID, statuses, nil)
			got := f.drain(t)
			e, _ := ReadLedgerEntry(f.child.ID)
			t.Logf("trigger=%q typed=%v delivered=%d repeats=%d", facts.Trigger, facts.TypedCommand, len(got), e.Repeats)
			if tc.typed {
				if len(got) != 1 || got[0].TurnUUID != "a1" || e.Repeats != 0 {
					t.Fatalf("typed skill completion must be delivered: records=%+v repeats=%d", got, e.Repeats)
				}
			} else if len(got) != 0 || e.Repeats != 1 {
				t.Fatalf("scheduled loop repeat must be counted: records=%+v repeats=%d", got, e.Repeats)
			}
			if facts.Trigger != TurnTriggerSystem || facts.TypedCommand != tc.typed {
				t.Fatalf("unexpected command classification: %+v", facts)
			}
		})
	}
}

// Review round 1 (MINOR 3): after a restart the hook-file path can see a new
// human-started identical done before emitTurn. For a child with a readable
// transcript emitTurn owns the decision: the hook path must not count it, and
// emitTurn delivers it.
func TestIssue2481_HookFirstHumanTurnAfterRestartIsDeliveredNotCounted(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxTaskNotification("u0"), fxAssistantText("a0", "All lanes merged."+doneLine))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.drain(t)

	f.appendTurn(t, fxHuman("u1", "do board 2"), fxAssistantText("a1", "Board 2 done."+doneLine))
	f.d.lastDone = map[string]map[string]DoneSignal{} // daemon restart
	hs := &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: time.Now(), DoneStatus: "ok", DoneSummary: "board at zero, 13 closed"}
	f.d.emitDoneSignals("default", f.byID, map[string]*HookStatus{f.child.ID: hs})
	if e, _ := ReadLedgerEntry(f.child.ID); e.Repeats != 0 {
		t.Fatalf("the hook path must not count a turn emitTurn owns: %+v", e)
	}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.drain(t); len(got) != 1 || got[0].TurnUUID != "a1" {
		t.Fatalf("emitTurn must deliver the human-started completion once: %+v", got)
	}
	if st, _ := ReadInboxStats(f.parent.ID); st.DoneRepeats != 0 {
		t.Fatalf("a delivered completion must not be counted as a repeat: %+v", st)
	}
}

func TestIssue2481_TypedCommandTurnBoundary(t *testing.T) {
	command := fxUser("command", "<command-name>/ship</command-name>", nil)
	meta := fxUser("meta", "Expanded command body", map[string]any{"isMeta": true})
	for _, tc := range []struct {
		name    string
		prompts []string
		typed   bool
	}{
		{"single command", []string{command}, true},
		{"multiple expansions", []string{command, meta, meta}, true},
		{"missing start", []string{meta}, false},
		{"previous turn", []string{command, fxAssistantText("old", "Done"), meta}, false},
		{"ordinary prompt", []string{command, fxHuman("human", "Check status"), meta}, false},
		{"scheduled wake", []string{command, fxScheduledWake("wake", "/loop check")}, false},
		{"sidechain command", []string{fxUser("side", "<command-name>/ship</command-name>", map[string]any{"isSidechain": true}), meta}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := classifyTranscriptTail(append(tc.prompts, fxAssistantText("reply", "Finished."+doneLine)))
			if facts.TypedCommand != tc.typed {
				t.Fatalf("TypedCommand=%v, want %v", facts.TypedCommand, tc.typed)
			}
		})
	}
}
