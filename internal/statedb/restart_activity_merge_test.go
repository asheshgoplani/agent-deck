package statedb

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestartSaveMergesConcurrentLastActivity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mine, theirs int64
		want         int64
	}{
		{"monitor newer", 1999, 2000, 2000},
		{"restart newer", 2001, 2000, 2001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, base := launchRaceFixture(t)
			_, err := db.DB().Exec(`UPDATE instances SET tool_data = json_set(tool_data, '$.last_activity_at', ?) WHERE id = 'sess'`, tc.theirs)
			require.NoError(t, err)
			desired := CloneInstanceRow(base)
			desired.TmuxSession = "agentdeck_restarted"
			data := map[string]any{"loaded_mcp_names": []string{"neo4j"}, "last_activity_at": tc.mine}
			desired.ToolData, err = json.Marshal(data)
			require.NoError(t, err)
			merged, err := db.MergeInstanceSnapshots([]InstanceSnapshot{
				{Original: base, Stored: base, Desired: desired},
			}, nil)
			require.NoError(t, err)
			require.Equal(t, "agentdeck_restarted", merged[0].TmuxSession)
			var saved map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(merged[0].ToolData, &saved))
			var at int64
			require.NoError(t, json.Unmarshal(saved["last_activity_at"], &at))
			require.Equal(t, tc.want, at)
		})
	}
}

func TestRestartActivityClearAndMalformedStillConflict(t *testing.T) {
	for _, value := range []string{"0", "null", `"not-a-timestamp"`, "-1"} {
		t.Run(value, func(t *testing.T) {
			db, base := launchRaceFixture(t)
			_, err := db.DB().Exec(`UPDATE instances SET tool_data = json_set(tool_data, '$.last_activity_at', 2000) WHERE id = 'sess'`)
			require.NoError(t, err)
			desired := CloneInstanceRow(base)
			desired.ToolData = json.RawMessage(`{"last_activity_at":` + value + `}`)
			_, err = db.MergeInstanceSnapshots([]InstanceSnapshot{
				{Original: base, Stored: base, Desired: desired},
			}, nil)
			require.ErrorContains(t, err, "tool_data.last_activity_at conflict")
		})
	}
}
