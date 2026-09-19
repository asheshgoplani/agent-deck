package quota

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// windowByKind is a test helper: the model stores windows as a slice (unknown
// provider windows must stay representable), so assertions look them up.
func windowByKind(t *testing.T, snapshot Snapshot, kind WindowKind) Window {
	t.Helper()
	for _, window := range snapshot.Windows {
		if window.Kind == kind {
			return window
		}
	}
	t.Fatalf("window %q not present in %+v", kind, snapshot.Windows)
	return Window{}
}

func TestParseStatusLineWindows(t *testing.T) {
	const payload = `{
	  "session_id": "abc-123",
	  "transcript_path": "/home/someone/.claude/projects/x/abc-123.jsonl",
	  "cwd": "/home/someone/secret-project",
	  "model": {"id": "claude-opus-5", "display_name": "Opus"},
	  "rate_limits": {
	    "five_hour":   {"used_percentage": 23.5, "resets_at": 1738425600},
	    "seven_day":   {"used_percentage": 41.2, "resets_at": 1738857600},
	    "spend_limit": {"used_percentage": 162.8, "resets_at": 1740787200}
	  }
	}`

	snapshot, ok, err := ParseStatusLine(strings.NewReader(payload))
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, ProviderClaude, snapshot.ID)
	require.Len(t, snapshot.Windows, 3)

	fiveHour := windowByKind(t, snapshot, WindowFiveHour)
	// The float must survive: 23.5 rounded to 23 is a different claim about
	// how much of the window is gone.
	assert.Equal(t, 23.5, fiveHour.UsedPercentage)
	require.NotNil(t, fiveHour.ResetsAt)
	assert.Equal(t, int64(1738425600), *fiveHour.ResetsAt)

	sevenDay := windowByKind(t, snapshot, WindowSevenDay)
	assert.Equal(t, 41.2, sevenDay.UsedPercentage)

	// spend_limit legitimately exceeds 100 once the account is in overage.
	// Clamping it would report a breached limit as an exactly-met one.
	spend := windowByKind(t, snapshot, WindowSpendLimit)
	assert.Equal(t, 162.8, spend.UsedPercentage)
}

func TestParseStatusLineAbsentRateLimits(t *testing.T) {
	// The normal state before the session's first API response, and the
	// permanent state for API-key / Bedrock / Vertex auth. Not an error.
	tests := []struct {
		name    string
		payload string
	}{
		{"no rate_limits key", `{"session_id":"abc","model":{"id":"claude-opus-5"}}`},
		{"rate_limits null", `{"rate_limits": null}`},
		{"rate_limits empty", `{"rate_limits": {}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, ok, err := ParseStatusLine(strings.NewReader(tt.payload))
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Empty(t, snapshot.Windows)
		})
	}
}

func TestParseStatusLineSingleWindowInventsNothing(t *testing.T) {
	// Claude drops a window from the block once its reset passes.
	const payload = `{"rate_limits": {"five_hour": {"used_percentage": 7, "resets_at": 1738425600}}}`

	snapshot, ok, err := ParseStatusLine(strings.NewReader(payload))
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, snapshot.Windows, 1)
	assert.Equal(t, WindowFiveHour, snapshot.Windows[0].Kind)
}

func TestParseStatusLineMissingResetIsNil(t *testing.T) {
	// nil means "the provider did not tell us". Zero would mean 1970 and
	// would render as a window that reset long ago.
	const payload = `{"rate_limits": {"five_hour": {"used_percentage": 7}}}`

	snapshot, ok, err := ParseStatusLine(strings.NewReader(payload))
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, snapshot.Windows, 1)
	assert.Nil(t, snapshot.Windows[0].ResetsAt)
}

func TestParseStatusLineKeepsOnlyRateLimits(t *testing.T) {
	// The statusLine payload carries the user's cwd, transcript path and
	// prompt. None of it may reach a file agent-deck writes.
	const payload = `{
	  "session_id": "SENTINEL-SESSION",
	  "transcript_path": "/home/someone/SENTINEL-TRANSCRIPT.jsonl",
	  "cwd": "/home/someone/SENTINEL-CWD",
	  "model": {"id": "SENTINEL-MODEL"},
	  "rate_limits": {"five_hour": {"used_percentage": 10, "resets_at": 1738425600}}
	}`

	snapshot, ok, err := ParseStatusLine(strings.NewReader(payload))
	require.NoError(t, err)
	require.True(t, ok)

	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	for _, sentinel := range []string{"SENTINEL-SESSION", "SENTINEL-TRANSCRIPT", "SENTINEL-CWD", "SENTINEL-MODEL"} {
		assert.NotContains(t, string(encoded), sentinel)
	}
}

func TestParseStatusLineMalformed(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"truncated", `{"rate_limits": {"five_hour":`},
		{"not an object", `["five_hour"]`},
		{"empty input", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok, err := ParseStatusLine(strings.NewReader(tt.payload))
			require.Error(t, err)
			assert.False(t, ok)
		})
	}
}

func TestParseStatusLineCapsInput(t *testing.T) {
	// stdin is attacker-adjacent only in the sense that it is whatever the
	// caller pipes in; an unbounded read is still an unbounded allocation.
	oversized := `{"rate_limits": {"five_hour": {"used_percentage": 1}}, "pad": "` +
		strings.Repeat("x", int(maxStatusLineBytes)+1) + `"}`

	_, ok, err := ParseStatusLine(strings.NewReader(oversized))
	require.Error(t, err)
	assert.False(t, ok)
}
