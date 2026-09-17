package session

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Remote parity walk on g14 (2026-09-18), finding 1: the TUI's New Session
// dialog bound to a remote ran `add` then `session start`, and when the start
// reported the pane died at once it DELETED the session as compensation, so
// the row flashed "1 idle" and vanished with nothing to inspect. The CLI path
// (`remote <r> add` + `session start`) leaves the remote's own error record
// with its spawn_failure explainer. The create path must keep that record
// and hand the failure to the caller so the row can be drawn as an error.

// remoteStartSpawnFailureJSON is what a #2099-era remote's `session start
// --json` prints (exit 1) when the pane dies before it can be observed.
const remoteStartSpawnFailureJSON = `{
  "code": "INVALID_OPERATION",
  "error": "failed to start session: tmux session \"agentdeck_parity_1\" is gone: tool not found on PATH: claude (searched: /usr/bin:/bin) (exited after 261ms)",
  "id": "parity-1",
  "reason": "tool not found on PATH: claude (searched: /usr/bin:/bin)",
  "spawn_failure": {
    "command": "export AGENTDECK_INSTANCE_ID=parity-1; exec claude --session-id 1",
    "dying_output": "",
    "elapsed_ms": 261,
    "reason": "tool not found on PATH: claude (searched: /usr/bin:/bin)",
    "ts": 1789685743
  },
  "success": false,
  "title": "parity-1789685743",
  "tmux": "agentdeck_parity_1"
}`

func newSpawnFailureCreateRunner(t *testing.T, startOutput string, startErr error) (*SSHRunner, *[][]string) {
	t.Helper()
	var calls [][]string
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"add", "--capabilities", "--json"}) {
				return json.Marshal(creationTestCatalog())
			}
			calls = append(calls, append([]string(nil), args...))
			switch {
			case len(args) > 0 && args[0] == "add":
				return []byte(`{"id":"parity-1","title":"parity-1789685743"}`), nil
			case len(args) >= 2 && args[0] == "session" && args[1] == "start":
				return []byte(startOutput), startErr
			case len(args) > 0 && args[0] == "remove":
				return []byte(`{"success":true}`), nil
			}
			return nil, errors.New("unexpected runner call")
		},
	}
	return runner, &calls
}

func TestCreateSessionWithOptions_StartSpawnFailureKeepsRecord(t *testing.T) {
	runner, calls := newSpawnFailureCreateRunner(t, remoteStartSpawnFailureJSON,
		errors.New("ssh command failed: exit status 1: "+remoteStartSpawnFailureJSON))

	id, err := runner.CreateSessionWithOptions(context.Background(), RemoteAddOptions{Tool: "claude", Title: "parity-1789685743", Path: "/tmp"})
	require.Error(t, err)
	assert.Equal(t, "", id, "a failed start is not an attachable id")

	var spawnFailed *RemoteSessionSpawnFailedError
	require.ErrorAs(t, err, &spawnFailed, "the failure must be typed so the caller can draw the error row: %v", err)
	assert.Equal(t, "parity-1", spawnFailed.ID)
	assert.Equal(t, "parity-1789685743", spawnFailed.Title)
	assert.Equal(t, "tool not found on PATH: claude (searched: /usr/bin:/bin)", spawnFailed.Reason)
	require.NotNil(t, spawnFailed.Record)
	assert.Equal(t, int64(261), spawnFailed.Record.ElapsedMs)
	assert.Equal(t, "export AGENTDECK_INSTANCE_ID=parity-1; exec claude --session-id 1", spawnFailed.Record.Command)
	assert.Contains(t, spawnFailed.Error(), "parity-1789685743")
	assert.Contains(t, spawnFailed.Error(), "tool not found on PATH: claude")
	assert.Contains(t, spawnFailed.Preview(), "session failed to start")
	assert.Contains(t, spawnFailed.Preview(), "not found on PATH: claude")

	for _, call := range *calls {
		assert.NotEqual(t, "remove", call[0], "the remote's error record must survive, as it does after `remote add` + `session start`; calls: %v", *calls)
	}
}

// A remote whose `session start` fails without the #2099 shape (no reason,
// no spawn_failure) cannot say whether it kept anything: the create is
// rolled back and the plain error returned, exactly as before, so the TUI
// never draws a row the remote does not have.
func TestCreateSessionWithOptions_StartFailureOldShapeRollsBack(t *testing.T) {
	for name, output := range map[string]string{
		"legacy-json": `{"success":false,"error":"failed to start session: tmux: server exited unexpectedly","code":"INVALID_OPERATION"}`,
		"plain-text":  "Error: failed to start session: tmux: server exited unexpectedly\n",
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			runner, calls := newSpawnFailureCreateRunner(t, output, errors.New("ssh command failed: exit status 1: "+strings.TrimSpace(output)))

			_, err := runner.CreateSessionWithOptions(context.Background(), RemoteAddOptions{Tool: "claude"})
			require.Error(t, err)
			var spawnFailed *RemoteSessionSpawnFailedError
			assert.False(t, errors.As(err, &spawnFailed), "old shape must not be promoted to a kept record: %v", err)
			assert.Contains(t, err.Error(), "failed to start remote session")

			removed := false
			for _, call := range *calls {
				if call[0] == "remove" && call[1] == "parity-1" {
					removed = true
				}
			}
			assert.True(t, removed, "old-shape failure keeps the rollback; calls: %v", *calls)
		})
	}
}
