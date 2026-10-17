package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// CORE-CHANGES 13: a remote row's exit_code must survive the forwarding
// round trip (remote list JSON -> RemoteSessionInfo -> local JSON), and a host
// that predates the field must keep it absent instead of reporting 0.
func TestExitStatus1628_RemoteRowForwardsExitCode(t *testing.T) {
	rows, err := parseRemoteSessions([]byte(`[{"id":"a","status":"error","substate":"process-exited","exit_code":3},{"id":"b","status":"idle"}]`))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	var forwarded []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &forwarded))
	require.Equal(t, float64(3), forwarded[0]["exit_code"])
	_, has := forwarded[1]["exit_code"]
	require.False(t, has, "an older host must not grow an exit_code")
}
