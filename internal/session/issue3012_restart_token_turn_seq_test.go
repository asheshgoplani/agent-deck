package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #3012 step 3, the restart token side: turn_seq is recorded only for a
// Claude child whose every turn bumps it (local, unwrapped, the claude binary
// itself, Claude's hooks on and installed in its config dir), and only once
// the hook file shows the last turn closed, so a Stop still on its way at the
// restart is not read as a turn after it. Otherwise the token has no turn_seq
// and the transcript rule judges it. The transcript end is recorded either way.

// restartHookFile writes the child's hook file with event and count, and
// points the judge's hook-file seam at what it parses to.
func (st *pidStub) restartHookFile(t *testing.T, f *turnTestFixture, event string, seq uint64) {
	t.Helper()
	writeHookFileForTest(t, f.child.ID, fmt.Sprintf(`{"status":"waiting","event":%q,"ts":%d,"turn_seq":%d}`, event, time.Now().Unix(), seq))
	st.hook = readHookStatusFile(f.child.ID)
	if st.hook == nil || st.hook.TurnSeq != seq {
		t.Fatalf("precondition: hook file %+v, want turn_seq %d", st.hook, seq)
	}
}

// restartTokenSeq is the child's restart token's turn_seq, nil for none; the
// token must exist and hold its transcript end.
func restartTokenSeq(t *testing.T, f *turnTestFixture) *uint64 {
	t.Helper()
	tok, ok := readRestartToken(f.child.ID)
	if !ok || tok.Transcript != f.transcript || tok.Offset == 0 {
		t.Fatalf("restart token %+v ok=%v, want one with the transcript end", tok, ok)
	}
	return tok.TurnSeq
}

func seqString(p *uint64) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprint(*p)
}

// Which children's tokens record turn_seq. A child whose transcript does not
// resolve on this host (noToken) gets no token at all, as before: step 3 does
// not start writing tokens for children that had none.
func TestIssue3012_RestartTokenTurnSeqGate(t *testing.T) {
	const want, noToken = "6", "no token"
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, home string, f *turnTestFixture)
		want  string
	}{
		{"Claude, hooks installed", func(t *testing.T, home string, f *turnTestFixture) {}, want},
		{"configured path to a claude binary", func(t *testing.T, home string, f *turnTestFixture) {
			writeConfigTOML(t, home, "[claude]\ncommand = \"/opt/tools/bin/claude\"\n")
		}, want},
		{"hooks not installed", func(t *testing.T, home string, f *turnTestFixture) {
			if err := os.Remove(filepath.Join(home, ".claude", "settings.json")); err != nil {
				t.Fatal(err)
			}
		}, "none"},
		{"Claude hooks turned off", func(t *testing.T, home string, f *turnTestFixture) {
			writeConfigTOML(t, home, "[claude]\nhooks_enabled = false\n")
		}, "none"},
		{"hooks not installed in the child's config dir", func(t *testing.T, home string, f *turnTestFixture) {
			writeConfigTOML(t, home, "[claude]\nconfig_dir = \""+filepath.Join(home, "other-claude")+"\"\n")
		}, noToken},
		{"a config that does not load", func(t *testing.T, home string, f *turnTestFixture) {
			writeConfigTOML(t, home, "[claude\n")
		}, "none"},
		{"configured alias that picks its own config dir", func(t *testing.T, home string, f *turnTestFixture) {
			writeConfigTOML(t, home, "[claude]\ncommand = \"cdw\"\n")
		}, "none"},
		{"inline env before the binary", func(t *testing.T, home string, f *turnTestFixture) {
			writeConfigTOML(t, home, "[claude]\ncommand = \"CLAUDE_CONFIG_DIR=/elsewhere claude\"\n")
		}, "none"},
		{"SSH", func(t *testing.T, home string, f *turnTestFixture) { f.child.SSHHost = "build-box" }, noToken},
		{"behind its own wrapper", func(t *testing.T, home string, f *turnTestFixture) {
			f.child.Wrapper = "ssh build-box {command}"
		}, "none"},
		{"behind its tool definition's wrapper", func(t *testing.T, home string, f *turnTestFixture) {
			writeConfigTOML(t, home, "[tools.myclaude]\ncommand = \"claude\"\ncompatible_with = \"claude\"\nwrapper = \"env CLAUDE_CONFIG_DIR=/elsewhere {command}\"\n")
			f.child.Tool = "myclaude"
		}, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			home := os.Getenv("HOME")
			installClaudeHooks(t, home)
			f.finishedTurn(t)
			st.restartHookFile(t, f, "Stop", 6)
			c.setup(t, home, f)
			noteRestartToken(f.child, time.Now())
			if c.want == noToken {
				if _, ok := readRestartToken(f.child.ID); ok {
					t.Fatalf("a child whose transcript does not resolve here gets no token, as before")
				}
				return
			}
			if got := seqString(restartTokenSeq(t, f)); got != c.want {
				t.Fatalf("turn_seq %s, want %s", got, c.want)
			}
		})
	}
}

