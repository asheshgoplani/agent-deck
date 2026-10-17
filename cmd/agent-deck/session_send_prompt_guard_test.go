package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

const guardedMenu = "Which approach should I take?\n❯ 1. Yes\n  2. No\nEnter to select · Esc to cancel"

func TestSendTuningKeepsGuardInNoWaitMode(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		if !sendTuning(noWait, true).requireInputPrompt || sendTuning(noWait, false).requireInputPrompt {
			t.Fatalf("noWait=%v: guard tuning lost", noWait)
		}
	}
}

func TestPromptGuardRefusesMenuBeforeBody(t *testing.T) {
	pane := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{guardedMenu}}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 1, checkDelay: 0, verifyDelivery: true})
	tun.requireInputPrompt = true
	res, err := executeSend(pane, "claude", "first line\nsecond line", false, tun)
	if err == nil || res.delivery != deliveryMenuOpen || !strings.Contains(err.Error(), "no keys typed") {
		t.Fatalf("delivery=%q err=%v", res.delivery, err)
	}
	if pane.sendKeysCalls != 0 || pane.sendEnterCalls != 0 || pane.sendChunkedCalls != 0 {
		t.Fatalf("menu received keys: body=%d enter=%d chunks=%d", pane.sendKeysCalls, pane.sendEnterCalls, pane.sendChunkedCalls)
	}
}

func TestPromptGuardStopsBetweenBodyAndEnter(t *testing.T) {
	pane := &mockSendRetryTarget{panes: []string{claudeComposer(""), guardedMenu}}
	guard := &promptGuardTarget{sendRetryTarget: pane, tool: "claude"}
	err := guard.SendKeysAndEnterChecked("first line\nsecond line", nil, tmux.PostPasteCheck(func(string, error) (bool, error) { return true, nil }))
	if err == nil || guard.typedBatches != 1 || pane.sendKeysCalls != 1 || pane.sendEnterCalls != 0 {
		t.Fatalf("err=%v batches=%d body=%d enter=%d", err, guard.typedBatches, pane.sendKeysCalls, pane.sendEnterCalls)
	}
	if delivery, reason := guard.refusal(); delivery != deliveryTypedNotSubmitted || !strings.Contains(reason.Error(), "1 keystroke batch") {
		t.Fatalf("delivery=%q reason=%v", delivery, reason)
	}
}

func TestPromptGuardDoesNotCallAbsentPromptAMenu(t *testing.T) {
	guard := &promptGuardTarget{tool: "claude"}
	if err := guard.check("Claude is starting", nil); err == nil {
		t.Fatal("missing prompt should refuse before typing")
	}
	if delivery, _ := guard.refusal(); delivery != deliveryComposerBlocked {
		t.Fatalf("delivery=%q, want composer_blocked", delivery)
	}
}

func TestPromptGuardOpenComposerAllowsBodyAndEnter(t *testing.T) {
	pane := &mockSendRetryTarget{panes: []string{claudeComposer("")}}
	guard := &promptGuardTarget{sendRetryTarget: pane, tool: "claude"}
	if err := guard.SendKeysAndEnter("ordinary message"); err != nil {
		t.Fatal(err)
	}
	if guard.typedBatches != 2 || pane.sendKeysCalls != 1 || guard.refused {
		t.Fatalf("batches=%d body=%d refused=%v", guard.typedBatches, pane.sendKeysCalls, guard.refused)
	}
}

func TestGuardedSendKeepsOrdinaryDeliveryEvidence(t *testing.T) {
	tun := testGuardTuning(sendRetryOptions{maxRetries: 2, checkDelay: 0, verifyDelivery: true})
	tun.guardHold = 0
	tun.requireInputPrompt = true
	pane := &mockSendRetryTarget{statuses: []string{"waiting", "active"}, panes: []string{claudeComposer("")}}
	res, err := executeSend(pane, "claude", "ordinary message with token", false, tun)
	if err != nil || res.delivery != deliverySubmitted || pane.sendKeysCalls != 1 {
		t.Fatalf("delivery=%q err=%v body=%d", res.delivery, err, pane.sendKeysCalls)
	}
}

func TestGuardedMenuQueueRetryOnlyBeforeTyping(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   childOutcome
	}{
		{"input prompt unavailable; no keys typed", childNotSent},
		{"input prompt unavailable; 1 keystroke batch(es) typed before refusal", childUnknown},
	} {
		outcome, _ := classifyChild(map[string]interface{}{"success": false, "delivery": deliveryMenuOpen, "error": tc.reason}, 1)
		if outcome != tc.want {
			t.Fatalf("%q: outcome=%v want=%v", tc.reason, outcome, tc.want)
		}
	}
}

