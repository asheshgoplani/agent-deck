package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #3012 step 3: the restart judge asks "a turn since the restart?" as
// the instance's turn_seq against the token's, when the token recorded one.
// The same count is no turn; a higher one is a turn, a lower one the hook file
// and its sidecar recreated (something happened). The transcript the daemon
// resolves no longer matters for such a token, so a path that differs from the
// restart's (CLAUDE_CONFIG_DIR in the launching shell) is no longer news, and a
// turn the resolved transcript does not show (a /clear the saved id did not
// follow) is no longer hidden. A token with no turn_seq (an older binary, or
// one taken while a turn edge could still land) keeps the transcript rule.
//
// The tokens here are written raw, the way the restart writes them
// (settled_turn_seq), so these cases read the same on a binary before step 3:
// there the count is ignored and the transcript decides.

// restartSeqAt is the turn_seq the token records in these cases.
const restartSeqAt = 4

// noSeq is a token with no turn_seq: an older binary's.
const noSeq = -1

// restartWithRawToken is a restart of the child after its finished turn: the
// token (seq, or none for noSeq; the transcript the restarting process
// resolved and its size), the old process killed, the new one spawned and
// recorded, and its SessionStart in the hook file the judge reads.
func (f *turnTestFixture) restartWithRawToken(t *testing.T, st *pidStub, at time.Time, transcript string, seq int) {
	t.Helper()
	info, err := os.Stat(transcript)
	if err != nil {
		t.Fatal(err)
	}
	tok := map[string]any{"at": at, "tool": "claude", "old_pid": st.pane, "transcript": transcript, "offset": info.Size()}
	if seq != noSeq {
		tok["settled_turn_seq"] = seq
	}
	writeRestartFile(f.child.ID, ".json", tok)
	if _, ok := readRestartToken(f.child.ID); !ok {
		t.Fatalf("precondition: token unreadable")
	}
	st.alive[st.pane] = false
	st.pane += 100
	st.alive[st.pane] = true
	markRestartSpawned(f.child, at)
	st.newHook("waiting", "SessionStart", at)
}

