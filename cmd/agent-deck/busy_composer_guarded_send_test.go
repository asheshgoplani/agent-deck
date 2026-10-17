package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// `session send --require-input-prompt` (the macOS app's guarded send) to a
// busy Claude. Claude Code queues input typed while a turn runs, so a busy
// pane whose framed composer is empty takes the send; a menu, a picker, a
// permission dialog, an operator draft or an unreadable pane still refuses
// before any key is typed. The busy screens are drawn by the app audit's
// Claude-like fake (testdata/claude_like_fake.py), whose spinner line, like
// Claude 2.1's, carries no "esc to interrupt" hint.

const busyClaudeHead = "⏺ Running the lanes.\n\n✽ Thundering… (34s · ↓ 135 tokens)\n\n"

func busyClaudePane(composer string) string {
	div := strings.Repeat("─", 80)
	return busyClaudeHead + div + "\n❯ " + composer + "\n" + div + "\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
}

func guardedBusyTuning() sendExecTuning {
	tun := testGuardTuning(sendRetryOptions{maxRetries: 2, checkDelay: 0, verifyDelivery: true})
	tun.requireInputPrompt = true
	return tun
}

func TestGuardedSendDeliversToBusyEmptyComposer(t *testing.T) {
	pane := &mockSendRetryTarget{statuses: []string{"running", "active"}, panes: []string{busyClaudePane("")}}
	res, err := executeSend(pane, "claude", "queued while the turn runs", true, guardedBusyTuning())
	if err != nil || res.delivery == deliveryComposerBlocked || res.delivery == deliveryMenuOpen {
		t.Fatalf("busy empty composer refused: delivery=%q err=%v", res.delivery, err)
	}
	if got := atomic.LoadInt32(&pane.sendKeysCalls); got != 1 {
		t.Fatalf("message typed %d time(s), want 1", got)
	}
}

func TestGuardedSendRefusesBusyPaneBeforeTyping(t *testing.T) {
	cases := []struct {
		name     string
		pane     string
		paneErr  error
		delivery string
	}{
		{"permission dialog", busyClaudeHead + "⏺ Bash(make test)\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently\n\n Esc to cancel\n", nil, deliveryMenuOpen},
		{"picker", busyClaudeHead + guardedMenu + "\n", nil, deliveryMenuOpen},
		{"operator draft", busyClaudePane("half typed operator note"), nil, deliveryComposerBlocked},
		{"capture failure", "", errors.New("pane gone"), deliveryComposerBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Without --no-wait the guard's capture is the first one taken.
			pane := &mockSendRetryTarget{statuses: []string{"running"}, panes: []string{tc.pane}, paneErrs: []error{tc.paneErr}}
			res, err := executeSend(pane, "claude", "must not be typed", false, guardedBusyTuning())
			if err == nil || res.delivery != tc.delivery || !strings.Contains(err.Error(), "no keys typed") {
				t.Fatalf("delivery=%q err=%v, want %q with no keys typed", res.delivery, err, tc.delivery)
			}
			if pane.sendKeysCalls != 0 || pane.sendEnterCalls != 0 || pane.sendChunkedCalls != 0 || pane.sendCtrlCCalls != 0 {
				t.Fatalf("keys reached the pane: body=%d enter=%d chunks=%d ctrlc=%d", pane.sendKeysCalls, pane.sendEnterCalls, pane.sendChunkedCalls, pane.sendCtrlCCalls)
			}
		})
	}
}

// Busy input is a Claude capability: a busy Codex pane keeps refusing.
func TestGuardedSendKeepsRefusingBusyCodex(t *testing.T) {
	pane := &mockSendRetryTarget{statuses: []string{"running"}, panes: []string{busyClaudePane("")}}
	res, err := executeSend(pane, "codex", "must not be typed", true, guardedBusyTuning())
	if err == nil || res.delivery != deliveryComposerBlocked || pane.sendKeysCalls != 0 {
		t.Fatalf("delivery=%q err=%v body=%d", res.delivery, err, pane.sendKeysCalls)
	}
}

// swallowingGhost is a ghostComposer whose typed bytes never land (a cold
// start drops them), so an Enter pressed after typing hits the suggestion.
type swallowingGhost struct{ *ghostComposer }

func (s swallowingGhost) SendKeysAndEnter(string) error {
	s.keysTypedCount++
	return s.SendEnter()
}

func (s swallowingGhost) SendKeysAndEnterChecked(_ string, capture func() (string, error), check tmux.PostPasteCheck) error {
	s.keysTypedCount++
	if check != nil {
		if ok, err := check(capture()); !ok {
			return err
		}
	}
	return s.SendEnter()
}

func (s swallowingGhost) SendKeysChunked(string) error { s.keysTypedCount++; return nil }

