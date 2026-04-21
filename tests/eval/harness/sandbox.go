// Package harness provides a behavioral evaluator sandbox for agent-deck.
//
// The harness builds the agent-deck binary once per test package, then spawns
// it under a scratch HOME with an isolated tmux socket and stub shims on PATH
// (gh, etc.). Tests drive the binary via PTY or CLI args and assert on what a
// human would see — stdout timing, tmux display state, recorded shim calls.
//
// See docs/rfc/EVALUATOR_HARNESS.md for the motivating bugs and design.
package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Sandbox is a per-test isolated environment: scratch HOME, dedicated tmux
// socket, stub shims on PATH. All state is torn down by t.Cleanup.
type Sandbox struct {
	t *testing.T

	// Home is a scratch directory used for HOME, XDG_CONFIG_HOME, and
	// agent-deck's state dir. Cleaned up on test exit.
	Home string

	// BinPath is the full path to the agent-deck binary built for this
	// package (shared across tests in the package via buildOnce).
	BinPath string

	// ShimDir is on PATH ahead of /usr/bin, housing executable shims
	// (gh, etc.). Tests mutate its contents before spawning.
	ShimDir string

	// GhShim records calls to the gh stub and scripts its exit behavior.
	GhShim *GhShim

	// tmuxSock is a per-sandbox tmux socket path. Lazy-initialized by
	// TmuxSocket(); the tmux server is killed on cleanup.
	tmuxSock string
}

// NewSandbox returns a Sandbox bound to t. All resources are released via
// t.Cleanup. Safe to call from a parallel test.
func NewSandbox(t *testing.T) *Sandbox {
	t.Helper()
	bin, err := buildAgentDeck()
	if err != nil {
		t.Fatalf("build agent-deck: %v", err)
	}

	home := t.TempDir()
	shim := t.TempDir()

	sb := &Sandbox{
		t:       t,
		Home:    home,
		BinPath: bin,
		ShimDir: shim,
	}
	sb.GhShim = newGhShim(t, shim)

	t.Cleanup(func() { sb.teardown() })
	return sb
}

// Env returns the base environment vector used when spawning the binary.
// Tests can extend this slice before passing it to exec.Cmd.
func (s *Sandbox) Env() []string {
	path := s.ShimDir + string(os.PathListSeparator) + os.Getenv("PATH")
	env := []string{
		"HOME=" + s.Home,
		"XDG_CONFIG_HOME=" + filepath.Join(s.Home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(s.Home, ".local", "state"),
		"PATH=" + path,
		// TERM=dumb prevents termenv from probing the PTY for OSC 11
		// background and CSI 6n cursor position — those probes have no
		// responder in our harness and leak raw bytes into captured
		// output. AGENTDECK_COLOR=none also forces the Ascii lipgloss
		// profile. For TUI tests that need real terminal capabilities,
		// swap to xterm-256color via SpawnWithEnv. See issue #37.
		"TERM=dumb",
		"AGENTDECK_COLOR=none",
		"NO_COLOR=1",
		// Let the binary find tmux; we do NOT shim tmux — we use the real
		// binary against a per-sandbox socket. See TmuxSocket().
		"AGENT_DECK_TMUX_SOCKET=" + s.TmuxSocket(),
	}
	// Preserve a few passthrough vars that the binary and its children need.
	for _, k := range []string{"LANG", "LC_ALL", "SHELL", "GOCACHE", "GOMODCACHE"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// TmuxSocket returns the path to this sandbox's tmux socket. Lazy-created.
func (s *Sandbox) TmuxSocket() string {
	if s.tmuxSock == "" {
		s.tmuxSock = filepath.Join(s.t.TempDir(), "tmux.sock")
	}
	return s.tmuxSock
}

// Tmux runs `tmux -S <socket> <args...>` and returns trimmed stdout. The
// first failure fails the test.
func (s *Sandbox) Tmux(args ...string) string {
	s.t.Helper()
	full := append([]string{"-S", s.TmuxSocket()}, args...)
	out, err := exec.Command("tmux", full...).CombinedOutput()
	if err != nil {
		s.t.Fatalf("tmux %v: %v\n%s", args, err, string(out))
	}
	return string(out)
}

// TmuxTry runs `tmux -S <socket> <args...>` and returns stdout+err without
// failing the test. Useful when the caller wants to tolerate a non-zero exit
// (e.g. kill-server when no server is running).
func (s *Sandbox) TmuxTry(args ...string) (string, error) {
	full := append([]string{"-S", s.TmuxSocket()}, args...)
	out, err := exec.Command("tmux", full...).CombinedOutput()
	return string(out), err
}

func (s *Sandbox) teardown() {
	if s.tmuxSock != "" {
		// Best-effort. A server may not be running if the test never
		// started one.
		_, _ = s.TmuxTry("kill-server")
	}
}

// ---- binary build (shared across tests in a package) ---------------------

var (
	buildOnce sync.Once
	buildBin  string
	buildErr  error
)

// buildAgentDeck compiles cmd/agent-deck once per test binary and returns
// the path. Reused across tests in the same package.
func buildAgentDeck() (string, error) {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agent-deck-eval-bin-")
		if err != nil {
			buildErr = fmt.Errorf("tempdir: %w", err)
			return
		}
		out := filepath.Join(dir, "agent-deck")

		root, err := repoRoot()
		if err != nil {
			buildErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", out, "./cmd/agent-deck")
		cmd.Dir = root
		cmd.Env = os.Environ()
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, string(b))
			return
		}
		buildBin = out
	})
	return buildBin, buildErr
}

// repoRoot walks up from the test's working directory until it finds go.mod.
// Tests run from the package directory, so we climb until we hit the module
// root.
func repoRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no go.mod found walking up from cwd")
		}
		d = parent
	}
}
