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
// Outside a test binary (testing.Testing() is false) every check is a no-op,
// and nothing is looked up: the start environment is only captured in a test
// binary, and the passwd home only on the first check.
// There is no opt-out env var on purpose.
package sandboxguard

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// homeIsolationMarkerEnv mirrors testutil.HomeIsolationMarkerEnv. It is a
// literal so production code does not import testutil.
const homeIsolationMarkerEnv = "AGENT_DECK_TEST_HOME_ISOLATED"

// isolationRootEnvs are the roots testutil.IsolateHome manages, and the ones a
// test points at its own t.TempDir().
var isolationRootEnvs = []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}

// ToolConfigDirEnvs are the agent tools' own config-dir overrides. Each one
// wins over $HOME in agent-deck's resolvers, so a value inherited from the
// shell that ran go test points a test at the live tool config even after HOME
// was swapped. testutil.IsolateHome clears every one of them (testutil
// re-exports this list), and Check names the one a refused path came through.
var ToolConfigDirEnvs = []string{
	"CLAUDE_CONFIG_DIR",
	"CODEX_HOME",
	"COPILOT_CONFIG_DIR",
	"DSH_HOME",
	"GEMINI_CLI_HOME",
	"GEMINI_CLI_TRUSTED_FOLDERS_PATH",
	"PI_CODING_AGENT_DIR",
}

// startEnv is what the guard needs from the environment the process started
// with.
type startEnv struct {
	home, tmux, tmuxTmpdir, tempDir string
	// isolated is true when this process was launched with the home
	// isolation marker already set: a helper child an isolated test re-execs
	// with a HOME it picked itself. That HOME is the test's sandbox, not a
	// real home.
	isolated bool
}

// start is captured at package init, so a TestMain that swaps HOME cannot hide
// the original from the guard. Outside a test binary it stays empty: a
// production binary (every hook invocation included) does no lookups here.
var start = captureStart(testing.Testing(), os.Getenv, os.TempDir)

func captureStart(isTest bool, getenv func(string) string, tempDir func() string) startEnv {
	if !isTest {
		return startEnv{}
	}
	return startEnv{
		home:       getenv("HOME"),
		tmux:       getenv("TMUX"),
		tmuxTmpdir: getenv("TMUX_TMPDIR"),
		tempDir:    tempDir(),
		isolated:   getenv(homeIsolationMarkerEnv) == "1",
	}
}

// lazyValue runs lookup at most once, on first use.
type lazyValue struct {
	once   sync.Once
	lookup func() string
	val    string
	done   atomic.Bool
}

func (l *lazyValue) get() string {
	l.once.Do(func() {
		l.val = l.lookup()
		l.done.Store(true)
	})
	return l.val
}

// passwdHome is looked up on the first check, not at init: the passwd home
// does not change when TestMain swaps HOME, so there is nothing to capture
// early, and only a test binary ever asks for it.
var passwdHome = &lazyValue{lookup: lookupPasswdHome}

func lookupPasswdHome() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// StartHome returns the HOME this process was started with, cleaned.
func StartHome() string { return filepath.Clean(start.home) }

// StartTmux returns the TMUX and TMUX_TMPDIR values this process was started
// with, before testutil.IsolateTmuxSocket could change them.
func StartTmux() (tmux, tmuxTmpdir string) { return start.tmux, start.tmuxTmpdir }

// RealHomes returns the homes the guard protects: the start HOME and the
// passwd home, without duplicates, empty, relative, "/" or temp-dir values.
func RealHomes() []string {
	candidates := []string{passwdHome.get()}
	if startHomeIsReal(start.home, start.tempDir, start.isolated) {
		candidates = append([]string{start.home}, candidates...)
	}
	return realHomes(candidates, start.tempDir)
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
	return violation(path, RealHomes(), start.tempDir, os.Getenv)
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
// real home. kind names the path in the message ("Claude config dir"). When
// the path came through an env var (a leaked CLAUDE_CONFIG_DIR, or a HOME
// nobody isolated), the message names that variable and its value.
func Check(kind, path string) error {
	h := Violation(path)
	if h == "" {
		return nil
	}
	env, val := leakSource(path, os.Getenv)
	return violationError(kind, path, h, env, val)
}

// checkFrom is Check for a path whose source is known: env names the variable
// the caller built it from, so the message does not have to guess from the
// path prefix. A home-derived path with CLAUDE_CONFIG_DIR=$HOME/.claude
// inherited would otherwise be blamed on CLAUDE_CONFIG_DIR, whose fix does
// not stop that site.
func checkFrom(kind, path, env string) error {
	h := Violation(path)
	if h == "" {
		return nil
	}
	return violationError(kind, path, h, env, os.Getenv(env))
}

// homeRootEnvs are the env vars a home-derived path comes through when no
// tool override is set.
var homeRootEnvs = []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "HOME"}

// leakSource returns the env var, and its value, that path resolved under:
// a tool override first, since it beats HOME in the resolvers, then the XDG
// roots and HOME.
func leakSource(path string, getenv func(string) string) (env, val string) {
	p := cleanAbs(path)
	for _, k := range append(append([]string{}, ToolConfigDirEnvs...), homeRootEnvs...) {
		v := strings.TrimSpace(getenv(k))
		r := cleanAbs(v)
		if r == "" || r == string(filepath.Separator) {
			continue
		}
		if under(p, r) {
			return k, v
		}
	}
	return "", ""
}

func violationError(kind, path, home, env, val string) error {
	prefix := fmt.Sprintf("sandboxguard: %s resolved to %q, inside the real home %q, while running under go test", kind, path, home)
	switch {
	case env == "":
		return fmt.Errorf("%s; the test is not sandboxed: call testutil.IsolateHome() in TestMain, or point HOME or the tool's "+
			"override env var at t.TempDir()", prefix)
	case isHomeRootEnv(env):
		return fmt.Errorf("%s, through %s=%q; %s still points at the real home: call testutil.IsolateHome() in TestMain, or "+
			"t.Setenv(%q, t.TempDir()) in the test", prefix, env, val, env, env)
	default:
		return fmt.Errorf("%s, through %s=%q, leaked in from the environment that ran go test; testutil.IsolateHome() clears it "+
			"(add it to sandboxguard.ToolConfigDirEnvs if it is new), or t.Setenv(%q, t.TempDir()) in the test", prefix, env, val, env)
	}
}

func isHomeRootEnv(env string) bool {
	for _, k := range homeRootEnvs {
		if k == env {
			return true
		}
	}
	return false
}

// HomeJoin returns filepath.Join(home, elem...) for the user's home
// directory (os.UserHomeDir), and the os.UserHomeDir error when there is none.
// Under go test the result goes through Must, so a home-derived default
// (~/.claude, ~/.codex, ~/.claude.json) that lands in a real home panics
// before the caller reads or writes it. Outside a test binary it is exactly
// os.UserHomeDir plus filepath.Join.
func HomeJoin(kind string, elem ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return home, err
	}
	return MustHome(kind, filepath.Join(append([]string{home}, elem...)...)), nil
}

// MustHome is Must for a path the caller joined onto os.UserHomeDir() itself:
// the refusal is attributed to HOME, whatever tool override happens to share
// the prefix. Outside a test binary it returns path unchanged.
func MustHome(kind, path string) string {
	if err := checkFrom(kind, path, "HOME"); err != nil {
		panic(err.Error())
	}
	return path
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
