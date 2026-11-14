package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cached ReleasesBehind count is only true for the version that wrote
// it. After an upgrade (brew upgrade, agent-deck update) the cache still
// says how far the OLD binary was behind, so reusing it makes the startup
// nudge claim "8 releases behind" on a build that is one release behind.

// staleCountServer serves v1.7.58 as latest, eight releases in the listing,
// and answers 304 to a matching If-None-Match.
func staleCountServer(t *testing.T) {
	t.Helper()
	const etag = `"v58"`
	latest := Release{TagName: "v1.7.58", HTMLURL: "https://example/releases/v1.7.58"}
	recent := []Release{
		{TagName: "v1.7.58"}, {TagName: "v1.7.57"}, {TagName: "v1.7.56"}, {TagName: "v1.7.55"},
		{TagName: "v1.7.54"}, {TagName: "v1.7.53"}, {TagName: "v1.7.52"}, {TagName: "v1.7.51"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(latest)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(recent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })
}

func TestCheckForUpdate_FreshCacheFromOlderVersionRecountsReleasesBehind(t *testing.T) {
	withUpdateChecksEnabled(t)
	isolateUpdatePaths(t)
	staleCountServer(t)

	first, err := CheckForUpdate("1.7.50", true)
	require.NoError(t, err)
	require.Equal(t, 8, first.ReleasesBehind)

	// The user upgrades to 1.7.57 while the cache is still fresh.
	info, err := CheckForUpdate("1.7.57", false)
	require.NoError(t, err)
	assert.True(t, info.Available)
	assert.Equal(t, 1, info.ReleasesBehind, "a cache written by 1.7.50 must not supply 1.7.57's releases-behind count")
	assert.False(t, ShouldNudge(info), "one release behind must not nudge")
}

func TestCheckForUpdate_NotModifiedAfterUpgradeRecountsReleasesBehind(t *testing.T) {
	withUpdateChecksEnabled(t)
	isolateUpdatePaths(t)
	staleCountServer(t)

	first, err := CheckForUpdate("1.7.50", true)
	require.NoError(t, err)
	require.Equal(t, 8, first.ReleasesBehind)

	// The user upgrades; the cache has gone stale and GitHub answers 304.
	expireCache(t)
	info, err := CheckForUpdate("1.7.57", false)
	require.NoError(t, err)
	assert.True(t, info.Available)
	assert.Equal(t, 1, info.ReleasesBehind, "a 304 after an upgrade must recount, not reuse the old version's count")

	// The renewed cache must carry the recount too, or the next fresh
	// read (now stamped with 1.7.57) serves the old count again.
	again, err := CheckForUpdate("1.7.57", false)
	require.NoError(t, err)
	assert.Equal(t, 1, again.ReleasesBehind)
}

func TestCachedUpdateInfo_IgnoresReleasesBehindFromOlderVersion(t *testing.T) {
	withUpdateChecksEnabled(t)
	isolateUpdatePaths(t)

	require.NoError(t, saveCache(&UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  "1.7.58",
		CurrentVersion: "1.7.20",
		ReleasesBehind: 30,
	}))

	info, err := CachedUpdateInfo("1.7.57")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.Available)
	assert.Equal(t, 0, info.ReleasesBehind, "1.7.20's count of 30 is not 1.7.57's; offline there is no way to recount")
	assert.False(t, ShouldNudge(info))

	// The "v" prefix does not make the same version look different.
	info, err = CachedUpdateInfo("v1.7.20")
	require.NoError(t, err)
	assert.Equal(t, 30, info.ReleasesBehind)
}
