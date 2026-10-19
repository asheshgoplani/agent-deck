package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Issue #2792: the idle build refresh restarted a parented child onto a new
// Claude build; the resumed Claude booted (running) and settled at its prompt
// (waiting) with no new turn, and the parent got an urgent "waiting" record as
// if a turn had finished. The observed flip over an unchanged transcript was
// read as a real turn with a stale signal (#2184) and forced to urgent.

// fxResumeMeta is the kind of non-turn record a resumed Claude appends to its
// transcript while it boots.
func fxResumeMeta() string { return `{"type":"permission-mode","permissionMode":"default"}` }

// bootSeen is the daemon pass that sees the restarted child running while it
// boots, which starts a new run for it.
func (f *turnTestFixture) bootSeen() {
	f.d.forgetJournaledTurnsOfRunning("default", map[string]string{f.child.ID: "running", f.parent.ID: "waiting"})
}

// The idle refresh boot reaches the parent as at most one info line, never an
// urgent record or a wake.
func TestIssue2792_IdleRefreshRestartSendsNoCompletion(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	before := f.inboxRecords(t)
	if len(before) != 1 {
		t.Fatalf("precondition: the real turn is one record, got %+v", before)
	}
	sendsBefore := *f.sends

	// `session restart --if-idle` notes the turn, the daemon adds the build.
	f.child.SetRestartReason(RestartReasonIdleBuildRefresh)
	noteRestartResume(f.child, time.Now())
	annotateRestartResumeBuild(f.child.ID, "/opt/claude/versions/2.1.303")
	f.appendTurn(t, fxResumeMeta())
	f.bootSeen()
	result, ok := f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	if !ok {
		t.Fatalf("the boot flip must be handled, not retried")
	}

	recs := f.inboxRecords(t)
	var added []TransitionNotificationEvent
	for _, r := range recs {
		if r.TurnUUID == "a1" && r.Tier == TurnTierInfo && r.Text == "Index added; tests pass." {
			continue // the real turn from before the restart
		}
		added = append(added, r)
	}
	if len(added) > 1 {
		t.Fatalf("at most one record for the refresh, got %+v", added)
	}
	for _, r := range added {
		if r.IsUrgent() {
			t.Fatalf("the refresh boot must not be urgent: %+v (result %+v)", r, result)
		}
		if !strings.Contains(r.Text, "Refreshed onto claude 2.1.303") {
			t.Fatalf("the info line names the build, got %q", r.Text)
		}
	}
	if *f.sends != sendsBefore {
		t.Fatalf("the refresh boot must not wake the parent, sends %d -> %d", sendsBefore, *f.sends)
	}
	if _, left := readRestartResumeNote(f.child.ID); left {
		t.Fatalf("the note is spent by the boot flip")
	}
}

// A plain `session restart` (no refresh reason) of an idle child sends nothing.
func TestIssue2792_ResumeOnlyRestartSendsNothing(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	n := len(f.inboxRecords(t))
	sends := *f.sends

	noteRestartResume(f.child, time.Now())
	f.appendTurn(t, fxResumeMeta())
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)

	if got := len(f.inboxRecords(t)); got != n {
		t.Fatalf("a resume-only restart must add no record: %d -> %d: %+v", n, got, f.inboxRecords(t))
	}
	if *f.sends != sends {
		t.Fatalf("no wake, sends %d -> %d", sends, *f.sends)
	}
}

// A real turn after the restart, or an error, is delivered as before.
func TestIssue2792_NewTurnOrErrorAfterRestartStillNotifies(t *testing.T) {
	t.Run("new turn", func(t *testing.T) {
		f := newTurnTestFixture(t)
		f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		f.child.SetRestartReason(RestartReasonIdleBuildRefresh)
		noteRestartResume(f.child, time.Now())
		f.bootSeen()
		f.appendTurn(t, fxResumeMeta(), fxHuman("u2", "status?"), fxAssistantText("a2", "Blocked on the schema. Should I drop the old column?"))
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		var found bool
		for _, r := range f.inboxRecords(t) {
			found = found || (r.TurnUUID == "a2" && r.IsUrgent())
		}
		if !found {
			t.Fatalf("the question after the restart must reach the parent urgently: %+v", f.inboxRecords(t))
		}
	})
	t.Run("error", func(t *testing.T) {
		f := newTurnTestFixture(t)
		f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		sends := *f.sends
		noteRestartResume(f.child, time.Now())
		f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), true)
		var found bool
		for _, r := range f.inboxRecords(t) {
			found = found || (r.ToStatus == "error" && r.IsUrgent())
		}
		if !found || *f.sends == sends {
			t.Fatalf("an error after a restart is urgent news: %+v", f.inboxRecords(t))
		}
	})
	t.Run("stale note", func(t *testing.T) {
		f := newTurnTestFixture(t)
		f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		noteRestartResume(f.child, time.Now().Add(-restartResumeTTL-time.Minute))
		f.appendTurn(t, fxResumeMeta())
		f.bootSeen()
		f.bootSeen()
		_, _ = f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		if _, left := readRestartResumeNote(f.child.ID); left {
			t.Fatalf("an expired note is spent, not kept")
		}
	})
}

