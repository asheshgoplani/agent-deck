package pathsafety

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestSandboxCanary resolves every state and tool-config path the product
// computes, under this package's normal test isolation, and fails if any of
// them lands in the HOME the test binary started with (captured before
// TestMain isolated it) or in the passwd home.
//
// TestPathsDoNotResolveUnderRealHome compares agent-deck paths with the passwd
// home only. That misses two shapes this canary catches: a sandbox where HOME
// is a throwaway dir that is not the passwd home (an un-isolated package then
// writes into that HOME unnoticed), and tool config dirs (~/.claude, ~/.codex,
// ...) that live outside agentpaths. A resolver that ignores the HOME or env
// override (a hardcoded home, #2001; a live CLAUDE_CONFIG_DIR) fails here.
//
// The decision is sandboxguard.Violation, not the guard calls inside the
// resolvers, so removing a resolver's guard does not silence the canary.
func TestSandboxCanary(t *testing.T) {
	// Stricter than the runtime guard: here the start HOME counts even when it
	// is a throwaway dir in the temp area (a sandbox runner's HOME=/tmp/home),
	// so a package that stops isolating fails in that sandbox as it would in CI.
	start := sandboxguard.StartHome()
	if start == "/" || start == filepath.Clean(os.TempDir()) || !filepath.IsAbs(start) {
		start = ""
	}
	if home, _ := os.UserHomeDir(); (start != "" && filepath.Clean(home) == start) || sandboxguard.Violation(home) != "" {
		t.Fatalf("HOME=%q is the start HOME or a real home %v: TestMain did not isolate it", home, sandboxguard.RealHomes())
	}
	hit := func(p string) string {
		if h := sandboxguard.Violation(p); h != "" {
			return h
		}
		if c := filepath.Clean(p); start != "" && (c == start || strings.HasPrefix(c, start+string(filepath.Separator))) {
			return start
		}
		return ""
	}

	resolvers := []struct {
		name string
		fn   func() (string, error)
	}{
		{"agent-deck dir", session.GetAgentDeckDir},
		{"config.json", session.GetConfigPath},
		{"profiles dir", session.GetProfilesDir},
		{"state.db", func() (string, error) { return session.GetDBPathForProfile(session.DefaultProfile) }},
		{"config.toml", session.GetUserConfigPath},
		{"worker-scratch root", session.WorkerScratchDirRoot},
		{"CLAUDE_CONFIG_DIR", str(session.GetClaudeConfigDir)},
		{"CODEX_HOME", str(session.GetCodexConfigDir)},
		{"Gemini config dir", str(session.GetGeminiConfigDir)},
		{"Cursor config dir", str(session.GetCursorConfigDir)},
		{"Hermes config dir", str(session.GetHermesConfigDir)},
		{"OpenCode config dir", str(session.GetOpenCodeConfigDir)},
	}
	for _, r := range resolvers {
		p, err := resolve(r.fn)
		if err != nil {
			t.Errorf("%s: resolver refused or panicked: %v", r.name, err)
			continue
		}
		if p == "" {
			t.Errorf("%s: resolved to an empty path", r.name)
			continue
		}
		if h := hit(p); h != "" {
			t.Errorf("SANDBOX CANARY: %s resolved to %q, inside the real home %q. The test reached the real environment.", r.name, p, h)
		}
	}
}

// TestSandboxCanary_TmuxSocket fails if the default tmux socket this test
// process would use is the one it was started with, or sits in a real home.
func TestSandboxCanary_TmuxSocket(t *testing.T) {
	if v := os.Getenv("TMUX"); v != "" {
		t.Fatalf("TMUX=%q is set under test: tmux would join that server", v)
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		t.Fatal("TMUX_TMPDIR is unset under test: tmux falls back to the user's default socket base")
	}
	sock := filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default")
	_, startTmpdir := sandboxguard.StartTmux()
	startBase := startTmpdir
	if startBase == "" {
		startBase = os.TempDir()
	}
	if startSock := filepath.Join(startBase, "tmux-"+strconv.Itoa(os.Getuid()), "default"); filepath.Clean(sock) == filepath.Clean(startSock) {
		t.Fatalf("default tmux socket %q is the one this process started with: not isolated", sock)
	}
	for _, base := range []string{"/tmp", "/private/tmp", os.TempDir()} {
		if filepath.Clean(dir) == filepath.Clean(base) {
			t.Fatalf("TMUX_TMPDIR=%q is tmux's default base", dir)
		}
	}
	if h := sandboxguard.Violation(sock); h != "" {
		t.Fatalf("default tmux socket %q is inside the real home %q", sock, h)
	}
}

func str(f func() string) func() (string, error) {
	return func() (string, error) { return f(), nil }
}

// resolve runs a resolver and turns a guard panic into an error, so one leak
// reports every path instead of stopping at the first.
func resolve(f func() (string, error)) (p string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f()
}