func TestRemoteGuardedSendFlagRecognition(t *testing.T) {
	if !remoteGuardedSend([]string{"session", "send", "id", "message", "--require-input-prompt"}) || remoteGuardedSend([]string{"session", "send", "id", "message"}) {
		t.Fatal("remote flag recognition")
	}
	args, _, _, err := remoteMessageInput([]string{"session", "send", "id", "--require-input-prompt", "--message-file", "-"})
	if err != nil || !strings.Contains(strings.Join(args, " "), "--require-input-prompt") {
		t.Fatalf("forwarded=%q err=%v", args, err)
	}
}

func TestOldRemoteGuardedSendIsUnsupported(t *testing.T) {
	if remoteSendHelpSupportsGuard("Usage: session send [--json] [--wait]") {
		t.Fatal("old remote was treated as guarded")
	}
	if !remoteSendHelpSupportsGuard("  -require-input-prompt  Refuse unless prompt is visible") {
		t.Fatal("new remote flag was not recognized")
	}
}

type oldGuardRemote struct{ calls int }

func (r *oldGuardRemote) RunIO(_ context.Context, _ io.Reader, stdout, _ io.Writer, args ...string) error {
	r.calls++
	if strings.Join(args, " ") != "session send --help" {
		return nil
	}
	_, _ = io.WriteString(stdout, "Usage: session send [--json] [--wait]")
	return nil
}

func TestOldRemoteGuardedSendStopsBeforeDelivery(t *testing.T) {
	remote := &oldGuardRemote{}
	err := requireRemoteGuardSupport(context.Background(), remote, "old-remote")
	if err == nil || !strings.Contains(err.Error(), "unsupported on this remote") || remote.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, remote.calls)
	}
}

func TestGuardedQueueUsesGuardedChildAndRetriesBeforeTyping(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	rec := &sendqueue.Record{SendID: sendqueue.NewID(now), State: sendqueue.StateQueued, SessionID: "guarded-target", Tool: "claude", Message: "answer",
		RequireInputPrompt: true, CreatedAt: now.UTC().Format(time.RFC3339Nano), Deadline: now.Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	if err := sendqueue.Save(dir, rec); err != nil {
		t.Fatal(err)
	}
	previous := sendChildGuarded
	t.Cleanup(func() { sendChildGuarded = previous })
	called := false
	sendChildGuarded = func(_, _, _, resultPath string) (int, func() int, error) {
		called = true
		if err := os.WriteFile(resultPath, []byte(`{"success":false,"delivery":"menu_open","error":"input prompt unavailable; no keys typed"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return 1, func() int { return 1 }, nil
	}
	set := func(fn func(*sendqueue.Record)) error {
		updated, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
		if err == nil {
			*rec = *updated
		}
		return err
	}
	if !typeQueued("", dir, rec, "waiting", "", 0, set) || !called || rec.State != sendqueue.StateQueued {
		t.Fatalf("called=%v state=%q", called, rec.State)
	}
}

// A real tmux pane showing a picker receives nothing from a guarded send:
// the pane runs cat after drawing the picker, so any typed byte would be
// echoed back into the capture.
func TestGuardedSendTypesNothingIntoRealTmuxPicker(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sess := tmux.NewSession("guarded-real-picker", "/tmp")
	script := `sh -c 'printf "Which approach should I take?\n❯ 1. Yes\n  2. No\nEnter to select · Esc to cancel\n"; exec cat'`
	if err := sess.Start(script); err != nil {
		t.Fatalf("start pane: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		pane, _ := sess.CapturePaneFresh()
		if strings.Contains(pane, "Enter to select") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("picker never rendered: %q", pane)
		}
		time.Sleep(50 * time.Millisecond)
	}
	const msg = "GUARDED_MUST_NOT_APPEAR"
	tun := testGuardTuning(sendRetryOptions{maxRetries: 1, checkDelay: 0, verifyDelivery: true})
	tun.requireInputPrompt = true
	res, err := executeSend(sess, "claude", msg, false, tun)
	if err == nil || res.delivery != deliveryMenuOpen || !strings.Contains(err.Error(), "no keys typed") {
		t.Fatalf("delivery=%q err=%v", res.delivery, err)
	}
	time.Sleep(300 * time.Millisecond)
	pane, _ := sess.CapturePaneFresh()
	if strings.Contains(pane, msg) {
		t.Fatalf("guarded send typed into the picker:\n%s", pane)
	}
}
