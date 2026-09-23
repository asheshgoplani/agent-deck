package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// visualcheck review, 2026-09-23: after the foreign-tmux-server guard
// (7b8e657c) the sandbox's claude-error fixture, a real mid-turn crash, kept
// its stored "starting" row and drew ○, uncounted. The sandbox's private
// server was not the default server of its TMUX_TMPDIR, so the guard (rightly)
// formed no verdict. The rule the guard keeps: a process that cannot prove the
// session's server is its own forms no verdict, but a session whose server IS
// its own (the default socket, or its configured socket_name) keeps reading
// error when it crashed mid-turn. The sandbox now pins its private server to
// its own TMUX_TMPDIR's default socket (tools/visualcheck/sandbox.go).

// startMidTurn starts a claude pane on the session's own server and marks a
// turn in flight (hook "running"), as `session send` leaves it mid-response.
func startMidTurn(t *testing.T, title, socketName string) *Instance {
	t.Helper()
	skipIfNoTmuxBinary(t)
	inst := NewInstanceWithTool(title, t.TempDir(), "claude")
	inst.tmuxSession.SocketName = socketName
	if err := inst.tmuxSession.Start("exec sleep 3600"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })
	deadline := time.Now().Add(5 * time.Second)
	for !inst.tmuxSession.Exists() {
		if time.Now().After(deadline) {
			t.Fatal("pane never came up")
		}
		time.Sleep(50 * time.Millisecond)
	}
	inst.mu.Lock()
	inst.Status = StatusRunning
	inst.hookStatus = "running"
	inst.hookLastUpdate = time.Now()
	inst.lastStartTime = time.Now().Add(-time.Minute) // past the start grace
	inst.mu.Unlock()
	return inst
}

// crash kills the pane under the running turn: the session is gone from its
// server with no exit code, which classifyTerminatedPane reads as error.
func crash(t *testing.T, inst *Instance) {
	t.Helper()
	if err := inst.tmuxSession.Kill(); err != nil {
		t.Fatalf("kill pane: %v", err)
	}
}

