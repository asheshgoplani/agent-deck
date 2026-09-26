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

// Near-event-driven polling: a short check_interval only stays cheap if an
// unchanged release answers 304 Not Modified to a conditional (ETag)
// request instead of re-sending (and re-billing the rate limit for) the
// full release body every time.

// expireCache backdates the on-disk cache's CheckedAt so the next
// CheckForUpdate(forceCheck=false) treats it as stale and polls live,
// deterministically — no sleeping past checkInterval and hoping the timing
// works out under CI load.
func expireCache(t *testing.T) {
	t.Helper()
	cache, err := loadCache()
	require.NoError(t, err)
	cache.CheckedAt = time.Now().Add(-24 * time.Hour)
	require.NoError(t, saveCache(cache))
}

func TestCheckForUpdate_ConditionalRequestReturns304BetweenReleases(t *testing.T) {
	withUpdateChecksEnabled(t)
	isolateUpdatePaths(t)

	const etag = `"deadbeef"`
	release := Release{TagName: "v1.7.58", HTMLURL: "https://example/releases/v1.7.58"}

	var gets, conditionalGets, notModified int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		gets++
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			conditionalGets++
			notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(release)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Release{release})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })

	// First poll: no ETag cached yet, GitHub answers 200.
	first, err := CheckForUpdate("1.7.1", true)
	require.NoError(t, err)
	assert.True(t, first.Available)
	assert.Equal(t, "1.7.58", first.LatestVersion)
	assert.Equal(t, 1, gets)
	assert.Equal(t, 0, conditionalGets, "first poll has no ETag to send")

	// Once the cache is stale, the poll goes live again: this time the
	// cached ETag rides along as If-None-Match, and the release hasn't
	// changed, so GitHub answers 304.
	expireCache(t)
	second, err := CheckForUpdate("1.7.1", false)
	require.NoError(t, err)
	assert.True(t, second.Available)
	assert.Equal(t, "1.7.58", second.LatestVersion, "a 304 must keep serving the last known release")
	assert.Equal(t, 1, second.ReleasesBehind, "a 304 must keep the cached releases-behind count")

	// A third poll, again once the cache is stale, should also come back
	// 304: nothing has changed on GitHub between releases.
	expireCache(t)
	third, err := CheckForUpdate("1.7.1", false)
	require.NoError(t, err)
	assert.Equal(t, "1.7.58", third.LatestVersion)

	assert.Equal(t, 2, notModified, "polls 2 and 3 both got 304")
	assert.Equal(t, 2, conditionalGets, "polls 2 and 3 both sent the cached ETag")
	assert.Equal(t, 3, gets, "one 200 + two 304s: three requests total, all cheap after the first")
}

func TestCheckForUpdate_NewReleaseAfter304sIsPickedUpOnNextChange(t *testing.T) {
	withUpdateChecksEnabled(t)
	isolateUpdatePaths(t)

	oldRelease := Release{TagName: "v1.7.58", HTMLURL: "https://example/releases/v1.7.58"}
	newRelease := Release{TagName: "v1.7.59", HTMLURL: "https://example/releases/v1.7.59"}
	current := &oldRelease
	etag := `"v58"`

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Release{*current})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })

	first, err := CheckForUpdate("1.7.1", true)
	require.NoError(t, err)
	assert.Equal(t, "1.7.58", first.LatestVersion)

	// A new release lands and the ETag changes with it.
	current = &newRelease
	etag = `"v59"`
	expireCache(t)

	second, err := CheckForUpdate("1.7.1", false)
	require.NoError(t, err)
	assert.Equal(t, "1.7.59", second.LatestVersion, "a changed ETag must be picked up as a fresh 200, not served from the stale cache")
}