// transcriptElsewhere is the same conversation's transcript under another
// config dir: the path a restart launched with CLAUDE_CONFIG_DIR set in its
// shell resolves, while the daemon resolves the fixture's.
func (f *turnTestFixture) transcriptElsewhere(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.transcript)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "other-claude", "projects", "p", f.child.ClaudeSessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// noHookFiles removes the hook file and its turn_seq sidecar: the prune, or a
// manual unacknowledge followed by a lost sidecar. The count reads 0.
func noHookFiles(t *testing.T, id string) {
	t.Helper()
	dir := filepath.Dir(hookStatusFilePath(id))
	for _, name := range []string{id + ".json", id + ".turnseq"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

// restartSeqCase is one restart: the token's turn_seq (noSeq for none), the
// transcript it recorded, and what happened after it.
type restartSeqCase struct {
	name      string
	seq       int
	elsewhere bool // the token's transcript is not the one the daemon resolves
	after     func(t *testing.T, f *turnTestFixture)
}

// The cases every check below shares, with the verdict "no turn since the
// restart" each one must reach.
func restartSeqCases() []struct {
	restartSeqCase
	noTurn bool
} {
	countIs := func(seq uint64) func(t *testing.T, f *turnTestFixture) {
		return func(t *testing.T, f *turnTestFixture) { f.turnSeq(t, seq) }
	}
	type c = restartSeqCase
	return []struct {
		restartSeqCase
		noTurn bool
	}{
		{c{"no turn", restartSeqAt, false, countIs(restartSeqAt)}, true},
		// Fixed by step 3: the restart resolved another transcript path than
		// the daemon does, which read as a turn.
		{c{"no turn, the restart resolved another transcript", restartSeqAt, true, countIs(restartSeqAt)}, true},
		{c{"no turn, history off", restartSeqAt, false, func(t *testing.T, f *turnTestFixture) {
			f.turnSeq(t, restartSeqAt)
			noHookHistory(t, f.child.ID)
		}}, true},
		// The TUI removes the hook file on attach-return; the sidecar keeps
		// the count.
		{c{"no turn, hook file removed, sidecar keeps the count", restartSeqAt, false, func(t *testing.T, f *turnTestFixture) {
			noHookFiles(t, f.child.ID)
			f.turnSeqFile(t, restartSeqAt)
		}}, true},
		{c{"a turn in the transcript", restartSeqAt, false, func(t *testing.T, f *turnTestFixture) {
			f.appendTurn(t, fxHuman("u2", "next task"), fxAssistantText("a2", "Done."))
			f.turnSeq(t, restartSeqAt+2)
		}}, false},
		// Fixed by step 3: a turn, then a /clear the saved id did not follow;
		// the transcript the instance resolves shows nothing new.
		{c{"a turn the resolved transcript does not show", restartSeqAt, false, countIs(restartSeqAt + 2)}, false},
		{c{"a turn still running (prompt submitted)", restartSeqAt, false, countIs(restartSeqAt + 1)}, false},
		// The hook handler skipped the turn's events (a missing project dir,
		// a lock it could not open): the count did not move, but the token's
		// own transcript shows the turn, and that is news.
		{c{"a turn the hooks missed, in the token's own transcript", restartSeqAt, false, func(t *testing.T, f *turnTestFixture) {
			f.appendTurn(t, fxHuman("u2", "next task"), fxAssistantText("a2", "Done."))
			f.turnSeq(t, restartSeqAt)
		}}, false},
		// The transcript only adds news on a complete record: a partial line
		// is no evidence of a turn.
		{c{"a partial line in the token's own transcript, count unchanged", restartSeqAt, false, func(t *testing.T, f *turnTestFixture) {
			f.appendTurn(t, `{"type":"assistant","uuid":"a3","message":{"role":"assis`)
			f.turnSeq(t, restartSeqAt)
		}}, true},
		// Fixed by step 3: the count went back (the prune removed the hook
		// file and its sidecar): something happened.
		{c{"the count went back", restartSeqAt, false, func(t *testing.T, f *turnTestFixture) {
			noHookFiles(t, f.child.ID)
			f.turnSeq(t, 2)
		}}, false},
		// Fixed by step 3: a recorded 0 is a count, not an older binary's
		// token.
		{c{"recorded 0, then a turn", 0, false, countIs(2)}, false},
		// An older binary's token: the count is not read, the transcript
		// rule decides as before.
		{c{"older binary token, count moved, transcript unchanged", noSeq, false, countIs(9)}, true},
		{c{"older binary token, another transcript", noSeq, true, countIs(restartSeqAt)}, false},
		{c{"older binary token, a turn in the transcript", noSeq, false, func(t *testing.T, f *turnTestFixture) {
			f.appendTurn(t, fxHuman("u2", "next task"), fxAssistantText("a2", "Done."))
		}}, false},
	}
}

// setupRestartSeq runs a case up to the judgement: a finished turn, the
// restart at `at`, then the case's events.
func setupRestartSeq(t *testing.T, c restartSeqCase, at time.Time) (*turnTestFixture, *pidStub, int) {
	t.Helper()
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	n := f.finishedTurn(t)
	f.turnSeq(t, restartSeqAt) // the count at the restart (a recorded 0 case resets it below)
	transcript := f.transcript
	if c.elsewhere {
		transcript = f.transcriptElsewhere(t)
	}
	f.restartWithRawToken(t, st, at, transcript, c.seq)
	f.appendTurn(t, fxResumeMeta())
	c.after(t, f)
	// The resumed process's hook carries the count, as a counting handler
	// does (max of the file and the sidecar).
	if st.hook != nil {
		st.hook.TurnSeq = readHookTurnSeq(f.child.ID)
	}
	return f, st, n
}

// judgeRestart: the new process at its prompt is the boot only with no turn.
func TestIssue3012_RestartJudgedByTurnSeq(t *testing.T) {
	for _, c := range restartSeqCases() {
		t.Run(c.name, func(t *testing.T) {
			at := time.Now()
			f, _, _ := setupRestartSeq(t, c.restartSeqCase, at)
			_, verdict, _ := f.d.judgeRestart(f.child.ID, f.child.GetJSONLPath(), "", func() string { return "" }, "waiting", time.Now())
			if want := map[bool]restartVerdict{true: restartBoot, false: restartNews}[c.noTurn]; verdict != want {
				t.Fatalf("verdict %v, want %v", verdict, want)
			}
		})
	}
}

// The same through emitTurn, as the parent sees it: the restart whose
// transcript path differs from the daemon's is one info line, not news.
func TestIssue3012_RestartAnotherTranscriptIsOneInfoLine(t *testing.T) {
	for _, observed := range []bool{true, false} {
		f, _, n := setupRestartSeq(t, restartSeqCase{"", restartSeqAt, true, func(t *testing.T, f *turnTestFixture) {}}, time.Now())
		sends := *f.sends
		f.bootSeen()
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), observed)
		added := f.inboxRecords(t)[n:]
		if len(added) != 1 || added[0].IsUrgent() || added[0].Text != "Restarted while idle; conversation resumed, no new turn." {
			t.Fatalf("observed=%v: want exactly the one info line, got %+v", observed, added)
		}
		if *f.sends != sends {
			t.Fatalf("observed=%v: no wake, sends %d -> %d", observed, sends, *f.sends)
		}
	}
}

