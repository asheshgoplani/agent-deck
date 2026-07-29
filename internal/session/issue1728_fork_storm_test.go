package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #1728: with ~60 sessions the TUI sustained 150-350% CPU forking tmux
// subprocesses continuously. The dominant per-cycle fork source that scales
// with both session count AND sweep rate is GetEnvironment("CLAUDE_SESSION_ID"):
// its 30s cache only stores positive results, so every Claude-compatible
// session whose tmux env lacks the variable forks `tmux show-environment` on
// every metadata sync (down to a 500ms cadence while the session ID is
// unbound — exactly the un-cached case).
//
// This test measures real tmux forks across status sweeps via a PATH shim
// that logs every invocation before exec'ing the real tmux. It fails when
// show-environment forks scale with the number of sweeps instead of being
// absorbed by the (negative) env cache after the first sweep.
//
// The full-size profile (60 sessions, the #1728 shape) is opt-in:
//
//	AGENTDECK_FORKSTORM_N=60 go test ./internal/session -run TestIssue1728 -v
func TestIssue1728StatusSweepForkStorm(t *testing.T) {
	if testing.Short() {
		t.Skip("fork-storm harness spawns a real tmux server; skipped in -short")
	}
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}

	n := 8
	if env := os.Getenv("AGENTDECK_FORKSTORM_N"); env != "" {
		if _, err := fmt.Sscanf(env, "%d", &n); err != nil || n < 1 {
			t.Fatalf("bad AGENTDECK_FORKSTORM_N=%q", env)
		}
	}
	const sweeps = 4
	const sweepGap = 600 * time.Millisecond // > the 500ms unbound-ID metaSync cadence

	// PATH shim: every tmux fork appends its argv to a log, then execs the
	// real binary. exec.Command("tmux", ...) resolves via PATH at call time,
	// so the count is exact — no sampling.
	shimDir := t.TempDir()
	forkLog := filepath.Join(shimDir, "forks.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + forkLog + "\"\nexec \"" + realTmux + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(shim), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// N live sessions with continuously-changing pane content so the busy
	// check (CapturePane) stays on its hot path, created via the real tmux
	// binary directly (bypassing the shim) so setup doesn't pollute counts.
	instances := make([]*Instance, 0, n)
	for i := 0; i < n; i++ {
		inst := NewInstance(fmt.Sprintf("forkstorm-%02d", i), t.TempDir())
		inst.Tool = "claude"
		inst.Status = StatusRunning
		inst.lastStartTime = time.Now().Add(-time.Minute) // past the tmux-init grace window
		name := inst.tmuxSession.Name
		// Storage-loaded shape (the ~60-session #1728 fleet is all reconnects):
		// no startup grace window, and a declared claude command so DetectTool
		// doesn't re-classify the sh pane as "shell" and skip the Claude
		// metadata sync whose show-environment probe is under test.
		inst.tmuxSession = tmux.ReconnectSessionLazy(name, inst.Title, inst.ProjectPath, "claude", "active")
		cmd := exec.Command(realTmux, "new-session", "-d", "-s", name,
			"sh", "-c", "while :; do date; sleep 0.2; done")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("new-session %s: %v: %s", name, err, out)
		}
		t.Cleanup(func() {
			_ = exec.Command(realTmux, "kill-session", "-t", name).Run()
		})
		instances = append(instances, inst)
	}

	// Model the TUI's backgroundStatusUpdate: O(1) batched cache refreshes,
	// then per-instance UpdateStatus through a bounded worker pool.
	runSweep := func() {
		tmux.RefreshExistingSessions()
		tmux.RefreshPaneInfoCache()
		sem := make(chan struct{}, 10)
		var wg sync.WaitGroup
		for _, inst := range instances {
			wg.Add(1)
			sem <- struct{}{}
			go func(inst *Instance) {
				defer wg.Done()
				defer func() { <-sem }()
				_ = inst.UpdateStatus()
			}(inst)
		}
		wg.Wait()
	}

	// Reset the fork log so only sweep-driven forks are counted.
	if err := os.WriteFile(forkLog, nil, 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	start := time.Now()
	for s := 0; s < sweeps; s++ {
		// The storm state: sessions are running/waiting, so the metadata sync
		// (and its show-environment probe) is live on every pass.
		for _, inst := range instances {
			inst.mu.Lock()
			if inst.Status != StatusRunning && inst.Status != StatusWaiting {
				inst.Status = StatusRunning
			}
			inst.mu.Unlock()
		}
		runSweep()
		time.Sleep(sweepGap)
	}
	elapsed := time.Since(start)

	data, err := os.ReadFile(forkLog) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		total++
		sub := "other"
		for _, cand := range []string{
			"show-environment", "capture-pane", "has-session", "list-panes",
			"list-windows", "list-sessions", "list-clients", "display-message",
		} {
			if strings.Contains(line, cand) {
				sub = cand
				break
			}
		}
		counts[sub]++
	}
	t.Logf("issue #1728 fork profile: n=%d sweeps=%d elapsed=%s total_forks=%d (%.1f forks/sec)",
		n, sweeps, elapsed.Round(time.Millisecond), total, float64(total)/elapsed.Seconds())
	for sub, c := range counts {
		t.Logf("  %-18s %d", sub, c)
	}

	// Regression gate: show-environment must not scale with sweep count.
	// Un-fixed, every sweep forks one probe per session (n*sweeps, minus the
	// first sweep's near-misses); fixed, the first sweep's negative result is
	// cached and later sweeps are absorbed. n + n/2 leaves slack for cache
	// expiry racing the final sweep while still failing hard at n*sweeps.
	limit := n + n/2
	if got := counts["show-environment"]; got > limit {
		t.Errorf("show-environment forked %d times across %d sweeps of %d sessions (limit %d): negative env-cache results are not being cached (issue #1728)",
			got, sweeps, n, limit)
	}
}
