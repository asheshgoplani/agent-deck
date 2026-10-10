package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// A remote row carries the remote's favourite flag next to its parent and
// creation time, and forwards all three when re-encoded for
// `remote sessions --json`.
func TestRemoteSessionInfoParentAndFavoriteParity(t *testing.T) {
	input := []byte(`[{"id":"worker","parent_session_id":"conductor","favorite":true,"created_at":"2026-10-01T12:00:00Z"}]`)
	rows, err := parseRemoteSessions(input)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "conductor", rows[0].ParentSessionID)
	require.NotNil(t, rows[0].Favorite)
	require.True(t, *rows[0].Favorite)
	require.Equal(t, "2026-10-01T12:00:00Z", rows[0].CreatedAt)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	var forwarded []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &forwarded))
	require.Equal(t, "conductor", forwarded[0]["parent_session_id"])
	require.Equal(t, true, forwarded[0]["favorite"])
	require.Equal(t, "2026-10-01T12:00:00Z", forwarded[0]["created_at"])
}

// An older remote (or a non-favourite row, whose `list --json` omits the
// key) keeps the optional keys absent instead of inventing values.
func TestRemoteSessionInfoOlderHostKeepsOptionalFieldsAbsent(t *testing.T) {
	rows, err := parseRemoteSessions([]byte(`[{"id":"old","created_at":"2026-09-01T12:00:00Z"}]`))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Empty(t, rows[0].ParentSessionID)
	require.Nil(t, rows[0].Favorite)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	var forwarded []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &forwarded))
	_, hasParent := forwarded[0]["parent_session_id"]
	_, hasFavorite := forwarded[0]["favorite"]
	require.False(t, hasParent)
	require.False(t, hasFavorite)
	require.Equal(t, "2026-09-01T12:00:00Z", forwarded[0]["created_at"])
}

// Behavioural check that does not name the Go field: a favourite row from
// the remote's `list --json` must come out of the controller's re-encoding
// (what `remote sessions --json` prints) with `"favorite": true`, while a
// non-favourite row stays without the key.
func TestRemoteSessionsJSONForwardsFavoriteKey(t *testing.T) {
	rows, err := parseRemoteSessions([]byte(`[
	  {"id":"fav","title":"starred","status":"idle","favorite":true},
	  {"id":"plain","title":"plain","status":"idle"}
	]`))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	var forwarded []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &forwarded))
	require.Equal(t, true, forwarded[0]["favorite"], "favourite remote row must forward favorite=true")
	_, plainHasFavorite := forwarded[1]["favorite"]
	require.False(t, plainHasFavorite, "non-favourite row must not gain a favorite key")
}
