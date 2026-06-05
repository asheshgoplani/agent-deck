package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func isolateUpdatePaths(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg-cache"))
	return home
}

func TestCachePath_XDGCacheHomeRoundtrip(t *testing.T) {
	isolateUpdatePaths(t)

	cache := &UpdateCache{
		CheckedAt:      time.Now().UTC(),
		LatestVersion:  "2.0.0",
		CurrentVersion: "1.0.0",
		DownloadURL:    "https://example.test/download",
		ReleaseURL:     "https://example.test/release",
		ReleasesBehind: 4,
	}

	require.NoError(t, saveCache(cache))

	cachePath := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "agent-deck", CacheFileName)
	require.FileExists(t, cachePath)

	loaded, err := loadCache()
	require.NoError(t, err)
	require.Equal(t, cache.LatestVersion, loaded.LatestVersion)
	require.Equal(t, cache.CurrentVersion, loaded.CurrentVersion)
	require.Equal(t, cache.DownloadURL, loaded.DownloadURL)
	require.Equal(t, cache.ReleaseURL, loaded.ReleaseURL)
	require.Equal(t, cache.ReleasesBehind, loaded.ReleasesBehind)
}
