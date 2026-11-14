package sessionref_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionref"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Pane fixtures (ASK-111 HIGH-1). Each one is a screen where typed keys
// would answer a question: a digit picks an option, y/n/a approve or deny,
// Escape dismisses it.
const (
	claudePermissionMenu = "⏺ Bash(make test)\n\n" +
		" Do you want to proceed?\n" +
		" ❯ 1. Yes\n" +
		"   2. Yes, and don't ask again for make commands in /work/beta\n" +
		"   3. No, and tell Claude what to do differently (esc)\n"
	claudeAskUserQuestion = "⏺ I can take two routes.\n\n" +
		" Which approach should I take?\n" +
		" ❯ 1. Patch the guard\n" +
		"   2. Rewrite the insert\n" +
		"   3. Type something.\n\n" +
		" Enter to select · ↑/↓ to navigate · Esc to cancel\n"
	claudeMenuUnderSpinner = "⏺ Running the lanes.\n\n✽ Thundering… (34s · ↓ 135 tokens)\n\n" +
		" Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently\n\n Esc to cancel\n"
	codexApproval = "  Would you like to run the following command?\n\n  $ make test\n\n" +
		"› 1. Yes, proceed (y)\n  2. Yes, and don't ask again (a)\n  3. No, and tell Codex what to do differently (esc)\n\n" +
		"  Press enter to confirm or esc to go back\n"
	claudeIdleComposer = "⏺ Done. Tests pass.\n\n" +
		"────────────────────────────────────────\n" +
		"❯ \n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
)

func paneOf(raw string) func(*session.Instance) (string, error) {
	return func(*session.Instance) (string, error) { return raw, nil }
}

// TestHand_RefusesTargetShowingMenu: B shows a menu or permission prompt,
// so the core refuses with target_menu_open and types nothing.
func TestHand_RefusesTargetShowingMenu(t *testing.T) {
	cases := []struct{ name, tool, pane string }{
		{"claude permission dialog", "claude", claudePermissionMenu},
		{"claude AskUserQuestion picker", "claude", claudeAskUserQuestion},
		{"claude menu under a live spinner", "claude", claudeMenuUnderSpinner},
		{"codex approval", "codex", codexApproval},
		{"menu in a pane of another tool", "shell", claudePermissionMenu},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := inst("id-a", "A", "/pa", session.StatusIdle)
			b := inst("id-b", "B", "/pb", session.StatusWaiting)
			b.Tool = tc.tool
			f := &fakeInsert{}
			res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
				sessionref.Options{Insert: f.fn, Capture: paneOf(tc.pane), Running: alwaysRunning, Transcript: noTranscript})
			var r *sessionref.Refusal
			if !errors.As(err, &r) || r.Code != sessionref.CodeTargetMenuOpen {
				t.Fatalf("err = %v, want target_menu_open", err)
			}
			if !strings.Contains(r.Message, `"B" is showing a menu/prompt; answer it first`) {
				t.Errorf("message = %q", r.Message)
			}
			if len(f.calls) != 0 {
				t.Fatalf("typed into a pane showing a menu: %q", f.calls)
			}
			if res != nil && res.Inserted {
				t.Fatalf("result says inserted: %+v", res)
			}
		})
	}
}

// TestHand_IdleComposerIsNotAMenu is the control: the same guard lets an
// idle composer take the reference.
func TestHand_IdleComposerIsNotAMenu(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusIdle)
	f := &fakeInsert{}
	res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
		sessionref.Options{Insert: f.fn, Capture: paneOf(claudeIdleComposer), Running: alwaysRunning, Transcript: noTranscript})
	if err != nil || !res.Inserted || len(f.calls) != 1 {
		t.Fatalf("idle composer: err=%v res=%+v calls=%q", err, res, f.calls)
	}
}

