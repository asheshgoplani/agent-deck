package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Issue #3012 step 2: the fresh-start judge asks "a turn since the start?" as
// the hook file's turn_seq against the token's. A higher count is a turn, a
// lower one is the hook file recreated (something happened), the same count is
// no turn. The hook file's turn_seq does not depend on the [health] event
// history and survives /clear, /compact and an inherited claude -p, so those
// no longer need their own rules, and a token with the history off lasts
// until its first turn like one with it on. A token an older binary wrote (no
// turn_seq) is judged as before: TestIssue2954_FreshStartBoot covers that.

// freshSeqAt is the turn_seq the token records in these cases.
const freshSeqAt = 3

// freshStartSeq writes the token as this binary does, raw so the test reads
// the same on a binary where the field is a plain count: turn_seq always
// present, even at 0.
func (f *turnTestFixture) freshStartSeq(t *testing.T, at time.Time, seq uint64) {
	t.Helper()
	writeTokenFile(freshStartDir, f.child.ID, ".json", map[string]any{
		"at": at, "session_id": f.child.ClaudeSessionID, "turn_seq": seq,
	})
	if _, ok := readFreshStartToken(f.child.ID); !ok {
		t.Fatalf("precondition: token unreadable")
	}
}

// turnSeq sets the child's hook file turn_seq on disk, the count the hook
// handler keeps. The event the judge reads is the stub's (st.hook).
func (f *turnTestFixture) turnSeq(t *testing.T, seq uint64) {
	t.Helper()
	body := `{"status":"waiting","event":"SessionStart","ts":` + strconv.FormatInt(time.Now().Unix(), 10)
	if seq != 0 {
		body += `,"turn_seq":` + strconv.FormatUint(seq, 10)
	}
	writeHookFileForTest(t, f.child.ID, body+"}")
}