// An idle composer showing a prompt suggestion takes a guarded send, but only
// through the typed text check: Enter follows once our message replaced the
// suggestion, and never while the suggestion is still what Enter would submit.
func TestGuardedSendAtSuggestionUsesTypedTextCheck(t *testing.T) {
	const msg = "AUTOMATED_GHOST_PROBE_MESSAGE"
	tun := ghostSendTuning()
	tun.requireInputPrompt = true

	g := &ghostComposer{paintDelay: 3}
	res, err := executeSend(g, "claude", msg, false, tun)
	if err != nil || res.delivery == deliveryComposerBlocked || res.delivery == deliveryMenuOpen {
		t.Fatalf("guarded send at a suggestion refused: delivery=%q err=%v", res.delivery, err)
	}
	if len(g.submitted) != 1 || g.submitted[0] != msg || g.ghostEnters != 0 {
		t.Fatalf("submitted=%q ghostEnters=%d, want our message once and no Enter at the suggestion", g.submitted, g.ghostEnters)
	}

	lost := swallowingGhost{&ghostComposer{}}
	_, _ = executeSend(lost, "claude", msg, false, tun)
	if lost.ghostEnters != 0 {
		t.Fatalf("Enter pressed %d time(s) at the suggestion after the typed text was lost", lost.ghostEnters)
	}
}

// claudeLikeFake runs testdata/claude_like_fake.py in a real tmux pane and
// switches it to phase (idle or busy). It returns the session and the
// transcript the fake appends its records to.
func claudeLikeFake(t *testing.T, phase string) (*tmux.Session, string) {
	t.Helper()
	for _, tool := range []string{"tmux", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("requires %s", tool)
		}
	}
	src, err := os.ReadFile(filepath.Join("testdata", "claude_like_fake.py"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "fake-session.jsonl")
	writeJSON := func(path string, v any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(filepath.Join(dir, "fake-running.json"), map[string]string{dir: transcript})
	writeJSON(transcript+".input-control.json", map[string]string{"id": phase, "phase": phase})

	sess := tmux.NewSession(fmt.Sprintf("guarded-busy-%d", time.Now().UnixNano()), dir)
	if err := sess.Start("python3 -I " + filepath.Join(dir, "claude")); err != nil {
		t.Fatalf("start fake: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })
	want := "All four steps ran."
	if phase == "busy" {
		want = "Thundering"
	}
	waitForPane(t, sess, func(p string) bool { return strings.Contains(p, want) && strings.Contains(p, "❯") })
	return sess, transcript
}

func waitForPane(t *testing.T, sess *tmux.Session, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		pane, _ := sess.CapturePaneFresh()
		if ok(tmux.StripANSI(pane)) {
			return pane
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never reached the wanted state:\n%s", tmux.StripANSI(pane))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// fakeQueued counts the busy-turn enqueue records the fake wrote for msg.
func fakeQueued(t *testing.T, transcript, msg string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(readGolden(t, transcript), "\n") {
		var rec struct{ Type, Operation, Content string }
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Type == "queue-operation" && rec.Operation == "enqueue" && rec.Content == msg {
			n++
		}
	}
	return n
}

// The app's form of the send (`--no-wait --require-input-prompt`) against a
// busy Claude-like pane lands as Claude's queued message, exactly once.
func TestGuardedSendQueuesIntoBusyClaudeLikePane(t *testing.T) {
	sess, transcript := claudeLikeFake(t, "busy")
	msg := fmt.Sprintf("GUARDED_BUSY_%d please inspect this mid-turn message", time.Now().UnixNano())
	res, err := executeSend(sess, "claude", msg, true, sendTuning(true, true))
	if err != nil || res.delivery == deliveryComposerBlocked || res.delivery == deliveryMenuOpen || res.delivery == deliveryTypedNotSubmitted {
		t.Fatalf("guarded send to a busy empty composer: delivery=%q err=%v", res.delivery, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for fakeQueued(t, transcript, msg) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := fakeQueued(t, transcript, msg); got != 1 {
		t.Fatalf("queued %d time(s), want exactly 1; delivery=%q", got, res.delivery)
	}
}

// An operator draft in a busy Claude-like composer refuses the guarded send
// before typing and stays as it was.
func TestGuardedSendLeavesBusyOperatorDraftAlone(t *testing.T) {
	sess, transcript := claudeLikeFake(t, "busy")
	const draft = "operator note in progress"
	if err := sess.SendKeysChunked(draft); err != nil {
		t.Fatalf("type draft: %v", err)
	}
	waitForPane(t, sess, func(p string) bool { return strings.Contains(p, "❯ "+draft) })
	const msg = "GUARDED_MUST_NOT_APPEAR"
	res, err := executeSend(sess, "claude", msg, true, sendTuning(true, true))
	if err == nil || res.delivery != deliveryComposerBlocked || !strings.Contains(err.Error(), "no keys typed") {
		t.Fatalf("delivery=%q err=%v, want composer_blocked with no keys typed", res.delivery, err)
	}
	pane, _ := sess.CapturePaneFresh()
	if plain := tmux.StripANSI(pane); strings.Contains(plain, msg) || !strings.Contains(plain, "❯ "+draft) {
		t.Fatalf("draft changed or message typed:\n%s", plain)
	}
	if strings.Contains(readGolden(t, transcript), msg) {
		t.Fatal("the refused message reached the transcript")
	}
}
