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

// A headless poller (notify-daemon, `web --no-tui`) ticks every
// check_interval and each tick asks CheckForUpdate. The cache it wrote on
// the previous tick is stamped a moment AFTER that tick (the network round
// trip plus the `gh auth token` lookup), so on the next tick it is a little
// younger than check_interval. Without a margin the cache still counts as
// fresh, the tick is a cache hit, and the poller really hits GitHub only
// every second tick: a release is noticed up to 2x check_interval after it
// is published instead of within one check_interval.

// cacheAgedBy writes a cache for v1.7.58 whose CheckedAt is age ago.
func cacheAgedBy(t *testing.T, age time.Duration) {
	t.Helper()
	require.NoError(t, saveCache(&UpdateCache{
		CheckedAt:      time.Now().Add(-age),
		LatestVersion:  "1.7.58",
		CurrentVersion: "1.7.1",
		ReleaseURL:     "https://example/releases/v1.7.58",
		ETag:           `"v58"`,
	}))
}

// phaseServer serves v1.7.59 (a release published after the cache was
// written) and counts /releases/latest requests.
func phaseServer(t *testing.T) *int {
	t.Helper()
	gets := 0
	newRelease := Release{TagName: "v1.7.59", HTMLURL: "https://example/releases/v1.7.59"}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		gets++
		w.Header().Set("ETag", `"v59"`)
		_ = json.NewEncoder(w).Encode(newRelease)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Release{newRelease})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })
	return &gets
}

func withCheckInterval(t *testing.T, d time.Duration) {
	t.Helper()
	orig := checkInterval
	checkInterval = d
	t.Cleanup(func() { checkInterval = orig })
}

func TestCheckForUpdate_TickAtCheckIntervalPollsDespiteWriteLag(t *testing.T) {
	withUpdateChecksEnabled(t)
	isolateUpdatePaths(t)
	withCheckInterval(t, 90*time.Second)
	gets := phaseServer(t)

	// The previous tick's cache, as the next 90s tick sees it: written
	// 0.5s after that tick, so 89.5s old now.
	cacheAgedBy(t, 90*time.Second-500*time.Millisecond)

	info, err := CheckForUpdate("1.7.1", false)
	require.NoError(t, err)
	assert.Equal(t, 1, *gets, "a tick one check_interval after the last poll must poll GitHub, not serve a cache that is only younger by the write lag")
	assert.Equal(t, "1.7.59", info.LatestVersion, "the release published since the last poll is seen on this tick")
}

func TestCheckForUpdate_YoungCacheStillServedWithoutNetwork(t *testing.T) {
	withUpdateChecksEnabled(t)
	isolateUpdatePaths(t)
	withCheckInterval(t, 90*time.Second)
	gets := phaseServer(t)

	// Another process on the box polled 30s ago: that answer is shared and
	// no request is made (the margin must not turn caching off).
	cacheAgedBy(t, 30*time.Second)

	info, err := CheckForUpdate("1.7.1", false)
	require.NoError(t, err)
	assert.Equal(t, 0, *gets, "a cache well inside check_interval is served from disk")
	assert.Equal(t, "1.7.58", info.LatestVersion)
}

func TestCacheFreshFor(t *testing.T) {
	cases := []struct {
		interval, want time.Duration
	}{
		{90 * time.Second, 81 * time.Second}, // default check_interval: 10% margin
		{10 * time.Second, 9 * time.Second},
		{time.Hour, time.Hour - 10*time.Second}, // margin capped at 10s
		{24 * time.Hour, 24*time.Hour - 10*time.Second},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, cacheFreshFor(c.interval), "interval %s", c.interval)
	}
}
