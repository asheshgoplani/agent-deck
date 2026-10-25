package sessionref_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionref"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// claudeWorking is a Claude pane in the middle of a turn: a live spinner
// line with its token timer and interrupt hint above the composer. In vim
// mode the insert would start with Escape, and Escape interrupts this turn.
const claudeWorking = "⏺ Running the tests.\n\n" +
	"✻ Pondering… (12s · ↑ 1.2k tokens · esc to interrupt)\n\n" +
	"────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────\n"

// vimTarget is B with a vim-mode composer (no real tmux behind it: the fake
// Insert/Capture own the pane).
func vimTarget(st session.Status) *session.Instance {
	b := inst("id-b", "B", "/pb", st)
	b.SetTmuxSessionForTest(&tmux.Session{Name: "agentdeck_ref_vim_fake", VimMode: true})
	return b
}

// TestHand_VimTargetWorkingIsRefusedBeforeAnyKey (R2-3): a vim-mode B that
// is working, by its pane or by its status, is refused with target_busy_vim
// and gets no key at all, so no Escape can interrupt its turn.
func TestHand_VimTargetWorkingIsRefusedBeforeAnyKey(t *testing.T) {
	if !session.PaneBusy("claude", claudeWorking) || session.PaneBusy("claude", claudeIdleComposer) {
		t.Fatal("fixtures: the working pane must read busy and the idle composer must not")
	}
	cases := []struct {
		name   string
		status session.Status
		pane   string
	}{
		{"pane shows a live spinner", session.StatusWaiting, claudeWorking},
		{"status is running (pane looks idle)", session.StatusRunning, claudeIdleComposer},
		{"both", session.StatusRunning, claudeWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := inst("id-a", "A", "/pa", session.StatusIdle)
			b := vimTarget(tc.status)
			f := &fakeInsert{}
			res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
				sessionref.Options{Insert: f.fn, Capture: paneOf(tc.pane), Running: alwaysRunning, Transcript: noTranscript})
			var r *sessionref.Refusal
			if !errors.As(err, &r) || r.Code != sessionref.CodeTargetBusyVim {
				t.Fatalf("err = %v, want target_busy_vim", err)
			}
			if !strings.Contains(r.Message, `"B" is working; drop again when it is waiting`) {
				t.Errorf("message = %q", r.Message)
			}
			if len(f.calls) != 0 {
				t.Fatalf("typed into a working vim-mode target: %q", f.calls)
			}
			if res != nil && res.Inserted {
				t.Fatalf("result says inserted: %+v", res)
			}
		})
	}
}

// TestHand_BusyRuleOnlyForVimAndOnlyWhileWorking: controls. A vim-mode B
// that is waiting takes the reference, and a working B without vim mode
// does too (no Escape is sent there; the text waits in its composer).
func TestHand_BusyRuleOnlyForVimAndOnlyWhileWorking(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)

	waiting := vimTarget(session.StatusWaiting)
	f := &fakeInsert{}
	res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: waiting},
		sessionref.Options{Insert: f.fn, Capture: paneOf(claudeIdleComposer), Running: alwaysRunning, Transcript: noTranscript})
	if err != nil || !res.Inserted || len(f.calls) != 1 {
		t.Fatalf("waiting vim target: err=%v res=%+v calls=%q", err, res, f.calls)
	}

	plain := inst("id-c", "C", "/pc", session.StatusRunning)
	f = &fakeInsert{}
	res, err = sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: plain},
		sessionref.Options{Insert: f.fn, Capture: paneOf(claudeWorking), Running: alwaysRunning, Transcript: noTranscript})
	if err != nil || !res.Inserted || len(f.calls) != 1 {
		t.Fatalf("working target without vim mode: err=%v res=%+v calls=%q", err, res, f.calls)
	}
}

// TestHand_RealVimPaneWorkingGetsNoEscape runs the default tmux path on a
// private server (-L session-ref-test-<pid>). The pane's tty echoes control
// keys (Escape shows as ^[), so any key would change it. A vim-mode B whose
// pane shows a working Claude turn is refused and its pane is unchanged; the
// control, the same vim-mode setup on a waiting composer, does get Escape,
// i and the line.
func TestHand_RealVimPaneWorkingGetsNoEscape(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	socket := fmt.Sprintf("session-ref-test-%d", os.Getpid())
	a := inst("aaaa1111-1700000000", "alpha", "/work/alpha", session.StatusIdle)

	busy := inst("bbbb2222-1700000000", "beta", "/work/beta", session.StatusWaiting)
	busyName := "agentdeck_ref_busy_vim_probe"
	ts := startFixturePane(t, socket, busyName, claudeWorking)
	ts.VimMode = true
	busy.SetTmuxSessionForTest(ts)
	before := paneText(t, socket, busyName)

	_, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: busy},
		sessionref.Options{Running: alwaysRunning, Transcript: noTranscript})
	var r *sessionref.Refusal
	if !errors.As(err, &r) || r.Code != sessionref.CodeTargetBusyVim {
		t.Fatalf("err = %v, want target_busy_vim", err)
	}
	time.Sleep(300 * time.Millisecond) // give any stray key time to echo
	if after := paneText(t, socket, busyName); after != before || strings.Contains(after, "^[") || strings.Contains(after, a.ID) {
		t.Fatalf("working vim pane changed after a refused insert:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Control: a waiting vim-mode composer gets Escape (echoed ^[), i and the line.
	idle := inst("cccc3333-1700000000", "gamma", "/work/gamma", session.StatusWaiting)
	idleName := "agentdeck_ref_idle_vim_probe"
	its := startFixturePane(t, socket, idleName, claudeIdleComposer)
	its.VimMode = true
	idle.SetTmuxSessionForTest(its)
	res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: idle},
		sessionref.Options{Running: alwaysRunning, Transcript: noTranscript})
	if err != nil || !res.Inserted {
		t.Fatalf("control insert: err=%v res=%+v", err, res)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := paneText(t, socket, idleName)
		if strings.Contains(got, "^[i[ref]") && strings.Contains(got, a.ID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("control: Escape, i and the line never reached the waiting vim pane:\n%s", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
