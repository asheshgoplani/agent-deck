package tmux

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Issue #1131 — end-to-end keystroke latency investigation.
//
// Prior #1102/#1112 perf tests measured only the time for SendKeys to RETURN
// (a stdin write into the persistent `tmux -C` pipe, ~4µs/key). That is the
// WRONG layer: SendKeys returns the instant the bytes hit the OS pipe buffer,
// long before tmux executes the command and the pane actually renders the
// rune. This test measures the layer that was never benchmarked: the wall
// time from "send a rune" to "rune is visible in the pane" via real tmux.
//
// It exists to localize the @ddorman-dn lag report: if THIS number is small
// (single-digit ms), the dominant cost is NOT the send-keys/tmux layer and
// the lag lives elsewhere (the UI preview-refresh cadence — see the UI-layer
// test in internal/ui).
func TestIssue1131_E2E_KeystrokeToPaneRenderLatency(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}
	defer sender.Close()

	// Measure several keystrokes; each uses a distinct marker so we poll for
	// THIS keystroke's echo and not a stale capture from a previous one.
	const samples = 20
	var total time.Duration
	var worst time.Duration
	for i := range samples {
		marker := fmt.Sprintf("Z%d_", i) // unique, won't appear before we send it
		if err := sender.SendKeys(marker); err != nil {
			t.Fatalf("SendKeys: %v", err)
		}
		latency := pollUntilPaneContains(t, socket, target, marker, 2*time.Second)
		total += latency
		if latency > worst {
			worst = latency
		}
	}
	avg := total / samples
	t.Logf("#1131 tmux-layer keystroke→pane-render latency: avg=%v worst=%v over %d samples",
		avg, worst, samples)

	// Regression fence: the tmux send+render layer must stay well under the
	// ~100ms threshold of human-perceptible lag. If this ever blows up, the
	// bottleneck genuinely IS the send path and the conclusion changes.
	if avg > 100*time.Millisecond {
		t.Errorf("tmux-layer echo latency avg=%v exceeds 100ms — the send path itself is now the bottleneck", avg)
	}
}

// pollUntilPaneContains tightly polls capture-pane until `want` appears,
// returning the elapsed wall time. Fails the test on timeout. The poll
// granularity (1ms) is far finer than any realistic tmux render delay so the
// measured latency reflects tmux, not the poll loop.
func pollUntilPaneContains(t *testing.T, socket, target, want string, timeout time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	deadline := start.Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-L", socket, "capture-pane", "-t", target, "-p").CombinedOutput()
		if err == nil && strings.Contains(string(out), want) {
			return time.Since(start)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pane never rendered %q within %v", want, timeout)
	return 0
}
