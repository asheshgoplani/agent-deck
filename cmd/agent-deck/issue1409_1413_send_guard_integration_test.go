package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Integration coverage for issues #1409 (composer-draft collision) and #1413
// (typed-but-unsubmitted delivery) against a real tmux pane running a
// fake-claude composer script. Gated behind AGENT_DECK_INTEGRATION_TESTS like
// the existing TestSendWithRetry_DelayedInputHandler_Integration: bash is not
// a perfectly faithful model of Claude's Ink TUI, so these stay opt-in while
// the always-run mock tests in issue1409_1413_send_guard_test.go pin the
// state machines deterministically.
//
// TestMain isolates TMUX_TMPDIR, so sessions started here live on an
// isolated tmux server, never the user's default socket.

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("AGENT_DECK_INTEGRATION_TESTS") == "" {
		t.Skip("skipping tmux integration test (set AGENT_DECK_INTEGRATION_TESTS=1 to enable)")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

func integrationGuardTuning() sendExecTuning {
	tun := defaultSendTuning()
	tun.guardHold = 700 * time.Millisecond
	tun.guardPoll = 100 * time.Millisecond
	tun.guardClearWait = time.Second
	tun.retry = sendRetryOptions{
		maxRetries:     12,
		checkDelay:     150 * time.Millisecond,
		maxFullResends: -1,
		verifyDelivery: true,
	}
	return tun
}

// startFakeClaudePane starts an isolated tmux session running script and
// waits briefly for it to render.
func startFakeClaudePane(t *testing.T, name, script string) *tmux.Session {
	t.Helper()
	sess := tmux.NewSession(name, "/tmp")
	if err := sess.Start(script); err != nil {
		t.Fatalf("failed to start fake-claude pane: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })
	time.Sleep(700 * time.Millisecond)
	return sess
}

// fakeClaudeWithDraft renders a composer holding an operator draft. The
// draft stays until SIGINT (Ctrl+C clears it, like Claude's composer), after
// which the script accepts one line, echoes it as "GOT: <line>", and renders
// a fresh empty composer.
const fakeClaudeWithDraft = `bash -c '
	draft="instruct deploy ag"
	cleared=0
	trap "cleared=1" INT
	printf "❯ %s\n" "$draft"
	while [ "$cleared" -eq 0 ]; do sleep 0.05; done
	printf "\n❯ \n"
	IFS= read -r line
	echo "GOT: $line"
	printf "❯ \n"
	sleep 15
'`

func TestExecuteSend_OperatorDraftNotMerged_Integration(t *testing.T) {
	skipUnlessIntegration(t)
	sess := startFakeClaudePane(t, "send-preserve-draft", fakeClaudeWithDraft)
	const msg = "AUTOMATED_MUST_NOT_APPEAR"
	res, err := executeSend(sess, "claude", msg, false, integrationGuardTuning())
	if err == nil || res.delivery != deliveryComposerBlocked {
		t.Fatalf("expected unsent refusal: %+v %v", res, err)
	}
	content, err := sess.CapturePane()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "instruct deploy ag") || strings.Contains(content, msg) || strings.Contains(content, "GOT:") {
		t.Fatalf("draft changed or automated input leaked into pane: %q", content)
	}
}

// fakeClaudeSwallowsEnter renders an empty composer and echoes typed
// characters back onto the prompt line but never accepts Enter — the
// typed-but-unsubmitted state of issue #1413.
const fakeClaudeSwallowsEnter = `bash -c '
	printf "❯ "
	buf=""
	while IFS= read -r -n1 c; do [ -n "$c" ] && buf="$buf$c" && printf "\r❯ %s" "$buf"; done
'`

func TestExecuteSend_TypedNotSubmitted_Integration(t *testing.T) {
	skipUnlessIntegration(t)
	sess := startFakeClaudePane(t, "send-1413-stuck", fakeClaudeSwallowsEnter)

	const msg = "STUCK_1413_NEVER_SUBMITS"
	start := time.Now()
	res, err := executeSend(sess, "claude", msg, false, integrationGuardTuning())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("issue #1413: send against an Enter-swallowing pane must fail, got success (result %+v)", res)
	}
	if res.delivery != deliveryTypedNotSubmitted {
		t.Errorf("delivery: want %q, got %q (err: %v)", deliveryTypedNotSubmitted, res.delivery, err)
	}
	// Bounded: guard (≤ ~2s) + verification (12 × 150ms) + slack.
	if elapsed > 30*time.Second {
		t.Errorf("typed_not_submitted detection must be bounded, took %v", elapsed)
	}
}

func TestExecuteSend_NoWaitGuardsDraft_Integration(t *testing.T) {
	skipUnlessIntegration(t)
	sess := startFakeClaudePane(t, "send-preserve-draft", fakeClaudeWithDraft)
	const msg = "AUTOMATED_MUST_NOT_APPEAR"
	res, err := executeSend(sess, "claude", msg, true, integrationGuardTuning())
	if err == nil || res.delivery != deliveryComposerBlocked {
		t.Fatalf("expected unsent refusal: %+v %v", res, err)
	}
	content, err := sess.CapturePane()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "instruct deploy ag") || strings.Contains(content, msg) || strings.Contains(content, "GOT:") {
		t.Fatalf("draft changed or automated input leaked into pane: %q", content)
	}
}