// The live case: the child's transcript ended in a compaction summary with no
// reply, so it has no turn identity and the daemon keys it on the file size,
// which the resume's metadata records grow. The refresh boot still must not
// reach the parent as a completion.
func TestIssue2792_CompactedTranscriptRefreshSendsNoCompletion(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."),
		`{"type":"system","subtype":"compact_boundary","isSidechain":false}`,
		fxUser("u2", "This session is being continued from a previous conversation.", map[string]any{"isCompactSummary": true}))
	settle := func() { // past the flush-race window, as on a session idle for days
		past := time.Now().Add(-time.Hour)
		_ = os.Chtimes(f.transcript, past, past)
	}
	settle()
	sends := *f.sends
	n := len(f.inboxRecords(t))

	f.child.SetRestartReason(RestartReasonIdleBuildRefresh)
	noteRestartResume(f.child, time.Now())
	f.appendTurn(t, `{"type":"last-prompt"}`, `{"type":"custom-title","customTitle":"x"}`, fxResumeMeta(), `{"type":"cost-state"}`)
	settle()
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)

	for _, r := range f.inboxRecords(t)[n:] {
		if r.IsUrgent() {
			t.Fatalf("the refresh boot of a compacted child must not be urgent: %+v", r)
		}
	}
	if got := len(f.inboxRecords(t)) - n; got > 1 {
		t.Fatalf("at most one info line, got %d", got)
	}
	if *f.sends != sends {
		t.Fatalf("no wake, sends %d -> %d", sends, *f.sends)
	}
}

// Lander review of #2793: a restart that cuts a turn short (a plain `session
// restart` while the child works) must still tell the parent the child
// stopped, as main does: such a child gets no note.
func TestIssue2792_MidTurnRestartStillNotifies(t *testing.T) {
	cases := map[string]func(f *turnTestFixture, t *testing.T){
		"tool call after the text": func(f *turnTestFixture, t *testing.T) {
			f.appendTurn(t, fxHuman("u1", "run it"), fxAssistantText("a1", "Running the migration now."), fxAssistantToolUse("a2"))
		},
		"running": func(f *turnTestFixture, t *testing.T) {
			f.appendTurn(t, fxHuman("u1", "run it"), fxAssistantText("a1", "Running the migration now."))
			f.child.Status = StatusRunning
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			setup(f, t)
			noteRestartResume(f.child, time.Now())
			if _, ok := readRestartResumeNote(f.child.ID); ok {
				t.Fatalf("a child in the middle of a turn gets no note")
			}
			f.child.Status = StatusWaiting
			n := len(f.inboxRecords(t))
			f.appendTurn(t, fxResumeMeta())
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
			if got := len(f.inboxRecords(t)); got != n+1 {
				t.Fatalf("the parent must hear the cut-short child stopped: records %d -> %d", n, got)
			}
		})
	}
}

// An annotation after the daemon spent the note does not bring it back.
func TestIssue2792_AnnotateDoesNotResurrectSpentNote(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
	noteRestartResume(f.child, time.Now())
	clearRestartResumeNote(f.child.ID)
	annotateRestartResumeBuild(f.child.ID, "/opt/claude/versions/2.1.303")
	if _, ok := readRestartResumeNote(f.child.ID); ok {
		t.Fatalf("a spent note must stay spent")
	}
	// The note spent between the annotation's read and its write.
	writeRestartResumeNoteIf(f.child.ID, restartResumeNote{At: time.Now(), Signal: "x", Build: "b"}, true)
	if _, ok := readRestartResumeNote(f.child.ID); ok {
		t.Fatalf("the annotation's write must not bring back a spent note")
	}
}