// TestHand_UnreadablePaneTypesNothing: when B's pane cannot be read, the
// guard cannot rule a menu out, so nothing is typed.
func TestHand_UnreadablePaneTypesNothing(t *testing.T) {
	a := inst("id-a", "A", "/pa", session.StatusIdle)
	b := inst("id-b", "B", "/pb", session.StatusIdle)
	f := &fakeInsert{}
	_, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: b},
		sessionref.Options{Insert: f.fn, Running: alwaysRunning, Transcript: noTranscript,
			Capture: func(*session.Instance) (string, error) { return "", errors.New("capture timed out") }})
	var r *sessionref.Refusal
	if !errors.As(err, &r) || r.Code != sessionref.CodeTargetNoPane {
		t.Fatalf("err = %v, want target_no_pane", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("typed into an unreadable pane: %q", f.calls)
	}
}

// startFixturePane starts a pane on the private server that prints body and
// then runs cat (typed keys echo, so anything typed shows up in the pane).
func startFixturePane(t *testing.T, socket, name, body string) *tmux.Session {
	t.Helper()
	script := filepath.Join(t.TempDir(), "pane.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat \"$0.txt\"\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script+".txt", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", name, "-x", "160", "-y", "30", script).CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "="+name).Run() })
	first := strings.SplitN(strings.TrimSpace(body), "\n", 2)[0]
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", name).CombinedOutput()
		if strings.Contains(string(out), strings.TrimSpace(first)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture pane never drew %q:\n%s", first, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &tmux.Session{Name: name, SocketName: socket}
}

func paneText(t *testing.T, socket, name string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", name).CombinedOutput()
	if err != nil {
		t.Fatalf("capture-pane: %v: %s", err, out)
	}
	return string(out)
}

// TestHand_RealPaneShowingMenuGetsNoKeys runs the default tmux path (no
// fakes) on a private server (-L session-ref-test-<pid>). B's pane shows a
// Claude permission menu: the core refuses and the pane is unchanged, even
// with a vim-mode composer (whose Escape would dismiss the dialog). The
// control pane, an idle composer, does get the line.
func TestHand_RealPaneShowingMenuGetsNoKeys(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	socket := fmt.Sprintf("session-ref-test-%d", os.Getpid())
	a := inst("aaaa1111-1700000000", "alpha", "/work/alpha", session.StatusIdle)

	menu := inst("bbbb2222-1700000000", "beta", "/work/beta", session.StatusWaiting)
	menuName := "agentdeck_ref_menu_probe"
	ts := startFixturePane(t, socket, menuName, claudePermissionMenu)
	ts.VimMode = true
	menu.SetTmuxSessionForTest(ts)
	before := paneText(t, socket, menuName)

	_, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: menu},
		sessionref.Options{Running: alwaysRunning, Transcript: noTranscript})
	var r *sessionref.Refusal
	if !errors.As(err, &r) || r.Code != sessionref.CodeTargetMenuOpen {
		t.Fatalf("err = %v, want target_menu_open", err)
	}
	time.Sleep(300 * time.Millisecond) // give any stray key time to echo
	if after := paneText(t, socket, menuName); after != before || strings.Contains(after, a.ID) {
		t.Fatalf("pane changed after a refused insert:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Control: an idle composer on the same server takes the line once.
	idle := inst("cccc3333-1700000000", "gamma", "/work/gamma", session.StatusIdle)
	idleName := "agentdeck_ref_idle_probe"
	idle.SetTmuxSessionForTest(startFixturePane(t, socket, idleName, claudeIdleComposer))
	res, err := sessionref.Hand(sessionref.Endpoint{Inst: a}, sessionref.Endpoint{Inst: idle},
		sessionref.Options{Running: alwaysRunning, Transcript: noTranscript})
	if err != nil || !res.Inserted {
		t.Fatalf("control insert: err=%v res=%+v", err, res)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(paneText(t, socket, idleName), a.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("control: line never reached the idle pane:\n%s", paneText(t, socket, idleName))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
