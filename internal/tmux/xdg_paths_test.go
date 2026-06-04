package tmux

import (
	"os"
	"path/filepath"
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