// turnSeqFile writes <id>.turnseq, the copy of the count that outlives the
// hook file.
func (f *turnTestFixture) turnSeqFile(t *testing.T, seq uint64) {
	t.Helper()
	path := filepath.Join(filepath.Dir(hookStatusFilePath(f.child.ID)), f.child.ID+".turnseq")
	if err := os.WriteFile(path, []byte(strconv.FormatUint(seq, 10)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// noHookHistory is [health] enabled = false: no <id>.events.jsonl.
func noHookHistory(t *testing.T, id string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(hookStatusFilePath(id)), id+".events.jsonl")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// Every case of TestIssue2954_FreshStartBoot, with a token that records
// turn_seq and a hook file whose count matches what the case's events would
// have done, plus the cases only turn_seq can tell. Unless a case says
// otherwise: pane at the empty prompt, SessionStart in the hook file, no hook
// event history, no transcript yet, count unchanged.
func TestIssue3012_FreshStartJudgedByTurnSeq(t *testing.T) {
	type seqCase struct {
		run  func(t *testing.T, f *turnTestFixture, st *pidStub)
		want []string
		left bool
	}
	cases := map[string]seqCase{
		// The boot itself, as before.
		"seen on every path in one pass": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
		}, []string{freshStartLine}, true},
		"read by the parent, seen again by a restarted daemon": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
			if got := f.drain(t); len(got) != 1 {
				t.Fatalf("want the line drained, got %+v", got)
			}
			f.restartDaemon()
			f.observeAllPaths(f.idle())
		}, nil, true},
		"at the prompt before SessionStart lands, then it lands": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			f.observeAllPaths(f.idle())
			if got := f.pending(t); len(got) != 0 {
				t.Fatalf("not the boot until a hook event shows this process's hooks fire, got %q", got)
			}
			st.hook = sessionStartNow()
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"the previous process's Stop on file, then SessionStart": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			at := time.Now().Add(-2 * time.Second)
			st.hook = &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: at, ReceivedAt: at}
			f.observeAllPaths(f.idle())
			st.hook = sessionStartNow()
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"hooks never fire: past the boot timeout as before": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			f.freshStartSeq(t, time.Now().Add(-3*time.Minute), freshSeqAt)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// The transcript is not how this judge sees turns: with no hook event
		// yet, a resolving transcript path is no proof the hooks fire.
		"an empty transcript, no hook event: not yet": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			if err := os.WriteFile(f.transcript, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			f.observeAllPaths(f.idle())
		}, nil, true},
		"a flip stamped before the token": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now().Add(-time.Minute), true)
		}, []string{"urgent: "}, true},
		"pane not drawn yet, then the prompt": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.substate = string(SubstateNone)
			f.observeAllPaths(f.idle())
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"pane not drawn within the boot timeout: news, token spent": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.substate = string(SubstateNone)
			f.freshStartSeq(t, time.Now().Add(-3*time.Minute), freshSeqAt)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"acknowledged past the boot timeout, no turn": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStartSeq(t, time.Now().Add(-3*time.Minute), freshSeqAt)
			hookHistory(t, f.child.ID, "SessionStart")
			f.d.recordTerminalTurns("default", f.byID, f.status("waiting"), nil)
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		// [health] off: the count still says no turn, so the token is not
		// ended at the boot timeout (the history-off tier is gone).
		"history off: acknowledged past the boot timeout, no turn": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStartSeq(t, time.Now().Add(-3*time.Minute), freshSeqAt)
			f.d.recordTerminalTurns("default", f.byID, f.status("waiting"), nil)
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"history off: past the boot timeout": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStartSeq(t, time.Now().Add(-3*time.Minute), freshSeqAt)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"acknowledged past the boot timeout after the parent read the line": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStartSeq(t, time.Now().Add(-3*time.Minute), freshSeqAt)
			f.d.recordTerminalTurns("default", f.byID, f.status("waiting"), nil)
			if got := f.drain(t); len(got) != 1 {
				t.Fatalf("want the line drained, got %+v", got)
			}
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, nil, true},
		// SessionStart/SessionEnd order.
		"a SessionEnd since the start is not the proof": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: time.Now()}
			f.observeAllPaths(f.idle())
		}, nil, true},
		"a late SessionEnd over the new SessionStart": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			hookHistory(t, f.child.ID, "SessionStart", "SessionEnd")
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: time.Now()}
			f.observeAllPaths(f.idle())
		}, []string{freshStartLine}, true},
		"history off: a late SessionEnd over the new SessionStart, past the boot timeout": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStartSeq(t, time.Now().Add(-3*time.Minute), freshSeqAt)
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: time.Now()}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// /clear and conversation identity.
		"saved on another conversation than the start's": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.child.ClaudeSessionID = otherConversation
			f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now(), true)
		}, []string{"urgent: "}, false},
		"a /clear with no turn before it: a SessionStart of another conversation": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook.SessionID = otherConversation
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// The hook file keeps only the last event: the turn's Stop is gone,
		// and with the history off nothing else shows it. The count does.
		"history off: a turn, then a /clear (#3010 review)": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeq(t, freshSeqAt+2)
			st.hook.SessionID = otherConversation
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"history off: a turn, then a /compact (same conversation)": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeq(t, freshSeqAt+2)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"history off: an inherited claude -p ran a turn and ended": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeq(t, freshSeqAt+2)
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", SessionID: otherConversation, UpdatedAt: time.Now()}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"a turn with the history on": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			hookHistory(t, f.child.ID, "SessionStart", "UserPromptSubmit", "Stop", "SessionStart")
			f.turnSeq(t, freshSeqAt+2)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// The TUI removes the hook file on every attach-return; the count
		// lives on in <id>.turnseq.
		"a turn, then attach-return removed the hook file": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeqFile(t, freshSeqAt+2)
			f.child.ClearHookStatus()
			if _, err := os.Stat(hookStatusFilePath(f.child.ID)); !os.IsNotExist(err) {
				t.Fatalf("precondition: ClearHookStatus removes the hook file, stat %v", err)
			}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// The usual new child: no hook file yet, token at 0. Without the
		// sidecar the removal reads 0 again, the turn is lost, and the
		// pane back at its empty prompt reads as the boot.
		"a token at 0, a turn, then attach-return removed the hook file": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStartSeq(t, time.Now().Add(-time.Second), 0)
			f.turnSeqFile(t, 2)
			f.child.ClearHookStatus()
			hookHistory(t, f.child.ID, "SessionStart")
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"no turn, attach-return removed the hook file": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeqFile(t, freshSeqAt)
			f.child.ClearHookStatus()
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		// A count lower than the token's: the instance's hook artifacts were
		// removed and written again. Something happened.
		"the count went back (hook file recreated)": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeq(t, 0)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"the count went back to a lower non-zero one": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeq(t, 1)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// A token at 0 is a turn_seq token, not a legacy one.
		"a token at 0, then one turn": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStartSeq(t, time.Now().Add(-time.Second), 0)
			f.turnSeq(t, 1)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"two fresh starts in a row: a line each": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
			f.freshStartSeq(t, time.Now(), freshSeqAt)
			st.hook = sessionStartNow()
			f.d.recordTerminalTurns("default", f.byID, f.status("running"), nil)
			f.bootSeen()
			f.observeAllPaths(f.idle())
		}, []string{freshStartLine, freshStartLine}, true},
		"no parent: nothing sent, nothing unowned": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.child.ParentSessionID = ""
			f.observeAllPaths(f.idle())
			if events, _ := ReadInboxEvents(UnownedInboxID); len(events) != 0 {
				t.Fatalf("no unowned record, got %+v", events)
			}
		}, nil, true},
		"the kickoff's turn after the line": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
			f.appendTurn(t, fxHuman("u1", "reply pong"), fxAssistantText("a1", "pong"))
			f.turnSeq(t, freshSeqAt+2)
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		}, []string{freshStartLine, "info: pong"}, false},
		"a prompt submitted": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeq(t, freshSeqAt+1)
			st.hook = &HookStatus{Status: "running", Event: "UserPromptSubmit", UpdatedAt: time.Now().Add(time.Second)}
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		}, []string{"urgent: "}, false},
		"an error": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), true)
		}, []string{"urgent: "}, false},
	}
	for _, sub := range []Substate{SubstateInteractiveMenu, SubstateAuth401, SubstateModelUnavailable} {
		cases["pane shows "+string(sub)+", then the prompt"] = seqCase{func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.substate = string(sub)
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now(), true)
		}, []string{"urgent: ", freshStartLine}, true}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.turnSeq(t, freshSeqAt)
			f.freshStartSeq(t, time.Now().Add(-time.Second), freshSeqAt)
			noHookHistory(t, f.child.ID)
			st.hook = sessionStartNow()
			c.run(t, f, st)
			if got := f.pending(t); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("records:\n got %q\nwant %q", got, c.want)
			}
			if !strings.Contains(strings.Join(c.want, ""), "urgent") && (*f.sends != 0 || InboxHasUrgentPending(f.parent.ID)) {
				t.Fatalf("the boot must not wake the parent or block its Stop, sends=%d", *f.sends)
			}
			if f.freshTokenLeft() != c.left {
				t.Fatalf("token left = %v, want %v", !c.left, c.left)
			}
		})
	}
}

