package feedback_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/stretchr/testify/require"
)

func isolateFeedbackPaths(t *testing.T) (home string, data string) {
	t.Helper()

	root := t.TempDir()
	home = filepath.Join(root, "home")
	data = filepath.Join(root, "xdg-data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg-cache"))
	return home, data
}

func TestStatePath_XDGDataHomeSaveNewUser(t *testing.T) {
	home, data := isolateFeedbackPaths(t)

	state := &feedback.State{
		LastRatedVersion: "1.2.3",
		FeedbackEnabled:  true,
		ShownCount:       1,
		MaxShows:         3,
	}
	require.NoError(t, feedback.SaveState(state))

	xdgPath := filepath.Join(data, "agent-deck", "feedback-state.json")
	legacyPath := filepath.Join(home, ".agent-deck", "feedback-state.json")

	require.FileExists(t, xdgPath)
	require.NoFileExists(t, legacyPath)

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.Equal(t, "1.2.3", loaded.LastRatedVersion)
	require.True(t, loaded.FeedbackEnabled)
	require.Equal(t, 1, loaded.ShownCount)
	require.Equal(t, 3, loaded.MaxShows)
}

func TestStatePath_LegacyFallbackLoadsWhenXDGAbsent(t *testing.T) {
	home, data := isolateFeedbackPaths(t)

	legacyPath := filepath.Join(home, ".agent-deck", "feedback-state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o700))
	require.NoError(t, os.WriteFile(legacyPath, []byte(`{
  "last_rated_version": "0.9.0",
  "feedback_enabled": false,
  "shown_count": 2,
  "max_shows": 3
}`), 0o600))

	require.NoFileExists(t, filepath.Join(data, "agent-deck", "feedback-state.json"))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.Equal(t, "0.9.0", loaded.LastRatedVersion)
	require.False(t, loaded.FeedbackEnabled)
	require.Equal(t, 2, loaded.ShownCount)
	require.Equal(t, 3, loaded.MaxShows)
}
