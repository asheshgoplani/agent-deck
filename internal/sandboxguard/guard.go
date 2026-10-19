// Package sandboxguard stops a Go test from resolving a state or tool-config
// path inside the real user's home.
//
// Tests have reached the real environment more than once: a TUI under test
// auto-installed a release mid-CI (#2251, #2252), scripts hardcoded the
// author's home (#2001), and tests read the live Claude config through
// CLAUDE_CONFIG_DIR. internal/agentpaths already refused agent-deck paths under
// the passwd home, but nothing guarded the tool config dirs (~/.claude,
// ~/.codex, ~/.gemini, ...), and nothing compared against the HOME a test
// binary was started with when that differs from the passwd home.
//
// The guard compares a resolved path against two "real homes":
//
//   - the passwd home of the current user, and
//   - the HOME the test binary was started with, captured at package init,
//     before any TestMain can call testutil.IsolateHome. It is skipped when it
//     lives inside the temp dir (tests re-exec themselves with
//     HOME=t.TempDir(); a sandbox runner's HOME=/tmp/home is throwaway) or
//     when the process started with the isolation marker already set.
//
// A path under one of them is refused, except when it is under os.TempDir()
// and the temp dir itself sits inside that home (a developer TMPDIR=~/tmp, or a
// build sandbox), or when testutil.IsolateHome's marker is set and the path is
// under a HOME/XDG root nested strictly inside that home.
//
// Outside a test binary (testing.Testing() is false) every check is a no-op.
// There is no opt-out env var on purpose.
package sandboxguard

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// homeIsolationMarkerEnv mirrors testutil.HomeIsolationMarkerEnv. It is a
// literal so production code does not import testutil.
const homeIsolationMarkerEnv = "AGENT_DECK_TEST_HOME_ISOLATED"

// isolationRootEnvs are the roots testutil.IsolateHome manages, and the ones a
// test points at its own t.TempDir().
var isolationRootEnvs = []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}

// Captured once, at init, so a TestMain that swaps HOME cannot hide the
// original from the guard.
var (
	startHome       = os.Getenv("HOME")
	startTmux       = os.Getenv("TMUX")
	startTmuxTmpdir = os.Getenv("TMUX_TMPDIR")
	startTempDir    = os.TempDir()
	passwdHome      = lookupPasswdHome()
	// startIsolated is true when this process was launched with the home
	// isolation marker already set: a helper child an isolated test re-execs
	// with a HOME it picked itself. That HOME is the test's sandbox, not a
	// real home.
	startIsolated = os.Getenv(homeIsolationMarkerEnv) == "1"
)

func lookupPasswdHome() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// StartHome returns the HOME this process was started with, cleaned.
func StartHome() string { return filepath.Clean(startHome) }

// StartTmux returns the TMUX and TMUX_TMPDIR values this process was started
// with, before testutil.IsolateTmuxSocket could change them.
func StartTmux() (tmux, tmuxTmpdir string) { return startTmux, startTmuxTmpdir }

// RealHomes returns the homes the guard protects: the start HOME and the
// passwd home, without duplicates, empty, relative, "/" or temp-dir values.
func RealHomes() []string {
	candidates := []string{passwdHome}
	if startHomeIsReal(startHome, startTempDir, startIsolated) {
		candidates = append([]string{startHome}, candidates...)
	}
	return realHomes(candidates, startTempDir)
}

// startHomeIsReal reports whether the start HOME is worth guarding. It is not
// when the process was started with the isolation marker (a helper child of
// an isolated test) or when it lives inside the temp dir: tests re-exec
// themselves or the CLI with HOME=t.TempDir(), and a sandbox runner's
// throwaway HOME=/tmp/home holds nothing real. The passwd home is guarded
// either way, and in CI the start HOME is the runner's home outside the temp
// dir, so it is guarded there.
func startHomeIsReal(home, tempDir string, isolated bool) bool {
	h, tmp := cleanAbs(home), cleanAbs(tempDir)
	if h == "" || isolated {
		return false
	}
	return tmp == "" || !under(h, tmp)
}

func realHomes(candidates []string, tempDir string) []string {
	var out []string
	tmp := cleanAbs(tempDir)
	for _, c := range candidates {
		h := cleanAbs(c)
		if h == "" || h == string(filepath.Separator) || h == tmp {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o == h
		}
		if !dup {
			out = append(out, h)
		}
	}
	return out
}

// Violation returns the real home that path resolves under, or "" when the
// path is safe or the process is not a test binary.
func Violation(path string) string {
	if !testing.Testing() {
		return ""
	}
	return violation(path, RealHomes(), startTempDir, os.Getenv)
}

func violation(path string, homes []string, tempDir string, getenv func(string) string) string {
	p := cleanAbs(path)
	if p == "" {
		return ""
	}
	tmp := cleanAbs(tempDir)
	marker := getenv(homeIsolationMarkerEnv) == "1"
	for _, h := range homes {
		if !under(p, h) {
			continue
		}
		if tmp != "" && tmp != h && under(tmp, h) && under(p, tmp) {
			continue // the temp area lives inside this home; t.TempDir() is fine
		}
		if marker && underNestedRoot(p, h, getenv) {
			continue
		}
		return h
	}
	return ""
}

// underNestedRoot reports whether p is under an isolation root that is nested
// strictly inside home h. A root equal to h, or above it, is the un-isolated
// case and never counts.
func underNestedRoot(p, h string, getenv func(string) string) bool {
	for _, env := range isolationRootEnvs {
		r := cleanAbs(strings.TrimSpace(getenv(env)))
		if r == "" || r == h || !under(r, h) {
			continue
		}
		if under(p, r) {
			return true
		}
	}
	return false
}

// Check returns an error when a test binary resolves kind to a path under a
// real home. kind names the path in the message ("Claude config dir").
func Check(kind, path string) error {
	h := Violation(path)
	if h == "" {
		return nil
	}
	return fmt.Errorf("sandboxguard: %s resolved to %q, inside the real home %q, while running under go test; "+
		"the test is not sandboxed: call testutil.IsolateHome() in TestMain, or point HOME or the tool's "+
		"override env var at t.TempDir()", kind, path, h)
}

// Must returns path, or panics when a test binary resolved it under a real
// home. Use it in resolvers that return a bare string: the panic stops the
// test before anything reads or writes the live file.
func Must(kind, path string) string {
	if err := Check(kind, path); err != nil {
		panic(err.Error())
	}
	return path
}

func cleanAbs(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

func under(p, root string) bool {
	if root == "" {
		return false
	}
	if root == string(filepath.Separator) {
		return true
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