// The sweep spends a turn_seq token on a turn (or a lower count) and at
// restartTokenOrphanAge, never at the boot timeout: with the history off or
// on, an unchanged count is no turn.
func TestIssue3012_SweepByTurnSeq(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "30s")
	for _, c := range []struct {
		name string
		age  time.Duration
		seq  uint64
		left bool
	}{
		{"within the boot timeout", 10 * time.Second, freshSeqAt, true},
		{"past the boot timeout, history off, no turn", time.Minute, freshSeqAt, true},
		{"a turn", 10 * time.Second, freshSeqAt + 1, false},
		{"the count went back", 10 * time.Second, 0, false},
		{"a day, no turn", 25 * time.Hour, freshSeqAt, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			stubRestartPIDs(t)
			f.turnSeq(t, c.seq)
			f.freshStartSeq(t, time.Now().Add(-c.age), freshSeqAt)
			noHookHistory(t, f.child.ID)
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			if got := f.pending(t); len(got) != 0 {
				t.Fatalf("the sweep sends nothing, got %q", got)
			}
			if f.freshTokenLeft() != c.left {
				t.Fatalf("token left = %v, want %v", !c.left, c.left)
			}
		})
	}
}

// Back-compat: a token an older binary wrote has no turn_seq and is judged by
// the rules before it, whatever the hook file's count says.
func TestIssue3012_LegacyTokenKeepsTheOldRules(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, f *turnTestFixture, st *pidStub)
		want  []string
		left  bool
	}{
		// The count is not read: only the old rules' evidence counts.
		{"a higher count, no other sign of a turn", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.turnSeq(t, 9)
		}, []string{freshStartLine}, true},
		{"the history-off tier still ends it at the boot timeout", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			noHookHistory(t, f.child.ID)
			f.freshStart(time.Now().Add(-3 * time.Minute))
		}, []string{"urgent: "}, false},
		{"a turn in the history only", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			hookHistory(t, f.child.ID, "SessionStart", "Stop")
		}, []string{"urgent: "}, false},
		{"a resolving transcript path is still the proof", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			if err := os.WriteFile(f.transcript, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, []string{freshStartLine}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.freshStart(time.Now().Add(-time.Second))
			path, _ := runtimeTokenPath(freshStartDir, f.child.ID, ".json")
			if data, _ := os.ReadFile(path); strings.Contains(string(data), "turn_seq") {
				t.Fatalf("precondition: a legacy token has no turn_seq, got %s", data)
			}
			st.hook = sessionStartNow()
			c.setup(t, f, st)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if got := f.pending(t); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("records:\n got %q\nwant %q", got, c.want)
			}
			if f.freshTokenLeft() != c.left {
				t.Fatalf("token left = %v, want %v", !c.left, c.left)
			}
		})
	}
}

