package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// Cold-start regression tests for the agent-deck CLI.
//
// Both tests build the binary once via the eval harness's buildOnce machinery
// and exec it with `--help` / `--version`. The timed window covers everything
// from process spawn through the init() block (main.go:50-89), the tmux
// SetDefaultSocketName / WarnIfVulnerableTmux calls (main.go:218,223), and
// the subcommand-routing path that prints help/version and exits.
//
// These are COLD tests: they cross a process boundary, so the budget
// formula is base × 5 (with PERF_BUDGET_MULTIPLIER scaling and a 1ms floor)
// and the measurement is an n=11 trimmed mean. See internal/testutil/perfbudget.go
// for the full convention.
//
// On Linux (CI), WarnIfVulnerableTmux is a no-op and the binary spawns no
// child processes for --help/--version. Tests are safe under -race and
// require no real tmux server.

// Base local medians observed under -race at PERF_BUDGET_MULTIPLIER=1.0
// (Linux container, Intel Xeon @ 2.10GHz). ColdBudget multiplies by 5
// and applies the 1ms floor and the env multiplier.
const (
	coldStartHelpBase    = 8 * time.Millisecond // → ColdBudget = 40ms locally, 80ms in CI
	coldStartVersionBase = 8 * time.Millisecond // → ColdBudget = 40ms locally, 80ms in CI
)

// TestPerf_ColdStart_Help measures `agent-deck --help` end-to-end walltime.
// Catches regressions in init() at cmd/agent-deck/main.go:50-89 and the
// pre-dispatch tmux probes at :218,:223.
func TestPerf_ColdStart_Help(t *testing.T) {
	testutil.SkipIfShort(t)
	budget := testutil.ColdBudget(t, coldStartHelpBase)
	sb := harness.NewSandbox(t)
	env := sb.Env()

	got := testutil.TrimmedMean(func() {
		runColdStart(t, sb.BinPath, env, "--help")
	})

	if got > budget {
		t.Fatalf("agent-deck --help cold start trimmed mean = %v, budget = %v (regression in cmd/agent-deck/main.go init or tmux probes)", got, budget)
	}
	t.Logf("agent-deck --help trimmed mean = %v (budget = %v)", got, budget)
}

// TestPerf_ColdStart_Version measures `agent-deck --version`. Independent
// signal from --help: skips printHelp's formatting work but exercises the
// same init path.
func TestPerf_ColdStart_Version(t *testing.T) {
	testutil.SkipIfShort(t)
	budget := testutil.ColdBudget(t, coldStartVersionBase)
	sb := harness.NewSandbox(t)
	env := sb.Env()

	got := testutil.TrimmedMean(func() {
		runColdStart(t, sb.BinPath, env, "--version")
	})

	if got > budget {
		t.Fatalf("agent-deck --version cold start trimmed mean = %v, budget = %v", got, budget)
	}
	t.Logf("agent-deck --version trimmed mean = %v (budget = %v)", got, budget)
}

func runColdStart(t *testing.T, bin string, env []string, arg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, arg)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", bin, arg, err, string(out))
	}
	// Sanity: --version must mention "Agent Deck" and --help must mention
	// a top-level subcommand. Cheap correctness check that catches a
	// regression that swallows output (the bug class the perf budget
	// alone wouldn't notice).
	switch arg {
	case "--version":
		if !strings.Contains(string(out), "Agent Deck") {
			t.Fatalf("--version output missing 'Agent Deck' marker:\n%s", string(out))
		}
	case "--help":
		if len(strings.TrimSpace(string(out))) == 0 {
			t.Fatalf("--help produced empty output")
		}
	}
}

// BenchmarkColdStart_Help — Track A advisory bench, runs without -race via
// `make bench`. Captures ns/op for trending.
//
// Doesn't use the harness sandbox because harness.NewSandbox takes a
// *testing.T; a fresh build + scratch HOME is cheap to do directly.
func BenchmarkColdStart_Help(b *testing.B) {
	bin := buildBinaryForBench(b)
	home := b.TempDir()
	env := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"AGENTDECK_COLOR=none",
		"AGENTDECK_SUPPRESS_TMUX_WARNING=1",
	)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, bin, "--help")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			cancel()
			b.Fatalf("agent-deck --help failed: %v\n%s", err, string(out))
		}
		cancel()
	}
}

func buildBinaryForBench(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	bin := filepath.Join(dir, "agent-deck")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/agent-deck")
	cmd.Dir = repoRootForBench(b)
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("go build: %v\n%s", err, string(out))
	}
	return bin
}

func repoRootForBench(b *testing.B) string {
	b.Helper()
	d, err := os.Getwd()
	if err != nil {
		b.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			b.Fatalf("no go.mod found walking up from cwd")
		}
		d = parent
	}
}
