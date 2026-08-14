package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

// shellQuoteForTest mirrors what a LOCAL session's config dir goes through, so
// the "unchanged for local sessions" assertion compares against the real thing.
func shellQuoteForTest(s string) string { return shellescape.Quote(s) }

// evalShellValue evaluates a shell VALUE EXPRESSION the way the spawn payload
// does, under the given HOME, and discards the result. It exists to prove the
// expression cannot execute anything.
func evalShellValue(t *testing.T, expr, home string) error {
	t.Helper()
	cmd := exec.Command("bash", "-c", "V="+expr+"; printf '%s' \"$V\" >/dev/null")
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &shellEvalError{expr: expr, out: string(out), err: err}
	}
	return nil
}

type shellEvalError struct {
	expr string
	out  string
	err  error
}

func (e *shellEvalError) Error() string {
	return e.expr + ": " + e.err.Error() + ": " + e.out
}

// Regression tests for https://github.com/asheshgoplani/agent-deck/issues/1858.
//
// Reporter @yuvalsc: an `add --ssh` session crashes after ~250ms when the remote
// user differs from the local one, because CLAUDE_CONFIG_DIR is built from the
// LOCAL home and passed verbatim into the remote command:
//
//	export CLAUDE_CONFIG_DIR=/Users/yuvals/.local/share/agent-deck/worker-scratch/<id>
//
// Local user `yuvals`, remote user `cloudlydr`: /Users/yuvals does not exist on
// the remote host and is not creatable there, so claude cannot make its config
// dir and exits immediately.
//
// The end-to-end proof (the payload actually running under a different HOME
// through a fake ssh) lives in location_identity_execution_test.go. These tests
// pin the two mechanisms underneath it.

// TestNeedsWorkerScratchConfigDir_NeverForRemoteSessions closes the root cause.
// The worker scratch is a LOCAL tree — a settings.json plus plugin symlinks
// under this machine's data dir — so there is nothing for the remote host to
// use even if the path did exist there.
func TestNeedsWorkerScratchConfigDir_NeverForRemoteSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	// A session with explicit plugins is the plainest scratch trigger.
	local := NewInstanceWithTool("local", t.TempDir(), "claude")
	local.Plugins = []string{"telegram"}
	if !local.NeedsWorkerScratchConfigDir() {
		t.Skip("no scratch trigger fires in this environment; the remote assertion below would be vacuous")
	}

	remote := NewInstanceWithTool("remote", t.TempDir(), "claude")
	remote.Plugins = []string{"telegram"}
	remote.SSHHost = "cloudlydr@remote-host"
	if remote.NeedsWorkerScratchConfigDir() {
		t.Fatal("a remote session was given a LOCAL worker-scratch config dir; the remote host cannot create a path under the local home (#1858)")
	}
}

func TestRemotePluginSetupWarningIsExplicit(t *testing.T) {
	remote := NewInstanceWithTool("remote", t.TempDir(), "claude")
	remote.SSHHost = "cloudlydr@remote-host"
	remote.Plugins = []string{"telegram"}
	warning := remote.remotePluginSetupWarning()
	if !strings.Contains(warning, "unavailable over SSH") || !strings.Contains(warning, "remote Claude profile") {
		t.Fatalf("warning is not actionable: %q", warning)
	}
	local := NewInstanceWithTool("local", t.TempDir(), "claude")
	local.Plugins = []string{"telegram"}
	if got := local.remotePluginSetupWarning(); got != "" {
		t.Fatalf("local plugin setup warned: %q", got)
	}
}

// TestApplyWorkerScratchOverride_RemoteKeepsResolvedDir is defense in depth: even
// if WorkerScratchConfigDir is somehow already set (an older state.db row, a
// profile migration), the swap must not happen for a remote session.
func TestApplyWorkerScratchOverride_RemoteKeepsResolvedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	scratch := filepath.Join(home, ".local", "share", "agent-deck", "worker-scratch", "abc")
	resolved := "/Users/cloudlydr/.claude"

	local := NewInstanceWithTool("local", t.TempDir(), "claude")
	local.WorkerScratchConfigDir = scratch
	if got := local.applyWorkerScratchOverride(resolved); got != scratch {
		t.Fatalf("local scratch override regressed: got %q, want %q", got, scratch)
	}

	remote := NewInstanceWithTool("remote", t.TempDir(), "claude")
	remote.WorkerScratchConfigDir = scratch
	remote.SSHHost = "cloudlydr@remote-host"
	if got := remote.applyWorkerScratchOverride(resolved); got != resolved {
		t.Fatalf("a remote session's CLAUDE_CONFIG_DIR was swapped to the LOCAL scratch path %q; want the resolved dir %q", got, resolved)
	}
}
