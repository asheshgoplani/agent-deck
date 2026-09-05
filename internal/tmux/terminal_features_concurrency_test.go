package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The captured state is a deterministic read/apply barrier: another
// real tmux client completes its write before the planned configuration runs.
func TestTerminalFeatures_UserAppendAfterReadSurvives(t *testing.T) {
	socket, _ := startPrivateTmuxServer(t)
	before := readTerminalFeatures(socket)
	require.NotEmpty(t, terminalFeatureArgsFor(before))

	const userEntry = "concurrent-user*:RGB"
	require.NoError(t, tmuxExec(socket, "set", "-as", "terminal-features", ","+userEntry).Run())
	ensureTerminalFeature(socket, before)

	after := readTerminalFeatures(socket)
	require.True(t, after.known)
	assert.Contains(t, after.values, userEntry, "a separate client's completed append must survive")
	assert.Equal(t, 1, countTerminalFeature(after.values))
}

func TestTerminalFeatures_UserReplacementAfterReadSurvives(t *testing.T) {
	socket, _ := startPrivateTmuxServer(t)
	require.NoError(t, tmuxExec(socket, "set", "-as", "terminal-features",
		","+strings.Repeat(agentDeckTerminalFeature+",", 3)+agentDeckTerminalFeature).Run())
	indexed, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
	require.NoError(t, err)
	script := terminalFeatureCleanupScript(indexed)
	require.NotEmpty(t, script)

	const userEntry = "replacement-user*:RGB"
	require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features", userEntry).Run())
	require.NoError(t, runTerminalFeatureCleanup(socket, script))

	after := readTerminalFeatures(socket)
	require.True(t, after.known)
	assert.Contains(t, after.values, userEntry, "cleanup must not replace a newer array with its stale snapshot")
	for _, value := range after.values {
		assert.True(t, value == userEntry || value == agentDeckTerminalFeature,
			"an entry removed by the user was resurrected: %q", value)
	}
	assert.LessOrEqual(t, countTerminalFeature(after.values), 1)
}

func TestTerminalFeatures_ConcurrentInitializersDoNotDuplicate(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		name := "ordinary"
		if unsafe {
			name = "comma-valued-user-entry"
		}
		t.Run(name, func(t *testing.T) {
			socket, _ := startPrivateTmuxServer(t)
			if unsafe {
				require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features[9]", "foo,bar").Run())
			}
			before := readTerminalFeatures(socket)
			require.True(t, before.known)
			first := readTerminalFeatures(socket)
			second := readTerminalFeatures(socket)
			require.NotEmpty(t, terminalFeatureArgsFor(first))
			require.NotEmpty(t, terminalFeatureArgsFor(second))

			// Both clients have finished reading before either is allowed to
			// apply. Sequential application is itself a valid concurrent-read
			// interleaving and makes the missing-membership-check failure exact.
			ensureTerminalFeature(socket, first)
			ensureTerminalFeature(socket, second)
			after := readTerminalFeatures(socket)
			require.True(t, after.known)
			assert.Equal(t, 1, countTerminalFeature(after.values))
			var foreignAfter []string
			for _, value := range after.values {
				if value != agentDeckTerminalFeature {
					foreignAfter = append(foreignAfter, value)
				}
			}
			assert.Equal(t, before.values, foreignAfter)
			if unsafe {
				value, err := runBoundedOutput(socket, "show-options", "-sv", "terminal-features[9]")
				require.NoError(t, err)
				assert.Equal(t, "foo,bar\n", string(value), "sparse foreign index must not move")
			}
		})
	}
}