// restartBootUnseen (#2813): the sweep's info line for a boot flip never
// seen, by the same rule. Its SessionStart/SessionEnd ordering still reads
// the hook event history: an old SessionEnd on file with the history off
// proves nothing, with or without turn_seq.
func TestIssue3012_RestartBootUnseenByTurnSeq(t *testing.T) {
	for _, c := range restartSeqCases() {
		t.Run(c.name, func(t *testing.T) {
			at := time.Now().Add(-10 * time.Second)
			f, _, _ := setupRestartSeq(t, c.restartSeqCase, at)
			tok, _ := readRestartToken(f.child.ID)
			if got := restartBootUnseen(f.child, tok, "waiting", time.Now()); got != c.noTurn {
				t.Fatalf("restartBootUnseen = %v, want %v", got, c.noTurn)
			}
		})
	}
	for _, history := range []bool{true, false} {
		t.Run("old SessionEnd landed after the new SessionStart, history "+map[bool]string{true: "on", false: "off"}[history], func(t *testing.T) {
			at := time.Now().Add(-10 * time.Second)
			f, st, _ := setupRestartSeq(t, restartSeqCase{"", restartSeqAt, false, func(t *testing.T, f *turnTestFixture) { f.turnSeq(t, restartSeqAt) }}, at)
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: at.Truncate(time.Second), TurnSeq: restartSeqAt}
			if history {
				hookHistory(t, f.child.ID, "SessionStart")
			} else {
				noHookHistory(t, f.child.ID)
			}
			tok, _ := readRestartToken(f.child.ID)
			if got := restartBootUnseen(f.child, tok, "error", time.Now()); got != history {
				t.Fatalf("restartBootUnseen = %v, want %v (the ordering proof needs the history)", got, history)
			}
		})
	}
}

// The timeout sweep: a new process alive but still running past the boot
// timeout is reported unless it is already on a new turn.
func TestIssue3012_RestartTimeoutSweepByTurnSeq(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "30s")
	for _, c := range restartSeqCases() {
		t.Run(c.name, func(t *testing.T) {
			f, _, n := setupRestartSeq(t, c.restartSeqCase, time.Now().Add(-time.Minute))
			statuses := map[string]string{f.child.ID: "running", f.parent.ID: "waiting"}
			f.d.sweepRestartTokens("default", f.byID, statuses, time.Now())
			added := f.inboxRecords(t)[n:]
			reported := len(added) == 1 && added[0].IsUrgent() && strings.Contains(added[0].Text, "did not come back to its prompt")
			if reported != c.noTurn || (!c.noTurn && len(added) != 0) {
				t.Fatalf("want reported=%v, got %+v", c.noTurn, added)
			}
			if _, left := readRestartToken(f.child.ID); left {
				t.Fatalf("the timeout spends the token")
			}
		})
	}
}

// restartBootRecord, the notifier's own check: the boot record is dropped
// only with no turn. Its transcript rule reads the token's own transcript, so
// the path cases are the same for it either way.
func TestIssue3012_RestartBootRecordByTurnSeq(t *testing.T) {
	for _, c := range restartSeqCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.elsewhere && c.seq == noSeq {
				t.Skip("its transcript rule reads the token's own transcript, never the daemon's path")
			}
			f, _, _ := setupRestartSeq(t, c.restartSeqCase, time.Now())
			ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ToStatus: "waiting", Timestamp: time.Now()}
			if got := restartBootRecord(ev); got != c.noTurn {
				t.Fatalf("restartBootRecord = %v, want %v", got, c.noTurn)
			}
		})
	}
}

// restartResumeEcho: after the unseen-boot info line, a flip is the same
// resume only with no turn.
func TestIssue3012_RestartResumeEchoByTurnSeq(t *testing.T) {
	for _, c := range restartSeqCases() {
		t.Run(c.name, func(t *testing.T) {
			at := time.Now().Add(-10 * time.Second)
			f, _, _ := setupRestartSeq(t, c.restartSeqCase, at)
			tok, _ := readRestartToken(f.child.ID)
			rs := f.d.restarts()
			rs.mu.Lock()
			rs.banner[f.child.ID] = restartBannerWatch{profile: "default", tok: tok, until: at.Add(2 * time.Minute)}
			rs.mu.Unlock()
			clearRestartToken(f.child.ID) // the info line spent it
			if got := f.d.restartResumeEcho(f.child, "waiting", time.Now(), false); got != c.noTurn {
				t.Fatalf("restartResumeEcho = %v, want %v", got, c.noTurn)
			}
		})
	}
}

