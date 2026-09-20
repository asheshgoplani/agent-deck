package tmux

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

func TestFitDetachedPreviewReconcilesCurrentGeometryWithoutCache(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	s := &Session{Name: target, SocketName: socket}

	ctl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
	geometry := func() string {
		return ctl("display-message", "-p", "-t", target, "#{window_width}x#{window_height}")
	}

	ctl("set-option", "-t", target, "status", "off")
	ctl("set-option", "-w", "-t", target, "window-size", "latest")

	require.NoError(t, s.FitDetachedPreview(112, 44))
	require.Equal(t, "112x44", geometry())
	require.Equal(t, "latest", ctl("show-options", "-wqv", "-t", target, "window-size"))

	// Model the failure that made the previous #2326 design stale: another
	// attach/detach changes the real window geometry between preview refreshes.
	ctl("resize-window", "-t", target, "-x", "80", "-y", "30")
	ctl("set-option", "-w", "-t", target, "window-size", "latest")
	require.Equal(t, "80x30", geometry())

	// The same viewport request must consult current tmux state, not a cached
	// "last requested" size, and therefore restore the preview fit.
	require.NoError(t, s.FitDetachedPreview(112, 44))
	require.Equal(t, "112x44", geometry())
	require.Equal(t, "latest", ctl("show-options", "-wqv", "-t", target, "window-size"))
}

func TestFitDetachedPreviewAccountsForStatusRows(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	s := &Session{Name: target, SocketName: socket}

	ctl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}

	ctl("set-option", "-t", target, "status", "on")
	ctl("set-option", "-w", "-t", target, "window-size", "latest")
	require.NoError(t, s.FitDetachedPreview(112, 45))

	require.Equal(t, "112x44", ctl("display-message", "-p", "-t", target, "#{window_width}x#{window_height}"))
	require.Equal(t, "latest", ctl("show-options", "-wqv", "-t", target, "window-size"))
}

func TestFitDetachedPreviewLeavesInteractiveViewerGeometryAlone(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	cmd := exec.Command("tmux", "-L", socket, "attach-session", "-t", target)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 90, Rows: 30})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, terminal)
		close(done)
	}()
	t.Cleanup(func() {
		_ = terminal.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-done
	})

	s := &Session{Name: target, SocketName: socket}
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		viewers, err := ListViewers(ctx, socket, target)
		return err == nil && len(viewers) == 1
	}, 3*time.Second, 20*time.Millisecond)

	before, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, "#{window_width}x#{window_height}/#{window-size}").Output()
	require.NoError(t, err)

	require.NoError(t, s.FitDetachedPreview(112, 45))

	after, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, "#{window_width}x#{window_height}/#{window-size}").Output()
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}
