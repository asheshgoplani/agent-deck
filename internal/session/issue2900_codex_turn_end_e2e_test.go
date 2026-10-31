package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// fakeCodexScript poses as Codex in a real tmux pane (the package's private
// test socket). Each turn waits for its go file, draws Codex's working line
// (its timer ticking) for three seconds, then the composer. The turn's notify hook, written the way
// codex-notify writes it (status file renamed into place), lands per mode:
// early (before the composer), delayed (three seconds after it) or missing
// (never: a dropped notify).
const fakeCodexScript = `#!/bin/sh
dir="$1"; hooks="$2"; inst="$3"; thread="$4"
notify() {
  gen="$thread:turn-$1"
  printf '{"status":"waiting","session_id":"%s","event":"agent-turn-complete","ts":%s,"codex_started_generation":"%s","codex_completed_generation":"%s","codex_started_session_id":"%s","codex_completed_session_id":"%s"}' \
    "$thread" "$(date +%s)" "$gen" "$gen" "$thread" "$thread" > "$hooks/.$inst.tmp"
  mv "$hooks/.$inst.tmp" "$hooks/$inst.json"
}
clear; printf '\n› Ask Codex to do anything\n'
n=1
while [ $n -le 3 ]; do
  while [ ! -f "$dir/go-$n" ]; do sleep 0.2; done
  mode=$(cat "$dir/go-$n")
  i=0
  while [ $i -lt 10 ]; do
    clear; printf '• Working on turn %s\n\n◦ Working (%ss • esc to interrupt)\n' "$n" "$i"; sleep 0.3
    i=$((i+1))
  done
  [ "$mode" = early ] && notify $n
  clear; printf '• Done with turn %s.\n\n› Ask Codex to do anything\n\n  ? for shortcuts\n' "$n"
  if [ "$mode" = delayed ]; then sleep 3; notify $n; fi
  n=$((n+1))
done
sleep 600
`

// TestIssue2900_E2E_FakeCodexTurnEndsEachYieldOneRecord runs a fake Codex in
// a real tmux pane through three turns whose notify lands early, late, or
// never, derives the child's status from the pane with the Codex detector,
// feeds each pass through the daemon's emission paths with the real pane
// probe, and asserts one parent record per turn.
func TestIssue2900_E2E_FakeCodexTurnEndsEachYieldOneRecord(t *testing.T) {
	skipIfNoTmuxBinary(t)
	if testing.Short() {
		t.Skip("real tmux e2e")
	}
	f := newCodexTurnFixture(t, "codex")
	f.d.codexComposerIdle = nil              // the real pane probe
	f.d.codexFallbackAfter = 2 * time.Second // keeps the test short

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-codex.sh")
	if err := os.WriteFile(script, []byte(fakeCodexScript), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := f.child
	inst.tmuxSession = tmux.NewSession("fake-codex-2900", dir)
	inst.tmuxSession.SetDetectPatterns("codex", nil)
	cmd := fmt.Sprintf("sh %s %s %s %s %s", script, dir, GetHooksDir(), inst.ID, inst.CodexSessionID)
	if err := inst.tmuxSession.Start(cmd); err != nil {
		t.Fatalf("start fake codex pane: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })

	last := ""
	pass := func() string {
		// The pane decides the status: Codex's busy phrase, or the composer
		// through the Codex prompt detector. UpdateStatus adds activity
		// heuristics that misread a fake pane under a loaded test run.
		status := last
		if raw, err := inst.tmuxSession.CapturePaneFresh(); err == nil {
			content := tmux.StripANSI(raw)
			switch {
			case strings.Contains(content, "esc to interrupt"): // Codex's busy phrase
				status = string(StatusRunning)
			case codexContentAtComposer(inst.Tool, content):
				status = string(StatusWaiting)
			}
		}
		f.pass(t, status, time.Now())
		if status != last {
			pane, _ := inst.tmuxSession.CapturePane()
			t.Logf("status %s -> %s (records %d) pane=%q", last, status, f.records(t), pane)
			last = status
		}
		return status
	}
	// Settle at the composer; the first sight of a parked child may record.
	waitPasses := func(d time.Duration) {
		for end := time.Now().Add(d); time.Now().Before(end); {
			pass()
			time.Sleep(500 * time.Millisecond)
		}
	}
	waitPasses(3 * time.Second)
	base := f.records(t)

	for i, mode := range []string{"early", "delayed", "missing"} {
		n := i + 1
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("go-%d", n)), []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
		sawRunning := false
		for end := time.Now().Add(8 * time.Second); time.Now().Before(end) && !sawRunning; {
			sawRunning = pass() == string(StatusRunning)
			time.Sleep(300 * time.Millisecond)
		}
		if !sawRunning {
			t.Fatalf("turn %d (%s): the fake codex was never seen running", n, mode)
		}
		// Past the delayed notify and the fallback delay.
		waitPasses(f.d.codexFallbackAfter + 5*time.Second)
		if got := f.records(t) - base; got != n {
			t.Fatalf("turn %d (%s notify): %d records since start, want %d", n, mode, got, n)
		}
	}
}