// A token from a binary before step 3 carries the count under "turn_seq",
// taken for every child and without waiting for a Stop on its way: the judge
// does not trust it, and the transcript rule decides.
func TestIssue3012_PreStep3TurnSeqKeyKeepsTheTranscriptRule(t *testing.T) {
	for _, c := range []struct {
		name      string
		seq       uint64
		elsewhere bool
		boot      bool
	}{
		{"count moved, transcript unchanged", restartSeqAt + 2, false, true},
		{"count unchanged, another transcript", restartSeqAt, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _, _ := setupRestartSeq(t, restartSeqCase{"", noSeq, c.elsewhere, func(t *testing.T, f *turnTestFixture) {}}, time.Now())
			tok, _ := readRestartToken(f.child.ID)
			raw := map[string]any{"at": tok.At, "tool": "claude", "old_pid": tok.OldPID, "transcript": tok.Transcript, "offset": tok.Offset, "turn_seq": restartSeqAt}
			writeRestartFile(f.child.ID, ".json", raw)
			f.turnSeq(t, c.seq)
			_, verdict, _ := f.d.judgeRestart(f.child.ID, f.child.GetJSONLPath(), "", func() string { return "" }, "waiting", time.Now())
			if want := map[bool]restartVerdict{true: restartBoot, false: restartNews}[c.boot]; verdict != want {
				t.Fatalf("verdict %v, want %v", verdict, want)
			}
		})
	}
}

// The count is trusted only once a hook of the resumed process has fired
// since the restart. A restart can start the new process under a config dir
// with no agent-deck hooks (one-shot env, a regenerated worker scratch dir):
// none ever fires, the count never moves, and its next reply must still be
// news. Until a hook is seen, the transcript rule decides, as before #3012.
// Here the resumed process writes its transcript where the daemon looks, not
// where the restart recorded it.
func TestIssue3012_RestartCountNeedsTheResumedHooks(t *testing.T) {
	for _, c := range []struct {
		name    string
		history bool // the resumed process's SessionStart is in the hook event history
		boot    bool
	}{
		{"no hook since the restart: the transcript rule, its reply is news", false, false},
		// The control: with the resumed process's hooks proven and no turn,
		// the count decides and the other path does not matter.
		{"its SessionStart in the history, no turn: the count decides", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			at := time.Now().Add(-10 * time.Second)
			f, st, _ := setupRestartSeq(t, restartSeqCase{"", restartSeqAt, true, func(t *testing.T, f *turnTestFixture) {
				if !c.history {
					f.appendTurn(t, fxHuman("u2", "next task"), fxAssistantText("a2", "Done."))
				}
				f.turnSeq(t, restartSeqAt)
			}}, at)
			// The old process's SessionEnd is the last hook event.
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: at.Truncate(time.Second), TurnSeq: restartSeqAt}
			if c.history {
				hookHistory(t, f.child.ID, "SessionEnd", "SessionStart")
			} else {
				noHookHistory(t, f.child.ID)
			}
			_, verdict, _ := f.d.judgeRestart(f.child.ID, f.child.GetJSONLPath(), "", func() string { return "" }, "waiting", time.Now())
			if want := map[bool]restartVerdict{true: restartBoot, false: restartNews}[c.boot]; verdict != want {
				t.Fatalf("verdict %v, want %v", verdict, want)
			}
		})
	}
}

// The count is not trusted when the resumed process's hook file carries no
// count: its handler does not count turns (older than #3080), as one in a
// config dir the restart switched to can be. The sidecar keeps the old count,
// so the count would never move. The transcript rule decides: its reply, on
// another path than the token's, is news.
func TestIssue3012_RestartHookFileWithoutACountKeepsTheTranscriptRule(t *testing.T) {
	at := time.Now().Add(-10 * time.Second)
	f, st, _ := setupRestartSeq(t, restartSeqCase{"", restartSeqAt, true, func(t *testing.T, f *turnTestFixture) {
		f.appendTurn(t, fxHuman("u2", "next task"), fxAssistantText("a2", "Done."))
		f.turnSeqFile(t, restartSeqAt)
		writeHookFileForTest(t, f.child.ID, fmt.Sprintf(`{"status":"waiting","event":"Stop","ts":%d}`, time.Now().Unix()))
	}}, at)
	st.hook = readHookStatusFile(f.child.ID)
	if st.hook == nil || st.hook.TurnSeq != 0 || readHookTurnSeq(f.child.ID) != restartSeqAt {
		t.Fatalf("precondition: a hook file with no count and the sidecar at %d, got %+v", restartSeqAt, st.hook)
	}
	_, verdict, _ := f.d.judgeRestart(f.child.ID, f.child.GetJSONLPath(), "", func() string { return "" }, "waiting", time.Now())
	if verdict != restartNews {
		t.Fatalf("verdict %v, want news", verdict)
	}
}