// (2, in process) The user's DEFAULT server, the TUI outside tmux: a
// mid-turn crash reads error. A process INSIDE the session's own server
// cannot be staged in this test binary (the tmux isolation guard refuses any
// spawn while $TMUX names a live socket), so those cases run the real binary
// in TestCrashedSession_TUIOnItsOwnServerShowsError.
func TestCrashedSession_DefaultServerOutsideTmuxReadsError(t *testing.T) {
	inst := startMidTurn(t, "crashed", "")
	t.Setenv("TMUX", "")
	crash(t, inst)
	if err := inst.UpdateStatus(); err != nil {
		t.Fatal(err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("status = %q, want error", got)
	}
}

// sandboxTmuxWrapper writes the visualcheck tmux wrapper (tools/visualcheck
// sandbox.go): every call is pinned to the private server `-S socket`, and a
// caller's own -L/-S is refused with exit 64.
func sandboxTmuxWrapper(t *testing.T, realTmux, dir, socket string) {
	t.Helper()
	wrapper := `#!/bin/sh
expect_value=0
for arg in "$@"; do
 if [ "$expect_value" = 1 ]; then expect_value=0; continue; fi
 case "$arg" in
  -S|-L|-S?*|-L?*) echo 'visualcheck: refusing socket override' >&2; exit 64;;
  -f|-c|-T) expect_value=1;;
  -*) ;;
  *) break;;
 esac
done
exec ` + realTmux + ` -S ` + socket + ` -f /dev/null "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
}

// startTUIInsideServer runs the real binary as a TUI in a pane of the server
// that tmuxCmd (plus serverArgs) reaches, so tmux itself sets the pane's
// $TMUX to that server, as for a user who runs agent-deck inside tmux.
func startTUIInsideServer(t *testing.T, bin, profile, tmuxCmd string, serverArgs ...string) func() string {
	t.Helper()
	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[claude]\nhooks_enabled = false\n"), 0o600); err != nil { // no first-run dialogs
		t.Fatal(err)
	}
	cmdline := fmt.Sprintf("exec env HOME=%q PATH=%q TMUX_TMPDIR=%q AGENT_DECK_ALLOW_OUTER_TMUX=1 AGENTDECK_SKIP_UPDATE_CHECK=1 AGENTDECK_TELEMETRY=0 TERM=xterm-256color %q -p %q",
		os.Getenv("HOME"), os.Getenv("PATH"), os.Getenv("TMUX_TMPDIR"), bin, profile)
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// #nosec G204 -- test-owned tmux binary or wrapper on an isolated TMUX_TMPDIR.
		cmd := exec.CommandContext(ctx, tmuxCmd, append(append([]string{}, serverArgs...), args...)...)
		cmd.Env = append(os.Environ(), "TMUX=")
		return cmd.CombinedOutput()
	}
	if out, err := run("new-session", "-d", "-s", "tui", "-x", "160", "-y", "45", cmdline); err != nil {
		t.Fatalf("TUI pane: %v: %s", err, out)
	}
	t.Cleanup(func() { _, _ = run("kill-session", "-t", "tui") })
	return func() string {
		out, _ := run("capture-pane", "-p", "-t", "tui")
		return string(out)
	}
}

// killPrivateServer ends only the test's own private server (`-L name`
// under its own TMUX_TMPDIR, or an absolute `-S` socket path).
func killPrivateServer(realTmux, flag, name, tmpdir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- the test's own private server under its own TMUX_TMPDIR.
	cmd := exec.CommandContext(ctx, realTmux, flag, name, "kill-server")
	cmd.Env = []string{"TMUX_TMPDIR=" + tmpdir, "PATH=/usr/bin:/bin"}
	_ = cmd.Run()
}

// (2, 3) The real binary, as a TUI INSIDE the server the session belongs to:
// the user's default server; a private server that is the session's
// configured socket_name; and the visualcheck sandbox (a private server
// reached through the tmux wrapper, pinned to its own TMUX_TMPDIR's default
// socket). A
// session that crashed mid-turn must read error in the shared status row,
// the list row and the header tally.
func TestCrashedSession_TUIOnItsOwnServerShowsError(t *testing.T) {
	skipIfNoTmuxBinary(t)
	bin := foreignTUIBinary(t)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	for n, c := range []struct {
		name   string
		socket string // the session's configured socket_name
		setup  func(t *testing.T) (tmuxCmd string, serverArgs []string)
	}{
		{"default server", "", func(t *testing.T) (string, []string) {
			return realTmux, nil
		}},
		{"configured private socket", "vc-own", func(t *testing.T) (string, []string) {
			tmpdir := os.Getenv("TMUX_TMPDIR")
			t.Cleanup(func() { killPrivateServer(realTmux, "-L", "vc-own", tmpdir) })
			return realTmux, []string{"-L", "vc-own"}
		}},
		{"visualcheck sandbox wrapper", "", func(t *testing.T) (string, []string) {
			// Short /tmp root, not t.TempDir(): the socket path must fit sun_path.
			root, err := os.MkdirTemp("/tmp", "advc-")
			if err != nil {
				t.Fatal(err)
			}
			tmpdir, binDir := filepath.Join(root, "tmux"), filepath.Join(root, "bin")
			tmuxDir := filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()))
			for _, d := range []string{tmuxDir, binDir} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			socket := filepath.Join(tmuxDir, "default")
			sandboxTmuxWrapper(t, realTmux, binDir, socket)
			t.Setenv("TMUX_TMPDIR", tmpdir)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Cleanup(func() {
				killPrivateServer(realTmux, "-S", socket, tmpdir)
				_ = os.RemoveAll(root)
			})
			return filepath.Join(binDir, "tmux"), nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			profile := fmt.Sprintf("_test_crash_own_server_%d", n)
			_, storage := bootstrapDaemonProfile(t, profile)
			tmuxCmd, serverArgs := c.setup(t)

			child := startMidTurn(t, "crashed", c.socket)
			if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
				t.Fatal(err)
			}
			db := storage.GetDB()
			if err := db.WriteStatus(child.ID, "running", "claude"); err != nil {
				t.Fatal(err)
			}
			crash(t, child)

			capture := startTUIInsideServer(t, bin, profile, tmuxCmd, serverArgs...)
			rowStatus, pane := "", ""
			deadline := time.Now().Add(25 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(500 * time.Millisecond)
				rows, err := db.ReadAllStatuses()
				if err != nil {
					t.Fatal(err)
				}
				rowStatus = rows[child.ID].Status
				pane = capture()
				if rowStatus == string(StatusError) && strings.Contains(pane, "✕ crashed") && strings.Contains(pane, "✕ 1 error") {
					return
				}
			}
			t.Fatalf("crashed session: status row %q; want error, a ✕ row and ✕ 1 error in the header; pane:\n%s", rowStatus, pane)
		})
	}
}
