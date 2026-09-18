//go:build !windows

package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shared attach (#2186 follow-up): two people on one session must each see
// it full-size while they use it. rc.6 pinned window-size=smallest, which
// boxes every client larger than the smallest one into a corner and fills
// the rest with dots; `latest` follows the client that last attached, typed
// or resized.

func TestSharedViewOptionArgs_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		targets    []string
		overrides  map[string]string
		windowSize string
		want       []string
	}{
		{
			name:       "one window, latest",
			targets:    []string{"@3"},
			windowSize: "latest",
			want: []string{
				"set-option", "-w", "-q", "-t", "@3", "window-size", "latest", ";",
				"set-option", "-w", "-q", "-t", "@3", "aggressive-resize", "on",
			},
		},
		{
			name:       "every existing window gets the policy, not only the current one",
			targets:    []string{"@0", "@7"},
			windowSize: "latest",
			want: []string{
				"set-option", "-w", "-q", "-t", "@0", "window-size", "latest", ";",
				"set-option", "-w", "-q", "-t", "@0", "aggressive-resize", "on", ";",
				"set-option", "-w", "-q", "-t", "@7", "window-size", "latest", ";",
				"set-option", "-w", "-q", "-t", "@7", "aggressive-resize", "on",
			},
		},
		{
			name:       "fallback policy for a tmux without latest",
			targets:    []string{"sess"},
			windowSize: "largest",
			want: []string{
				"set-option", "-w", "-q", "-t", "sess", "window-size", "largest", ";",
				"set-option", "-w", "-q", "-t", "sess", "aggressive-resize", "on",
			},
		},
		{
			name:       "user [tmux] options win",
			targets:    []string{"@1"},
			overrides:  map[string]string{"window-size": "smallest", "aggressive-resize": "off"},
			windowSize: "latest",
			want: []string{
				"set-option", "-w", "-q", "-t", "@1", "window-size", "smallest", ";",
				"set-option", "-w", "-q", "-t", "@1", "aggressive-resize", "off",
			},
		},
		{
			name:       "no windows, no command",
			windowSize: "latest",
			want:       nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sharedViewOptionArgs(tc.targets, tc.overrides, tc.windowSize))
		})
	}
}

func TestShellWindowOptionArgs_PreservesHookInstalledOptions(t *testing.T) {
	t.Parallel()
	got := shellWindowOptionArgs("@4", nil, sharedViewWindowSize)
	assert.Equal(t, []string{
		"set-window-option", "-oq", "-t", "@4", "window-size", "latest", ";",
		"set-window-option", "-oq", "-t", "@4", "aggressive-resize", "on",
	}, got)
}

// sharedViewClient is a pty-backed `tmux attach` at a chosen size.
type sharedViewClient struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

func attachSharedViewClient(t *testing.T, socket, name string, cols, rows int) *sharedViewClient {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "attach-session", "-t", name)
	// A tmux client needs a terminal type; the test runner (CI, Docker) may
	// have none, and "terminal does not support clear" would end it at once.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) // #nosec G115 -- test sizes
	require.NoError(t, err)
	c := &sharedViewClient{cmd: cmd, ptmx: ptmx}
	t.Cleanup(c.close)
	// Drain the client's output so tmux never blocks on a full pty.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()
	return c
}

func (c *sharedViewClient) close() {
	_ = c.ptmx.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
}

func (c *sharedViewClient) resize(t *testing.T, cols, rows int) {
	t.Helper()
	require.NoError(t, pty.Setsize(c.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})) // #nosec G115 -- test sizes
}

