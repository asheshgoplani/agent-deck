package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func isolateTmuxXDGPaths(t *testing.T) (home string, data string) {
	t.Helper()

	root := t.TempDir()
	home = filepath.Join(root, "home")
	data = filepath.Join(root, "xdg-data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg-cache"))
	t.Setenv(badgeUpdatesDirEnv, "")
	return home, data
}

func TestXDGPaths_NewUsersUseDataHome(t *testing.T) {
	_, data := isolateTmuxXDGPaths(t)
	base := filepath.Join(data, "agent-deck")

	require.Equal(t, filepath.Join(base, "logs"), LogDir())

	ackPath, err := GetAckSignalPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, "ack-signal"), ackPath)

	require.Equal(t, filepath.Join(base, "badge-updates"), BadgeUpdatesDir())
}

func TestXDGPaths_LegacyAckSignalFallbackIsCategorySpecific(t *testing.T) {
	home, data := isolateTmuxXDGPaths(t)
	base := filepath.Join(data, "agent-deck")

	legacyAck := filepath.Join(home, ".agent-deck", "ack-signal")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyAck), 0o700))
	require.NoError(t, os.WriteFile(legacyAck, []byte("session-id"), 0o600))

	ackPath, err := GetAckSignalPath()
	require.NoError(t, err)
	require.Equal(t, legacyAck, ackPath)
	require.Equal(t, filepath.Join(base, "logs"), LogDir())
	require.Equal(t, filepath.Join(base, "badge-updates"), BadgeUpdatesDir())
}

func TestXDGPaths_LegacyAckSignalFallbackSurvivesSignalConsumption(t *testing.T) {
	home, _ := isolateTmuxXDGPaths(t)

	legacyDir := filepath.Join(home, ".agent-deck")
	legacyAck := filepath.Join(legacyDir, "ack-signal")
	require.NoError(t, os.MkdirAll(legacyDir, 0o700))
	require.NoError(t, os.WriteFile(legacyAck, []byte("session-id"), 0o600))

	require.Equal(t, "session-id", ReadAndClearAckSignal())

	ackPath, err := GetAckSignalPath()
	require.NoError(t, err)
	require.Equal(t, legacyAck, ackPath)
}

// TestQuickSwitchScript_EnsuresAckSignalDir is a regression test for #1327.
//
// The quick-switch bind (Ctrl+b <number>) runs a run-shell script that echoes
// the session ID into the ack-signal file and then `tmux switch-client`s. On
// the XDG layout the ack-signal dir (~/.local/share/agent-deck) may not exist,
// so the echo fails, the `&&` short-circuits, and the switch never happens.
// The bind script must `mkdir -p` the ack-signal dir first so the switch always
// runs. This test fails on main (no mkdir) and passes with the fix.
func TestQuickSwitchScript_EnsuresAckSignalDir(t *testing.T) {
	_, data := isolateTmuxXDGPaths(t)
	signalFile := filepath.Join(data, "agent-deck", "ack-signal")

	script := buildAckSwitchScript(signalFile, "session-123", "agentdeck_demo")

	signalDir := filepath.Dir(signalFile)
	require.Contains(t, script, fmt.Sprintf("mkdir -p '%s'", signalDir),
		"quick-switch script must ensure the ack-signal dir exists before writing (#1327)")

	// The mkdir must precede (guard) the echo so the && chain can't short-circuit
	// the switch-client when the dir is missing.
	require.True(t,
		strings.Index(script, "mkdir -p") < strings.Index(script, "echo "),
		"mkdir must run before echo: %q", script)
	require.Contains(t, script, "tmux switch-client -t 'agentdeck_demo'")
}

func TestXDGPaths_UnrelatedLegacyMarkerDoesNotForceTmuxPaths(t *testing.T) {
	home, data := isolateTmuxXDGPaths(t)
	base := filepath.Join(data, "agent-deck")

	unrelatedLegacy := filepath.Join(home, ".agent-deck", "feedback-state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(unrelatedLegacy), 0o700))
	require.NoError(t, os.WriteFile(unrelatedLegacy, []byte("{}"), 0o600))

	require.Equal(t, filepath.Join(base, "logs"), LogDir())

	ackPath, err := GetAckSignalPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, "ack-signal"), ackPath)

	require.Equal(t, filepath.Join(base, "badge-updates"), BadgeUpdatesDir())
}
