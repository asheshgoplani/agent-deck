package statedb

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeStatuslineUnknownBound(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 140; i++ {
		id, err := db.SaveClaudeStatusline(ClaudeStatusline{ClaudeSessionID: fmt.Sprintf("native-%d", i)})
		require.NoError(t, err)
		require.Empty(t, id)
	}
	var n int
	require.NoError(t, db.DB().QueryRow(`SELECT count(*) FROM claude_statuslines`).Scan(&n))
	require.Equal(t, 128, n)
	_, err := db.SaveClaudeStatusline(ClaudeStatusline{ClaudeSessionID: "native-139", Cwd: "/latest"})
	require.NoError(t, err)
	require.NoError(t, db.DB().QueryRow(`SELECT count(*) FROM claude_statuslines`).Scan(&n))
	require.Equal(t, 128, n)
}

func TestClaudeStatuslineSurvivesSessionSave(t *testing.T) {
	db := newTestDB(t)
	row := &InstanceRow{ID: "deck", Title: "Claude", Tool: "claude", ToolData: json.RawMessage(`{"claude_session_id":"native"}`)}
	require.NoError(t, db.SaveInstance(row))
	id, err := db.SaveClaudeStatusline(ClaudeStatusline{ClaudeSessionID: "native", Cwd: "/project"})
	require.NoError(t, err)
	require.Equal(t, "deck", id)
	require.NoError(t, db.SaveInstance(row))
	require.NoError(t, db.SaveInstances([]*InstanceRow{row}))
	record, err := db.ClaudeStatuslineForSession("deck")
	require.NoError(t, err)
	require.Equal(t, "/project", record.Cwd)
	require.NoError(t, db.ClearAllInstances())
	var n int
	require.NoError(t, db.DB().QueryRow(`SELECT count(*) FROM claude_statuslines`).Scan(&n))
	require.Zero(t, n)
}

func TestClaudeStatuslineAmbiguousNativeID(t *testing.T) {
	db := newTestDB(t)
	for _, id := range []string{"first", "second"} {
		require.NoError(t, db.SaveInstance(&InstanceRow{ID: id, Title: id, Tool: "claude", ToolData: json.RawMessage(`{"claude_session_id":"shared"}`)}))
	}
	id, err := db.SaveClaudeStatusline(ClaudeStatusline{ClaudeSessionID: "shared"})
	require.NoError(t, err)
	require.Empty(t, id)
	_, err = db.ClaudeStatuslineForSession("first")
	require.Error(t, err)
}