// The race: the count is recorded only when the hook file shows the last
// turn closed and its count is the instance's.
func TestIssue3012_RestartTokenWaitsForTheTurnToSettle(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, f *turnTestFixture, st *pidStub)
		want  string
	}{
		{"the turn's Stop landed", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.restartHookFile(t, f, "Stop", 6)
		}, "6"},
		{"a SessionStart (resumed, cleared or compacted), no prompt since", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.restartHookFile(t, f, "SessionStart", 6)
		}, "6"},
		{"the prompt was submitted, its Stop not landed yet", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.restartHookFile(t, f, "UserPromptSubmit", 5)
		}, "none"},
		{"a tool event last, the Stop not landed yet", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.restartHookFile(t, f, "PostToolUse", 5)
		}, "none"},
		{"a permission request last", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.restartHookFile(t, f, "PermissionRequest", 5)
		}, "none"},
		// The hook handler writes the sidecar first, then the hook file.
		{"the Stop half written: the sidecar ahead of the hook file", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.restartHookFile(t, f, "Stop", 5)
			f.turnSeqFile(t, 6)
		}, "none"},
		// The TUI removes the hook file on attach-return: nothing says whether
		// a Stop is on its way.
		{"no hook file, only the sidecar", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			if err := os.MkdirAll(filepath.Dir(hookStatusFilePath(f.child.ID)), 0o700); err != nil {
				t.Fatal(err)
			}
			f.turnSeqFile(t, 6)
			st.hook = nil
		}, "none"},
		// No count anywhere yet (a hook file from before turn_seq): 0 is a
		// count, recorded as one.
		{"a settled hook file with no count", func(t *testing.T, f *turnTestFixture, st *pidStub) {
			writeHookFileForTest(t, f.child.ID, `{"status":"waiting","event":"Stop","ts":1700000000}`)
			st.hook = readHookStatusFile(f.child.ID)
		}, "0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			installClaudeHooks(t, os.Getenv("HOME"))
			f.finishedTurn(t)
			c.setup(t, f, st)
			noteRestartToken(f.child, time.Now())
			if got := seqString(restartTokenSeq(t, f)); got != c.want {
				t.Fatalf("turn_seq %s, want %s", got, c.want)
			}
			if c.want == "0" {
				path, _ := restartTokenPath(f.child.ID, ".json")
				if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"settled_turn_seq":0`) {
					t.Fatalf("a recorded 0 must be in the token, got %s", data)
				}
			}
		})
	}
}

// End to end: the child reads idle with its reply in the transcript while its
// Stop hook is still on its way (the hook file shows the prompt); the restart
// takes its token, then the Stop lands and bumps the count. The resumed boot
// is one info line, not news. Had the token taken the count before the Stop,
// the late Stop would have read as a turn since the restart.
func TestIssue3012_InFlightStopAtTheRestartIsNotATurn(t *testing.T) {
	for _, c := range []struct {
		name     string
		takeSeq  bool // the token takes the count regardless (what a naive token would do)
		wantInfo bool
	}{
		{"the token waits for the Stop", false, true},
		{"a token that took the count before the Stop", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			installClaudeHooks(t, os.Getenv("HOME"))
			n := f.finishedTurn(t)
			st.restartHookFile(t, f, "UserPromptSubmit", 5) // the Stop is on its way
			at := time.Now()
			st.restart(f, at)
			tok, ok := readRestartToken(f.child.ID)
			if !ok || tok.TurnSeq != nil {
				t.Fatalf("precondition: the token has no turn_seq while the Stop is on its way, got %+v", tok)
			}
			if c.takeSeq {
				seq := uint64(5)
				tok.TurnSeq = &seq
				writeRestartFile(f.child.ID, ".json", tok)
			}
			writeHookFileForTest(t, f.child.ID, `{"status":"waiting","event":"Stop","ts":1700000000,"turn_seq":6}`) // the late Stop
			if got := readHookTurnSeq(f.child.ID); got != 6 {
				t.Fatalf("precondition: the late Stop bumps the count, got %d", got)
			}
			f.appendTurn(t, fxResumeMeta())
			st.newHook("waiting", "SessionStart", at)
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
			added := f.inboxRecords(t)[n:]
			info := len(added) == 1 && added[0].Text == "Restarted while idle; conversation resumed, no new turn."
			if info != c.wantInfo {
				t.Fatalf("want the info line=%v, got %+v", c.wantInfo, added)
			}
		})
	}
}

// The fresh-start token keeps its gate after the shared helper took it over:
// one case each way (TestIssue2954_FreshStartToken has the table).
func TestIssue3012_FreshStartGateUnchanged(t *testing.T) {
	for _, hooks := range []bool{true, false} {
		home := isolatedHomeDir(t)
		if hooks {
			installClaudeHooks(t, home)
		}
		inst := newGuardInstance(t, home)
		inst.ID = fmt.Sprintf("fresh-3012-gate-%v", hooks)
		_ = inst.buildClaudeCommand("claude")
		noteFreshStart(inst, time.Now())
		if got := hasFreshStartToken(inst.ID); got != hooks {
			t.Fatalf("hooks installed=%v: token=%v", hooks, got)
		}
	}
}
