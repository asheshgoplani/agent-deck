package tmux

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise the same Start -> NewShellWindow path used by Open Shell Here.
// A later Deck window must keep the sizing policy selected for the session.
func TestSession_NewShellWindowSizePolicy(t *testing.T) {
	requireTmux(t)
	for _, tc := range []struct {
		name      string
		overrides map[string]string
		wantSize  string
		wantAgg   string
		hook      bool
	}{
		{"defaults", nil, "largest", "on", false},
		{"explicit overrides", map[string]string{"window-size": "smallest", "aggressive-resize": "off"}, "smallest", "off", false},
		{"size override", map[string]string{"window-size": "smallest"}, "smallest", "on", false},
		{"resize override", map[string]string{"aggressive-resize": "off"}, "largest", "off", false},
		{"hook selects another window", nil, "largest", "on", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket, unrelated := makeIsolatedServer(t)
			ctl := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
				require.NoError(t, err, "%s", out)
				return strings.TrimSpace(string(out))
			}
			ctl("set-option", "-gw", "window-size", "latest")
			ctl("set-option", "-gw", "aggressive-resize", "off")
			s := NewSession("size-policy", t.TempDir())
			s.SocketName = socket
			s.OptionOverrides = tc.overrides
			require.NoError(t, s.Start(""))
			initial := ctl("display-message", "-p", "-t", s.Name, "#{window_id}")
			check := func(target, wantSize, wantAgg string) {
				t.Helper()
				size := ctl("show-options", "-wAv", "-t", target, "window-size")
				agg := ctl("show-options", "-wAv", "-t", target, "aggressive-resize")
				t.Logf("target=%s size=%s aggressive=%s geometry=%s", target, size, agg,
					ctl("display-message", "-p", "-t", target, "#{window_width}x#{window_height}"))
				assert.Equal(t, wantSize, size, "window-size for %s", target)
				assert.Equal(t, wantAgg, agg, "aggressive-resize for %s", target)
			}
			check(initial, tc.wantSize, tc.wantAgg)
			if tc.hook {
				// User hooks can emit stdout and change selection before creation
				// returns. Neither may redirect our settings to the initial window.
				ctl("set-hook", "-t", s.Name, "after-new-window", "display-message -p hook-output ; select-window -t "+initial)
				ctl("set-option", "-w", "-t", initial, "window-size", "manual")
			}
			for range 2 {
				before := ctl("list-windows", "-t", s.Name, "-F", "#{window_id}")
				started := time.Now()
				require.NoError(t, s.NewShellWindow(t.TempDir()))
				t.Logf("NewShellWindow elapsed=%s", time.Since(started))
				after := strings.Split(ctl("list-windows", "-t", s.Name, "-F", "#{window_id}"), "\n")
				created := 0
				for _, id := range after {
					if !strings.Contains("\n"+before+"\n", "\n"+id+"\n") {
						created++
						check(id, tc.wantSize, tc.wantAgg)
					}
				}
				require.Equal(t, 1, created, "each call must create exactly one window")
			}
			if tc.hook {
				tc.wantSize = "manual"
			}
			check(initial, tc.wantSize, tc.wantAgg)
			check(unrelated, "latest", "off")
			assert.Equal(t, "latest", ctl("show-options", "-gwv", "window-size"))
			assert.Equal(t, "off", ctl("show-options", "-gwv", "aggressive-resize"))
		})
	}
}

func TestSession_NewShellWindowPreservesHookOptions(t *testing.T) {
	requireTmux(t)
	for _, tc := range []struct{ name, hook, want string }{
		{"both", "set-option -w window-size manual ; set-option -w aggressive-resize off", "manual/0"},
		{"size only", "set-option -w window-size manual", "manual/1"},
		{"resize only", "set-option -w aggressive-resize off", "smallest/0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket, target := makeIsolatedServer(t)
			// A user's hook is more specific than Deck's default or configured values.
			out, err := exec.Command("tmux", "-L", socket, "set-hook", "-t", target, "after-new-window", tc.hook).CombinedOutput()
			require.NoError(t, err, "%s", out)
			s := &Session{Name: target, SocketName: socket, OptionOverrides: map[string]string{"window-size": "smallest"}}
			require.NoError(t, s.NewShellWindow(t.TempDir()))
			out, err = exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, "#{window-size}/#{aggressive-resize}").CombinedOutput()
			require.NoError(t, err, "%s", out)
			assert.Equal(t, tc.want, strings.TrimSpace(string(out)))
		})
	}
}

func TestSession_NewShellWindowInvalidOverrideStillCreatesOneWindow(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	s := &Session{Name: target, SocketName: socket, OptionOverrides: map[string]string{"window-size": ""}}
	// Match Start's key-presence and best-effort option semantics. An empty
	// override is not replaced with largest, nor mistaken for creation failure.
	require.NoError(t, s.NewShellWindow(t.TempDir()))
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_id}").CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Len(t, strings.Fields(string(out)), 2)
	out, err = exec.Command("tmux", "-L", socket, "show-options", "-wv", "-t", target, "window-size").CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Empty(t, strings.TrimSpace(string(out)), "invalid override must not silently install a local default")

	s.Name = "missing-window-policy-session"
	require.Error(t, s.NewShellWindow(t.TempDir()), "actual creation failure must still be returned")
}

// This control isolates the secondary report's size-less-client hypothesis.
// It does not establish behavior on the reporter's tmux 3.7a/macOS platform.
func TestSession_SizeLessControlClientGeometry(t *testing.T) {
	requireTmux(t)
	for _, policy := range []string{"latest", "largest"} {
		t.Run(policy, func(t *testing.T) {
			socket, target := makeIsolatedServer(t)
			ctl := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
				require.NoError(t, err, "%s", out)
				return strings.TrimSpace(string(out))
			}
			ctl("set-option", "-w", "-t", target, "window-size", policy)
			ctl("set-option", "-t", target, "status", "off")
			cmd := exec.Command("tmux", "-L", socket, "attach-session", "-t", target)
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 200, Rows: 54})
			require.NoError(t, err)
			var terminalOutput bytes.Buffer
			outputDone := make(chan struct{})
			go func() { _, _ = io.Copy(&terminalOutput, terminal); close(outputDone) }()
			closed := false
			closeTerminal := func() {
				if !closed {
					_ = terminal.Close()
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					<-outputDone
					closed = true
				}
			}
			t.Cleanup(closeTerminal)
			geometry := func() string {
				return ctl("display-message", "-p", "-t", target, "#{window_width}x#{window_height}")
			}
			if !assert.Eventually(t, func() bool { return geometry() == "200x54" }, 5*time.Second, 20*time.Millisecond) {
				closeTerminal()
				t.Fatalf("native attach failed before observer setup: output=%q geometry=%s", terminalOutput.String(), geometry())
			}
			sender, err := OpenKeySender(socket, target)
			require.NoError(t, err)
			defer sender.Close()
			t.Logf("native+observer: %s; clients=%s", geometry(), ctl("list-clients", "-t", target, "-F", "#{client_width}x#{client_height} #{client_flags}"))
			assert.Equal(t, "200x54", geometry())
			closeTerminal()
			require.Eventually(t, func() bool {
				return ctl("list-clients", "-t", target, "-F", "#{client_control_mode}") == "1"
			}, 5*time.Second, 20*time.Millisecond)
			t.Logf("observer only: %s; clients=%s", geometry(), ctl("list-clients", "-t", target, "-F", "#{client_width}x#{client_height} #{client_flags}"))
			assert.Equal(t, "200x54", geometry())
		})
	}
}