// waitWindowSize polls the window size until it matches (tmux resizes
// asynchronously after a client attaches, types or resizes).
func waitWindowSize(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		out, err := s.tmuxCmd("display-message", "-p", "-t", s.Name, "#{window_width}x#{window_height}").Output()
		if err == nil {
			got = strings.TrimSpace(string(out))
			if got == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("window size = %s, want %s", got, want)
}

func windowOption(t *testing.T, s *Session, target, option string) string {
	t.Helper()
	out, err := s.tmuxCmd("show-options", "-w", "-t", target, "-v", option).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func newSharedViewSession(t *testing.T, tag string) *Session {
	t.Helper()
	skipIfNoTmuxBinary(t)
	s := NewSession("test-shared-"+tag, t.TempDir())
	s.InstanceID = "test-instance-shared-" + tag
	s.SocketName = fmt.Sprintf("ad-shared-%s-%d", tag, time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", s.SocketName, "kill-server").Run() })
	require.NoError(t, s.Start("sleep 3600"))
	t.Cleanup(func() { _ = s.Kill() })
	return s
}

// TestSharedView_TwoClientsFollowTheActiveOne_Integration is the maintainer's
// screenshot: a 200x60 terminal and a 120x40 terminal on one session. Under
// rc.6's `smallest` the 200x60 client got a 120x39 box and dots; under
// `latest` the window follows whoever attached, typed or resized last, so
// each viewer is full-size while using the session.
func TestSharedView_TwoClientsFollowTheActiveOne_Integration(t *testing.T) {
	s := newSharedViewSession(t, "two")

	assert.Equal(t, "latest", windowOption(t, s, s.Name, "window-size"),
		"Start must install window-size=latest on the window")
	assert.Equal(t, "on", windowOption(t, s, s.Name, "aggressive-resize"))

	small := attachSharedViewClient(t, s.SocketName, s.Name, 120, 40)
	waitWindowSize(t, s, "120x39") // one status row

	// The larger client attaches second: the window must grow to it, not
	// stay boxed at the smaller client's size.
	large := attachSharedViewClient(t, s.SocketName, s.Name, 200, 60)
	waitWindowSize(t, s, "200x59")

	// The smaller client resizes (a font change, a split): it becomes the
	// active viewer and the window follows it.
	small.resize(t, 130, 41)
	waitWindowSize(t, s, "130x40")

	// The larger client types: the window comes back to it.
	_, err := large.ptmx.Write([]byte(" "))
	require.NoError(t, err)
	waitWindowSize(t, s, "200x59")
}

// TestSharedView_ApplyCoversEveryWindowAndResetsManual_Integration proves
// the two tmux facts the fix rests on: window-size is a per-window option
// (a window created after the session was configured gets the server
// default, not the session's policy), and resize-window pins a window to
// `manual` until something re-applies the policy, which every attach does.
func TestSharedView_ApplyCoversEveryWindowAndResetsManual_Integration(t *testing.T) {
	s := newSharedViewSession(t, "windows")

	// A window created behind agent-deck's back (a user's `tmux new-window`)
	// takes tmux's own default. Force a non-latest value so the assertion
	// below cannot pass by accident on a server whose default is latest.
	require.NoError(t, s.tmuxCmd("new-window", "-d", "-t", s.Name, "sleep 3600").Run())
	require.NoError(t, s.tmuxCmd("set-option", "-w", "-t", s.Name+":1", "window-size", "smallest").Run())
	assert.Equal(t, "smallest", windowOption(t, s, s.Name+":1", "window-size"))

	// An old web bridge or a user script resized the current window: pinned.
	require.NoError(t, s.tmuxCmd("resize-window", "-t", s.Name+":0", "-x", "100", "-y", "30").Run())
	assert.Equal(t, "manual", windowOption(t, s, s.Name+":0", "window-size"))

	s.applySharedViewSize()

	assert.Equal(t, "latest", windowOption(t, s, s.Name+":0", "window-size"), "manual must be reset at attach")
	assert.Equal(t, "latest", windowOption(t, s, s.Name+":1", "window-size"), "every window gets the policy")
	assert.Equal(t, "on", windowOption(t, s, s.Name+":1", "aggressive-resize"))

	// A shell window agent-deck creates itself is configured the same way.
	require.NoError(t, s.NewShellWindow(""))
	assert.Equal(t, "latest", windowOption(t, s, s.Name+":2", "window-size"))
}

// TestSharedView_PrepareAttachListsOthersBeforeSizing_Integration: the
// viewer list handed to the "also viewing" notice is who was there before
// this attach, and the size policy is in place before the client connects.
func TestSharedView_PrepareAttachListsOthersBeforeSizing_Integration(t *testing.T) {
	s := newSharedViewSession(t, "prepare")
	attachSharedViewClient(t, s.SocketName, s.Name, 120, 40)
	waitWindowSize(t, s, "120x39")
	require.NoError(t, s.tmuxCmd("set-option", "-w", "-t", s.Name, "window-size", "smallest").Run())

	others := s.prepareSharedAttach(context.Background())
	require.Len(t, others, 1)
	assert.Equal(t, 120, others[0].Width)
	assert.Equal(t, 40, others[0].Height)
	assert.Equal(t, "latest", windowOption(t, s, s.Name, "window-size"))
}
