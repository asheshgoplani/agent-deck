package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestMain ensures all cmd tests use the _test profile to prevent
// accidental modification of production data.
// CRITICAL: This was missing and caused test data to overwrite production sessions!
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body. It exists so the cleanup defers
// below actually run: TestMain calls os.Exit, which does NOT run deferred
// functions, so registering them here and returning the exit code is the only
// way to guarantee the isolated TMUX_TMPDIR and HOME temp dirs are removed.
// Skipping this leaked per-run temp dirs on every run — the 2026-06-07
// pty-exhaustion incident class.
func runTestMain(m *testing.M) int {
	// Helper subprocesses (e.g. the Task6 XDG help-path test) are spawned by a
	// parent test that has ALREADY exported a safe, sandboxed HOME+XDG and set
	// the specific XDG_*_HOME values the subprocess must observe. Re-running
	// IsolateHome()/isolatePackageHome() here would clobber those inherited
	// values with a fresh ad-home-* temp dir, breaking the test (and silently
	// resolving to the wrong sandbox). The inherited env is already off the
	// real home, so data-safety is preserved by NOT re-isolating.
	isHelperProcess := os.Getenv("AGENT_DECK_TASK6_HELPER_PROCESS") != ""

	widenTestParallelism()

	if !isHelperProcess {
		// Pin the Go build and module caches before HOME moves: a test that
		// runs `go build` itself (eval harness sandbox, goldens) would
		// otherwise find an empty $HOME/.cache/go-build under the isolated
		// HOME and recompile the whole module, about a minute per CI shard.
		pinGoCaches()
		// Build shared CLI fixture before HOME/XDG isolation so Go caches never
		// become read-only debris inside a per-test directory.
		if err := ensureChannelsCLIBinary(); err != nil {
			fmt.Fprintf(os.Stderr, "prepare CLI fixture: %v\n", err)
			return 1
		}
		// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir,
		// never the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
		// Must run before anything resolves a path. See internal/testutil/homeenv.go.
		cleanupHome := testutil.IsolateHome()
		defer cleanupHome()
		cleanupServiceManagers, err := stubServiceManagers()
		if err != nil {
			fmt.Fprintf(os.Stderr, "stub service managers: %v\n", err)
			return 1
		}
		defer cleanupServiceManagers()
	}
	os.Unsetenv("AGENTDECK_INSTANCE_ID")
	os.Unsetenv("AGENT_DECK_SESSION_ID")

	// Git hooks export GIT_DIR/GIT_WORK_TREE; clear them so test subprocess git
	// commands operate on their temp repos instead of the real repository.
	testutil.UnsetGitRepoEnv()
	if !isHelperProcess {
		isolatePackageHome("agent-deck-cmd-tests-home-*")
	}

	// Isolate the tmux socket. Without this, cmd-level tests spawn tmux
	// sessions on the user's default socket and destabilize live agent-deck
	// sessions. 2026-04-17 incident: go test ./... killed every session in
	// the personal profile when tests ran on a live host.
	// See internal/testutil/tmuxenv.go for the full postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	// Force _test profile for all tests in this package
	os.Setenv("AGENTDECK_PROFILE", "_test")

	// Run tests
	code := m.Run()
	// The subprocess tests share one agent-deck build in an
	// agent-deck-eval-bin-* temp dir (~15 MB) that must outlive every test,
	// so t.Cleanup cannot own it. Remove it here or it leaks on every run.
	harness.RemoveBuildArtifacts()
	if err := cleanupGoldensBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup goldens binary: %v\n", err)
		code = 1
	}

	// Cleanup: Kill any orphaned test sessions after tests complete
	// This prevents RAM waste from lingering test sessions
	// See CLAUDE.md: "2026-01-20 Incident: 20+ Test-Skip-Regen sessions orphaned, wasting ~3GB RAM"
	cleanupTestSessions()

	return code
}

func isolatePackageHome(pattern string) {
	home, err := os.MkdirTemp("", pattern)
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	// Clear (do NOT pin) XDG base dirs so they track HOME and don't accumulate
	// stale config/data across tests in this shared package home. See
	// testutil.IsolateHome's doc comment (2026-06-07 ~96-test isolation
	// regression from #1294's "prefer XDG if it exists" path resolution).
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("XDG_DATA_HOME")
	os.Unsetenv("XDG_CACHE_HOME")
	os.Unsetenv("XDG_STATE_HOME")
}

// cleanupTestSessions kills any tmux sessions created during testing.
// IMPORTANT: Only match specific known test artifacts, NOT broad patterns.
// Broad patterns like HasPrefix("agentdeck_test") or Contains("test_") kill
// real user sessions with "test" in their title. Each test already has
// defer Kill() which handles cleanup reliably (runs on panic, Fatal, etc).
func cleanupTestSessions() {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return
	}

	sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, sess := range sessions {
		if strings.Contains(sess, "Test-Skip-Regen") {
			_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
		}
	}
}

// widenTestParallelism gives the package's parallel tests two slots per CPU
// when the run did not pass -parallel itself (#2797). Every test here that
// calls t.Parallel (parallelSubprocessTest) spends its time waiting on child
// processes, daemons and timers rather than on CPU, so with go test's default
// of one slot per CPU the parallel phase queued behind those waits: about 90 s
// of a 320 s package run with the CPUs mostly idle. An explicit -parallel
// always wins.
func widenTestParallelism() {
	if !flag.Parsed() {
		flag.Parse()
	}
	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "test.parallel" {
			explicit = true
		}
	})
	if !explicit {
		_ = flag.Set("test.parallel", strconv.Itoa(2*runtime.GOMAXPROCS(0)))
	}
}

// pinGoCaches exports GOCACHE and GOMODCACHE with the values the go command
// resolves for the real HOME, unless the caller already set them.
func pinGoCaches() {
	names := []string{"GOCACHE", "GOMODCACHE"}
	out, err := exec.Command("go", append([]string{"env"}, names...)...).Output()
	if err != nil {
		return
	}
	values := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, name := range names {
		if i < len(values) && os.Getenv(name) == "" && strings.TrimSpace(values[i]) != "" {
			os.Setenv(name, strings.TrimSpace(values[i]))
		}
	}
}
