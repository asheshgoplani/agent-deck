//go:build !windows

package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	deckterminal "github.com/asheshgoplani/agent-deck/internal/terminal"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedTerminal_HonoursTmuxOptionOverrides: the embedded attach is
// one of the paths that re-applies the shared-attach size policy, and it
// must do so with the user's [tmux.options] like Session.Start and the TUI
// Enter path do, so a `window-size = "smallest"` opt-out is never reset to
// `latest` by opening the session in the embedded terminal.
func TestEmbeddedTerminal_HonoursTmuxOptionOverrides(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := setIsolatedAgentDeckDir(t)
	config := "[tmux.options]\nwindow-size = \"smallest\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, session.UserConfigFileName), []byte(config), 0o644))
	session.ClearUserConfigCache()
	require.Equal(t, "smallest", session.SharedViewOverrides()["window-size"])

	socket := fmt.Sprintf("ad-embedded-override-%d", os.Getpid())
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "tmux %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	// This private server lives only in the disposable test container.
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	tm("new-session", "-d", "-x", "60", "-y", "8", "-s", "override", "sleep 300")
	tm("set-option", "-w", "-t", "override", "window-size", "latest")

	terminal, err := startEmbeddedTerminalWithClipboard(context.Background(),
		deckterminal.AttachRequest{Name: "override", SocketName: socket},
		embeddedTerminalSize{Cols: 60, Rows: 8}, nil)
	require.NoError(t, err)
	defer terminal.Close()

	require.Eventually(t, func() bool {
		return tm("list-clients", "-F", "#{client_name}") != ""
	}, 3*time.Second, 50*time.Millisecond, "embedded tmux client never attached")
	require.Equal(t, "smallest", tm("display-message", "-p", "-t", "override", "#{window-size}"),
		"the user's window-size override survives the embedded attach")
}
