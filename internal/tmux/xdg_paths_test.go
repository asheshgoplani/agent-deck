package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
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
	// The mkdir must reference the (shell-escaped) signal dir and create it 0700
	// so the ack-signal dir is not exposed to other local users (P2).
	require.Contains(t, script, "mkdir -p -m 700 "+shellescape.Quote(signalDir),
		"quick-switch script must ensure the ack-signal dir exists 0700 before writing (#1327)")

	// The mkdir must precede (guard) the echo so the && chain can't short-circuit
	// the switch-client when the dir is missing.
	require.True(t,
		strings.Index(script, "mkdir -p") < strings.Index(script, "echo "),
		"mkdir must run before echo: %q", script)
	require.Contains(t, script, "tmux switch-client -t "+shellescape.Quote("agentdeck_demo"))
}

// TestQuickSwitchScript_EscapesSingleQuotes guards against the shell-quoting /
// injection hazard found in dual-review: targetSession (and the signal paths)
// derive from the user-controlled session title, so a title containing a single
// quote must be safely escaped rather than breaking out of the quoting.
func TestQuickSwitchScript_EscapesSingleQuotes(t *testing.T) {
	_, data := isolateTmuxXDGPaths(t)
	// A path segment and a session name that both contain a single quote.
	signalFile := filepath.Join(data, "agent-deck", "it's-data", "ack-signal")
	sessionID := "id's-123"
	targetSession := "agentdeck_it's-a-test"

	script := buildAckSwitchScript(signalFile, sessionID, targetSession)

	// Every interpolated value must appear as its shellescape.Quote'd form.
	// shellescape wraps single-quote-containing values as e.g. 'it'"'"'s-...'
	// which is a single safe shell word — the bare quote never appears unescaped.
	require.Contains(t, script, shellescape.Quote(filepath.Dir(signalFile)),
		"signal dir must be shell-escaped: %q", script)
	require.Contains(t, script, shellescape.Quote(sessionID),
		"session ID must be shell-escaped: %q", script)
	require.Contains(t, script, shellescape.Quote(signalFile),
		"signal file must be shell-escaped: %q", script)
	require.Contains(t, script, shellescape.Quote(targetSession),
		"target session must be shell-escaped: %q", script)

	// Sanity: the dangerous "broken-out" pattern from raw '%s' interpolation —
	// a bare single quote immediately followed by shell-significant text — must
	// not appear. With shellescape the only quotes are part of '"'"' sequences.
	require.NotContains(t, script, "'it's-a-test'",
		"single quote in session title must not break out of quoting: %q", script)

	// mkdir-before-echo guard still holds even with special characters.
	require.True(t,
		strings.Index(script, "mkdir -p") < strings.Index(script, "echo "),
		"mkdir must run before echo even with quoted values: %q", script)
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
