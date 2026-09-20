package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

func TestPreviewSizerPreservesWindowPolicy(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
	s := NewSession("restart-size", t.TempDir())
	s.SocketName = socket
	require.NoError(t, s.Start(""))
	ctl("set-option", "-t", s.Name, "status", "off")
	for _, policy := range []string{"latest", "largest", "smallest"} {
		ctl("set-option", "-w", "-t", s.Name, "window-size", policy)
		require.NoError(t, (&PreviewSizer{}).Fit(s, -1, 112, 44))
		require.Equal(t, "112x44", ctl("display-message", "-p", "-t", s.Name, "#{window_width}x#{window_height}"))
		require.Equal(t, policy, ctl("show-options", "-wAv", "-t", s.Name, "window-size"))
	}
	require.NoError(t, (&PreviewSizer{}).Fit(s, -1, 0, 0))
	require.Equal(t, "112x44", ctl("display-message", "-p", "-t", s.Name, "#{window_width}x#{window_height}"))
}

func TestPreviewSizerLeavesInteractiveViewerAlone(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	cmd := exec.Command("tmux", "-L", socket, "attach-session", "-t", target)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 40})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, terminal); close(done) }()
	t.Cleanup(func() { _ = terminal.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait(); <-done })
	s := &Session{Name: target, SocketName: socket}
	require.Eventually(t, func() bool {
		viewers, err := ListViewers(context.Background(), socket, target)
		return err == nil && len(viewers) == 1
	}, 3*time.Second, 10*time.Millisecond)
	geometry := func() string {
		out, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, "#{window_width}x#{window_height}/#{window-size}").Output()
		require.NoError(t, err)
		return string(out)
	}
	before := geometry()
	require.NoError(t, (&PreviewSizer{}).Fit(s, -1, 112, 45))
	require.Equal(t, before, geometry())
}

func TestPreviewSizerBoundaries(t *testing.T) {
	for _, scenario := range []string{"status-on", "status-two", "tiny", "manual", "inherited", "linked", "split", "auxiliary", "base-index"} {
		t.Run(scenario, func(t *testing.T) {
			requireTmux(t)
			socket, target := makeIsolatedServer(t)
			ctl := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
				require.NoError(t, err, "%s", out)
				return strings.TrimSpace(string(out))
			}
			s := &Session{Name: target, SocketName: socket}
			ctl("set-option", "-t", target, "status", "off")
			ctl("set-option", "-w", "-t", target, "window-size", "latest")
			rows, want := 45, "112x45"
			switch scenario {
			case "status-on":
				ctl("set-option", "-t", target, "status", "on")
				want = "112x44"
			case "status-two":
				ctl("set-option", "-t", target, "status", "2")
				want = "112x43"
			case "tiny":
				ctl("set-option", "-t", target, "status", "2")
				rows = 1
			case "manual":
				ctl("set-option", "-w", "-t", target, "window-size", "manual")
			case "inherited":
				ctl("set-option", "-gw", "window-size", "largest")
				ctl("set-option", "-wu", "-t", target, "window-size")
			case "linked":
				ctl("new-session", "-d", "-s", "other")
				ctl("link-window", "-s", target+":^", "-t", "other:9")
			case "split":
				ctl("split-window", "-d", "-t", target)
			case "auxiliary":
				ctl("new-window", "-t", target+":9", "-n", "aux")
			case "base-index":
				ctl("move-window", "-s", target+":^", "-t", target+":5")
			}
			geometry := func() string {
				return ctl("display-message", "-p", "-t", target+":^", "#{window_width}x#{window_height}")
			}
			if scenario == "manual" || scenario == "linked" || scenario == "split" || scenario == "tiny" {
				want = geometry()
			}
			policy := ctl("show-options", "-wqv", "-t", target+":^", "window-size")
			aux := ""
			if scenario == "auxiliary" {
				aux = ctl("display-message", "-p", "-t", target+":9", "#{window_width}x#{window_height}")
			}
			index := -1
			if scenario == "auxiliary" {
				index = 0
			}
			require.NoError(t, (&PreviewSizer{}).Fit(s, index, 112, rows))
			require.Equal(t, want, geometry())
			require.Equal(t, policy, ctl("show-options", "-wqv", "-t", target+":^", "window-size"))
			if scenario == "auxiliary" {
				require.Equal(t, aux, ctl("display-message", "-p", "-t", target+":9", "#{window_width}x#{window_height}"))
			}
		})
	}
}

func TestPreviewSizerWithControlClient(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	cmd := exec.Command("tmux", "-L", socket, "-C", "attach-session", "-t", target)
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	query := func(format string) string {
		out, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "-t", target, format).Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	require.Eventually(t, func() bool { return query("#{session_attached}") == "1" }, time.Second, 10*time.Millisecond)
	_, err = exec.Command("tmux", "-L", socket, "set-option", "-w", "-t", target, "window-size", "latest").Output()
	require.NoError(t, err)
	sizer := &PreviewSizer{}
	require.NoError(t, sizer.Fit(&Session{Name: target, SocketName: socket}, -1, 112, 45))
	require.Equal(t, "112", query("#{window_width}"), "an unsized background control pipe must not block preview fitting")
	_, err = io.WriteString(input, "refresh-client -C 100,40\n")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return query("#{window_width}") == "100" }, time.Second, 10*time.Millisecond)
	before := query("#{window_width}x#{window_height}/#{window-size}")
	require.NoError(t, sizer.Fit(&Session{Name: target, SocketName: socket}, -1, 120, 50))
	require.Eventually(t, func() bool { return query("#{window_width}x#{window_height}/#{window-size}") == before }, time.Second, 10*time.Millisecond)
}

func TestPreviewSizerDoesNotFightAnotherPreview(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	s := &Session{Name: target, SocketName: socket}
	ctl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
	ctl("set-option", "-t", target, "status", "off")
	ctl("set-option", "-w", "-t", target, "window-size", "latest")
	first, second := &PreviewSizer{}, &PreviewSizer{}
	require.NoError(t, first.Fit(s, -1, 112, 45))
	require.NoError(t, second.Fit(s, -1, 90, 30))
	require.NoError(t, first.Fit(s, -1, 112, 45))
	require.Equal(t, "90x30", ctl("display-message", "-p", "-t", target, "#{window_width}x#{window_height}"))
	// A real viewport change is a new request, while repeated polling is not.
	require.NoError(t, first.Fit(s, -1, 110, 44))
	require.Equal(t, "110x44", ctl("display-message", "-p", "-t", target, "#{window_width}x#{window_height}"))
	// CLI-style respawn retains the window but changes the process generation.
	ctl("respawn-pane", "-k", "-t", target, "sleep 300")
	require.NoError(t, second.Fit(s, -1, 90, 30))
	require.Equal(t, "90x30", ctl("display-message", "-p", "-t", target, "#{window_width}x#{window_height}"))
}

func BenchmarkPreviewCapture(b *testing.B) {
	socket := fmt.Sprintf("preview-bench-%d", os.Getpid())
	require.NoError(b, exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "preview", "sleep 300").Run())
	b.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	s := &Session{Name: "preview", SocketName: socket}
	sizer := &PreviewSizer{}
	require.NoError(b, sizer.Fit(s, -1, 112, 45))
	for _, fit := range []bool{false, true} {
		b.Run(fmt.Sprintf("fit=%t", fit), func(b *testing.B) {
			for b.Loop() {
				if fit {
					require.NoError(b, sizer.Fit(s, -1, 112, 45))
				}
				_, err := s.CaptureFullHistory()
				require.NoError(b, err)
			}
		})
	}
}