func TestTerminalFeatures_IndexedCleanupPreservesConcurrentChanges(t *testing.T) {
	for _, change := range []string{"candidate-replaced", "keeper-replaced", "keeper-removed"} {
		t.Run(change, func(t *testing.T) {
			socket, _ := startPrivateTmuxServer(t)
			require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features", "").Run())
			for _, index := range []int{2, 7} {
				require.NoError(t, tmuxExec(socket, "set", "-s", fmt.Sprintf("terminal-features[%d]", index), agentDeckTerminalFeature).Run())
			}
			indexed, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
			require.NoError(t, err)
			script := terminalFeatureCleanupScript(indexed)
			require.NotEmpty(t, script)

			const replacement = "other-client*:RGB"
			switch change {
			case "candidate-replaced":
				require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features[7]", replacement).Run())
			case "keeper-replaced":
				require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features[2]", replacement).Run())
			case "keeper-removed":
				require.NoError(t, tmuxExec(socket, "set", "-su", "terminal-features[2]").Run())
			}
			require.NoError(t, runTerminalFeatureCleanup(socket, script))
			after := readTerminalFeatures(socket)
			require.True(t, after.known)
			assert.Equal(t, 1, countTerminalFeature(after.values), "cleanup must retain the remaining owned entry")
			if change != "keeper-removed" {
				assert.Contains(t, after.values, replacement)
			}
		})
	}
}

func TestTerminalFeatures_IndexedCleanupNeverCopiesForeignValues(t *testing.T) {
	socket, _ := startPrivateTmuxServer(t)
	require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features", "").Run())
	foreign := []string{"foo,bar", "user value with spaces", "quotes '\" #{pid} ; literal", "line1\nline2", ""}
	for i, value := range foreign {
		require.NoError(t, tmuxExec(socket, "set", "-s", fmt.Sprintf("terminal-features[%d]", 20+i), value).Run())
	}
	for _, index := range []int{2, 7} {
		require.NoError(t, tmuxExec(socket, "set", "-s", fmt.Sprintf("terminal-features[%d]", index), agentDeckTerminalFeature).Run())
	}
	indexed, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
	require.NoError(t, err)
	script := terminalFeatureCleanupScript(indexed)
	require.NoError(t, runTerminalFeatureCleanup(socket, script))
	for i, value := range foreign {
		got, err := runBoundedOutput(socket, "show-options", "-sv", fmt.Sprintf("terminal-features[%d]", 20+i))
		require.NoError(t, err)
		assert.Equal(t, value+"\n", string(got), "foreign index %d changed", 20+i)
	}
	assert.Equal(t, 1, countTerminalFeature(readTerminalFeatures(socket).values))
}

func TestTerminalFeatures_EmbeddedOwnedTextPreservesForeignValue(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)
	value := "user " + agentDeckTerminalFeature + " tail"
	require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features[9]", value).Run())
	before := readTerminalFeatures(socket)
	require.True(t, before.known)
	require.NoError(t, (&Session{Name: session, SocketName: socket, mouse: true}).EnableMouseMode())
	after := readTerminalFeatures(socket)
	require.True(t, after.known)
	assert.Equal(t, append(before.values, agentDeckTerminalFeature), after.values)
	assert.Equal(t, 1, countTerminalFeature(after.values), "embedded text is not an exact owned entry")
	got, err := runBoundedOutput(socket, "show-options", "-sv", "terminal-features[9]")
	require.NoError(t, err)
	assert.Equal(t, value+"\n", string(got))
}

func TestTerminalFeatures_FiveThousandDuplicatesUseBoundedStdin(t *testing.T) {
	socket, _ := startPrivateTmuxServer(t)
	const userEntry = "preserved-user*:RGB"
	// tmux limits a client's command message, so seed the large array over
	// stdin as well. The seed uses only these fixed test literals.
	var seed strings.Builder
	fmt.Fprintf(&seed, "set-option -s terminal-features %s\n", userEntry)
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&seed, "set-option -as terminal-features ,%s\n", agentDeckTerminalFeature)
	}
	cmd := tmuxExec(socket, "source-file", "-")
	cmd.Stdin = strings.NewReader(seed.String())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	indexed, err := runBoundedOutput(socket, "show-options", "-s", "terminal-features")
	require.NoError(t, err)
	script := terminalFeatureCleanupScript(indexed)
	require.Equal(t, 4999, strings.Count(script, "\n"))
	started := time.Now()
	require.NoError(t, runTerminalFeatureCleanup(socket, script), "cleanup must finish within the existing subprocess deadline")
	t.Logf("5000-entry cleanup: stdin_bytes=%d elapsed=%s", len(script), time.Since(started))
	after := readTerminalFeatures(socket)
	require.True(t, after.known)
	assert.Equal(t, []string{userEntry, agentDeckTerminalFeature}, after.values)
}
