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

// Tests for the "N releases behind" nudge added in v1.7.59.
// Spec: conductor task #45. 4 users today posted feedback from versions
// 15-39 releases old. A count-based nudge only fires for severely behind
// users and is dismissible for the session.

func TestCountReleasesBehind_Basic(t *testing.T) {
	releases := []Release{
		{TagName: "v1.7.58"},
		{TagName: "v1.7.57"},
		{TagName: "v1.7.56"},
		{TagName: "v1.7.54"},
		{TagName: "v1.7.53"},
		{TagName: "v1.7.52"},
		{TagName: "v1.7.51"},
		{TagName: "v1.7.50"},
	}

	tests := []struct {
		name    string
		current string
		want    int
	}{
		{"six behind", "1.7.51", 6},      // 52, 53, 54, 56, 57, 58
		{"two behind", "1.7.56", 2},      // 57, 58
		{"up to date", "1.7.58", 0},      // nothing newer
		{"ahead of latest", "1.7.99", 0}, // all older — clamped at 0
		{"with v prefix", "v1.7.57", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountReleasesBehind(tt.current, releases)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShouldNudge_ThresholdIsGreaterThanFive(t *testing.T) {
	tests := []struct {
		name string
		info *UpdateInfo
		want bool
	}{
		{"nil info", nil, false},
		{"not available", &UpdateInfo{Available: false, ReleasesBehind: 99}, false},
		{"exactly 5 behind — below threshold", &UpdateInfo{Available: true, ReleasesBehind: 5}, false},
		{"6 behind — nudge", &UpdateInfo{Available: true, ReleasesBehind: 6}, true},
		{"40 behind — nudge", &UpdateInfo{Available: true, ReleasesBehind: 40}, true},
		{"1 behind — suppressed", &UpdateInfo{Available: true, ReleasesBehind: 1}, false},
		{"0 behind even if Available — suppressed", &UpdateInfo{Available: true, ReleasesBehind: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShouldNudge(tt.info))
		})
	}
}

func TestShouldNudge_EnvVarSuppressesNudge(t *testing.T) {
	info := &UpdateInfo{Available: true, ReleasesBehind: 40}
	// Without env: nudge fires
	assert.True(t, ShouldNudge(info))
	// With env set: nudge suppressed even at 40 behind
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")
	assert.False(t, ShouldNudge(info))
}

func TestCheckForUpdate_EnvSkipsCheckAndReturnsEmpty(t *testing.T) {
	// If AGENTDECK_SKIP_UPDATE_CHECK=1, CheckForUpdate must NOT hit the
	// network and must return a no-update UpdateInfo.
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")

	// Point at an httptest server that would fail the test if hit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("network hit with AGENTDECK_SKIP_UPDATE_CHECK=1: %s", r.URL.Path)
	}))
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })

	info, err := CheckForUpdate("1.7.1", true) // forceCheck=true proves env wins
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.False(t, info.Available)
	assert.Equal(t, "1.7.1", info.CurrentVersion)
	assert.Equal(t, 0, info.ReleasesBehind)
}

func TestCheckForUpdate_PopulatesReleasesBehind(t *testing.T) {
	// A fresh check (no cache hit path) must populate ReleasesBehind using
	// the /releases listing.
	latestRelease := Release{
		TagName: "v1.7.58",
		Name:    "v1.7.58",
		HTMLURL: "https://example/releases/v1.7.58",
		Assets: []Asset{{
			Name:               "agent-deck_1.7.58_linux_amd64.tar.gz",
			BrowserDownloadURL: "https://example/d/agent-deck_1.7.58_linux_amd64.tar.gz",
		}},
	}
	recent := []Release{
		{TagName: "v1.7.58"},
		{TagName: "v1.7.57"},
		{TagName: "v1.7.56"},
		{TagName: "v1.7.54"},
		{TagName: "v1.7.53"},
		{TagName: "v1.7.52"},
		{TagName: "v1.7.51"},
		{TagName: "v1.7.50"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(latestRelease)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })

	// Isolate disk cache so a stale entry on the dev machine doesn't skip the fetch.
	isolateUpdatePaths(t)

	info, err := CheckForUpdate("1.7.50", true)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.Available)
	assert.Equal(t, "1.7.58", info.LatestVersion)
	// 7 tags are newer than 1.7.50 in the fixture (51,52,53,54,56,57,58).
	assert.Equal(t, 7, info.ReleasesBehind)
}

func TestCachedUpdateInfo_OfflineReadFromCache(t *testing.T) {
	// `agent-deck --version` must be instant — never hit the network.
	// CachedUpdateInfo reads the on-disk cache directly.
	isolateUpdatePaths(t)

	// No cache yet → (nil, nil).
	info, err := CachedUpdateInfo("1.7.20")
	require.NoError(t, err)
	assert.Nil(t, info)

	// Write a fake cache.
	cache := &UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  "1.7.58",
		CurrentVersion: "1.7.20",
		ReleasesBehind: 30,
	}
	require.NoError(t, saveCache(cache))

	info, err = CachedUpdateInfo("1.7.20")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.Available)
	assert.Equal(t, "1.7.58", info.LatestVersion)
	assert.Equal(t, 30, info.ReleasesBehind)

	// User already ahead of cached latest → Available=false.
	info, err = CachedUpdateInfo("1.7.99")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.False(t, info.Available)
}

func TestCachedUpdateInfo_EnvSkipReturnsNil(t *testing.T) {
	isolateUpdatePaths(t)
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")

	// Even with a populated cache, env var suppresses the result so the
	// --version flag stays clean.
	cache := &UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  "1.7.58",
		CurrentVersion: "1.7.20",
		ReleasesBehind: 30,
	}
	require.NoError(t, saveCache(cache))

	info, err := CachedUpdateInfo("1.7.20")
	require.NoError(t, err)
	assert.Nil(t, info)
}

func TestCheckForUpdate_ReleasesBehindSurvivesCacheRoundtrip(t *testing.T) {
	// First call populates disk cache with ReleasesBehind. A second call
	// within the interval must read ReleasesBehind out of the cache — not
	// default to zero.
	latestRelease := Release{
		TagName: "v1.7.58",
		Name:    "v1.7.58",
		HTMLURL: "https://example/releases/v1.7.58",
	}
	recent := []Release{
		{TagName: "v1.7.58"},
		{TagName: "v1.7.57"},
		{TagName: "v1.7.56"},
		{TagName: "v1.7.54"},
		{TagName: "v1.7.53"},
		{TagName: "v1.7.52"},
		{TagName: "v1.7.51"},
	}
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(latestRelease)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(recent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })
	isolateUpdatePaths(t)

	// Seed cache by a force-check.
	first, err := CheckForUpdate("1.7.51", true)
	require.NoError(t, err)
	require.Equal(t, 6, first.ReleasesBehind)

	hitsAfterFirst := hits

	// Second call with forceCheck=false should hit cache and still return 6.
	second, err := CheckForUpdate("1.7.51", false)
	require.NoError(t, err)
	assert.Equal(t, 6, second.ReleasesBehind)
	assert.Equal(t, hitsAfterFirst, hits, "cached path must not hit the network")
}