// The token records turn_seq even at 0 (a new instance with no hook file), so
// the judge can tell it from a token an older binary wrote.
func TestIssue3012_FreshStartTokenRecordsZeroTurnSeq(t *testing.T) {
	home := isolatedHomeDir(t)
	installClaudeHooks(t, home)
	inst := newGuardInstance(t, home)
	inst.ID = "fresh-3012-zero"
	_ = inst.buildClaudeCommand("claude")
	noteFreshStart(inst, time.Now())
	path, err := runtimeTokenPath(freshStartDir, inst.ID, ".json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"turn_seq":0`) {
		t.Fatalf("token %s (%v), want turn_seq 0 recorded", data, err)
	}
}

// readHookTurnSeq takes the larger of the hook file's count and <id>.turnseq's,
// so either one alone is enough.
func TestIssue3012_ReadHookTurnSeqTakesTheLarger(t *testing.T) {
	isolatedHomeDir(t)
	for _, c := range []struct {
		name, hook, side string
		want             uint64
	}{
		{"hook file only", `{"event":"Stop","turn_seq":4}`, "", 4},
		{"sidecar only (hook file removed)", "", "6", 6},
		{"sidecar larger", `{"event":"Stop","turn_seq":4}`, "6", 6},
		{"hook file larger (written by step 1)", `{"event":"Stop","turn_seq":9}`, "6", 9},
		{"sidecar unreadable", `{"event":"Stop","turn_seq":4}`, "x", 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := "seq-" + strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(c.name)
			if c.hook != "" {
				writeHookFileForTest(t, id, c.hook)
			}
			if c.side != "" {
				path := filepath.Join(filepath.Dir(hookStatusFilePath(id)), id+".turnseq")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(c.side), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := readHookTurnSeq(id); got != c.want {
				t.Fatalf("readHookTurnSeq = %d, want %d", got, c.want)
			}
		})
	}
}

// A sandbox scope can outlive its sandbox: a stale scoped sidecar must not
// hide the live flat one.
func TestIssue3012_ReadHookTurnSeqLeftoverScope(t *testing.T) {
	isolatedHomeDir(t)
	const id = "seq-scope"
	scoped := filepath.Join(GetHooksDir(), "sandbox", id)
	if err := os.MkdirAll(scoped, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scoped, id+".turnseq"), []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(GetHooksDir(), id+".turnseq"), []byte("6"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readHookTurnSeq(id); got != 6 {
		t.Fatalf("readHookTurnSeq = %d, want the live flat count 6", got)
	}
}
