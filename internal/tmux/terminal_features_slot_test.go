package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalFeatures_SharedSlotCollisionReportsAbsence(t *testing.T) {
	for _, value := range []string{"foreign*:RGB", "", "head\n" + agentDeckTerminalFeature + "\ntail"} {
		t.Run(fmt.Sprintf("value=%q", value), func(t *testing.T) {
			socket, session := startPrivateTmuxServer(t)
			option := fmt.Sprintf("terminal-features[%d]", agentDeckTerminalFeatureIndex)
			require.NoError(t, tmuxExec(socket, "set", "-s", option, value).Run())
			before, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
			require.NoError(t, err)
			log := captureStatusLog(t)
			sess := &Session{Name: session, SocketName: socket, mouse: true}
			require.NoError(t, sess.EnableMouseMode())
			after, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
			require.NoError(t, err)
			assert.Equal(t, before, after, "no fallback may insert into a different slot")
			assert.Empty(t, terminalFeatureIndices(after), "foreign embedded text is not an owned entry")
			assert.Contains(t, log.String(), "terminal_features_installation_deferred")
			assert.Contains(t, log.String(), "owned_entry_absent_after_attempt")
			assert.NotContains(t, log.String(), "head\\n", "foreign contents must not be logged")

			// The collision is not cached: a later pass can install after the
			// user frees the slot, without changing any other array entry.
			require.NoError(t, tmuxExec(socket, "set", "-su", option).Run())
			require.NoError(t, sess.EnableMouseMode())
			installed, err := runBoundedOutput(socket, "show-options", "-sv", option)
			require.NoError(t, err)
			assert.Equal(t, agentDeckTerminalFeature+"\n", string(installed))
		})
	}
}

func TestTerminalFeatures_SharedSlotLateCollisionIsPreserved(t *testing.T) {
	for _, value := range []string{"late-client*:RGB", ""} {
		t.Run(fmt.Sprintf("value=%q", value), func(t *testing.T) {
			socket, _ := startPrivateTmuxServer(t)
			before := readTerminalFeatures(socket)
			require.True(t, before.known)
			option := fmt.Sprintf("terminal-features[%d]", agentDeckTerminalFeatureIndex)
			require.NoError(t, tmuxExec(socket, "set", "-s", option, value).Run())
			log := captureStatusLog(t)
			ensureTerminalFeature(socket, before)
			got, err := runBoundedOutput(socket, "show-options", "-sv", option)
			require.NoError(t, err)
			assert.Equal(t, value+"\n", string(got))
			assert.Contains(t, log.String(), "terminal_features_installation_deferred")
		})
	}
}

func TestTerminalFeatures_SimultaneousSessionInitializers(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprintf("comma=%t", unsafe), func(t *testing.T) {
			socket, session := startPrivateTmuxServer(t)
			if unsafe {
				require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features[9]", "foo,bar").Run())
			}
			before := readTerminalFeatures(socket)
			require.True(t, before.known)
			const clients = 16
			start := make(chan struct{})
			errors := make(chan error, clients)
			for range clients {
				go func() {
					<-start
					sess := &Session{Name: session, SocketName: socket, mouse: true}
					errors <- sess.EnableMouseMode()
				}()
			}
			close(start)
			for range clients {
				require.NoError(t, <-errors)
			}
			after := readTerminalFeatures(socket)
			require.True(t, after.known)
			assert.Equal(t, append(before.values, agentDeckTerminalFeature), after.values)
			indexed, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
			require.NoError(t, err)
			assert.Equal(t, []int{agentDeckTerminalFeatureIndex}, terminalFeatureIndices(indexed))
		})
	}
}

func TestTerminalFeatures_InstallationReadbackFailureIsUnverified(t *testing.T) {
	socket, _ := startPrivateTmuxServer(t)
	before := readTerminalFeatures(socket)
	require.True(t, before.known)
	log := captureStatusLog(t)
	restore := installPartialReadTmuxShim(t)
	ensureTerminalFeature(socket, before)
	restore()
	assert.Contains(t, log.String(), "terminal_features_installation_unverified")
	assert.NotContains(t, log.String(), "terminal_features_installation_deferred")
	assert.Equal(t, 1, countTerminalFeature(readTerminalFeatures(socket).values))
}

func TestTerminalFeatures_StaleAbsenceOnReplacementServer(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)
	before := readTerminalFeatures(socket)
	require.True(t, before.known)
	firstPID := tmuxServerPID(t, socket)
	require.NoError(t, tmuxExec(socket, "kill-server").Run())
	require.Eventually(t, func() bool {
		return !readTerminalFeatures(socket).known
	}, 5*time.Second, 20*time.Millisecond, "old server did not go away")
	startPrivateTmuxSession(t, socket, session)
	require.NotEqual(t, firstPID, tmuxServerPID(t, socket), "must be a different tmux server")
	for range 2 {
		ensureTerminalFeature(socket, before)
	}
	indexed, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
	require.NoError(t, err)
	assert.Equal(t, []int{agentDeckTerminalFeatureIndex}, terminalFeatureIndices(indexed))
	assert.True(t, strings.HasSuffix(strings.TrimSpace(string(indexed)), agentDeckTerminalFeature))
}
