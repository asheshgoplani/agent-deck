package statedb

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

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

// TestClaudeStatuslineLimitsMarkExpired pins the read-time marker: a window
// whose resets_at has passed is flagged expired, a live or reset-less window
// is not, and a nil block is a no-op.
func TestClaudeStatuslineLimitsMarkExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(sec int64) *int64 { return &sec }
	pct := func(v float64) *float64 { return &v }
	cases := []struct {
		name     string
		resetsAt *int64
		want     bool
	}{
		{"no reset reported", nil, false},
		{"resets in the future", at(now.Unix() + 60), false},
		{"resets exactly now", at(now.Unix()), true},
		{"reset in the past", at(now.Unix() - 60), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limits := &ClaudeStatuslineLimits{SevenDay: &ClaudeStatuslineWindow{UsedPercentage: pct(100), ResetsAt: tc.resetsAt}}
			limits.MarkExpired(now)
			if limits.SevenDay.Expired != tc.want {
				t.Fatalf("Expired = %v, want %v", limits.SevenDay.Expired, tc.want)
			}
			if limits.FiveHour != nil {
				t.Fatalf("an absent window must stay absent")
			}
		})
	}
	var none *ClaudeStatuslineLimits
	none.MarkExpired(now)
}
