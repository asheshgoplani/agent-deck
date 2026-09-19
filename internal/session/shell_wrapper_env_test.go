package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A shell session whose wrapper owns the pane (`--wrapper`, no command) is
// not a plain shell: whatever the wrapper runs reads the pane's input, so
// typing the export line into it (ensureInteractiveShellEnv) submits an
// empty line, or the line itself, to that program. TestNativeSSHAttachLifecycle
// saw exactly that as a receipt for zero bytes before the test sent anything.
func TestShellSession_WrapperOwnsPane_NoExportTyped(t *testing.T) {
	skipIfNoTmuxBinary(t)
	home := withTempAgentDeckHome(t, "")
	t.Setenv("AGENTDECK_ACCOUNT", "")
	ClearUserConfigCache()

	received := filepath.Join(home, "received")
	inst := NewInstanceWithTool("wrapped shell", t.TempDir(), "shell")
	// The receiver reads the pane raw and records every byte, so a typed
	// line (or its Enter alone) is evidence. The shell opens the output file
	// only after stty has switched the pane to raw mode, so the file's
	// existence means the wrapper has taken the pane.
	inst.Wrapper = "sh -c 'stty raw -echo; exec cat > " + received + "'"
	t.Cleanup(func() { _ = inst.Kill() })
	require.NoError(t, inst.Start())

	require.Eventually(t, func() bool {
		_, err := os.Stat(received)
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "the wrapper never took the pane")
	// Start() types synchronously, so anything typed is in the pane's input
	// queue by now; give the receiver a moment to drain it.
	time.Sleep(time.Second)
	data, err := os.ReadFile(received)
	require.NoError(t, err)
	assert.Empty(t, string(data), "the pane's program received input agent-deck typed on start")
}
