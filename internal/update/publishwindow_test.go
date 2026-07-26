package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// releaseWithAssets builds a release carrying the full goreleaser asset set for
// one platform (tarball + checksums.txt), i.e. an installable release.
func releaseWithAssets(tag, goos, goarch string) Release {
	version := tag
	if len(version) > 0 && version[0] == 'v' {
		version = version[1:]
	}
	return Release{
		TagName: tag,
		HTMLURL: "https://example/releases/" + tag,
		Assets: []Asset{
			{
				Name:               "agent-deck_" + version + "_" + goos + "_" + goarch + ".tar.gz",
				BrowserDownloadURL: "https://example/d/" + version + "/" + goos + "-" + goarch + ".tar.gz",
			},
			{
				Name:               ChecksumsAssetName,
				BrowserDownloadURL: "https://example/d/" + version + "/checksums.txt",
			},
		},
	}
}

// emptyRelease is the shape that caused #1759: a real, published release whose
// assets have not been uploaded yet.
func emptyRelease(tag string) Release {
	return Release{TagName: tag, HTMLURL: "https://example/releases/" + tag}
}

func TestHasPlatformAsset(t *testing.T) {
	t.Run("tarball and checksums present", func(t *testing.T) {
		r := releaseWithAssets("v1.10.11", "darwin", "arm64")
		assert.True(t, HasPlatformAsset(&r, "darwin", "arm64"))
	})

	t.Run("no assets at all is the publish window", func(t *testing.T) {
		r := emptyRelease("v1.10.11")
		assert.False(t, HasPlatformAsset(&r, "darwin", "arm64"))
	})

	t.Run("other platform only", func(t *testing.T) {
		r := releaseWithAssets("v1.10.11", "linux", "amd64")
		assert.False(t, HasPlatformAsset(&r, "darwin", "arm64"))
	})

	t.Run("tarball uploaded but checksums.txt not yet", func(t *testing.T) {
		// Mid-upload: goreleaser writes checksums.txt last. Treating this as
		// installable would pick a release that DownloadVerifiedBinary then
		// refuses, converting a transient window into a hard failure.
		r := releaseWithAssets("v1.10.11", "darwin", "arm64")
		r.Assets = r.Assets[:1]
		assert.False(t, HasPlatformAsset(&r, "darwin", "arm64"))
	})

	t.Run("nil release", func(t *testing.T) {
		assert.False(t, HasPlatformAsset(nil, "darwin", "arm64"))
	})
}

func TestSelectInstallableRelease(t *testing.T) {
	t.Run("newest is installable", func(t *testing.T) {
		releases := []Release{
			releaseWithAssets("v1.10.11", "darwin", "arm64"),
			releaseWithAssets("v1.10.10", "darwin", "arm64"),
		}
		got, publishing, err := SelectInstallableRelease(releases, "darwin", "arm64")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "v1.10.11", got.TagName)
		assert.Nil(t, publishing, "nothing is mid-publish when the newest is installable")
	})

	t.Run("falls back past the mid-publish release", func(t *testing.T) {
		// The exact v1.10.11 situation: latest is published and empty.
		releases := []Release{
			emptyRelease("v1.10.11"),
			releaseWithAssets("v1.10.10", "darwin", "arm64"),
			releaseWithAssets("v1.10.9", "darwin", "arm64"),
		}
		got, publishing, err := SelectInstallableRelease(releases, "darwin", "arm64")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "v1.10.10", got.TagName, "newest release that actually has a binary")
		require.NotNil(t, publishing)
		assert.Equal(t, "v1.10.11", publishing.TagName)
	})

	t.Run("unsorted input is ordered by version not position", func(t *testing.T) {
		releases := []Release{
			releaseWithAssets("v1.9.9", "linux", "amd64"),
			emptyRelease("v1.10.11"),
			releaseWithAssets("v1.10.10", "linux", "amd64"),
			releaseWithAssets("v1.10.2", "linux", "amd64"),
		}
		got, publishing, err := SelectInstallableRelease(releases, "linux", "amd64")
		require.NoError(t, err)
		assert.Equal(t, "v1.10.10", got.TagName)
		require.NotNil(t, publishing)
		assert.Equal(t, "v1.10.11", publishing.TagName)
	})

	t.Run("drafts are never offered", func(t *testing.T) {
		draft := releaseWithAssets("v1.11.0", "darwin", "arm64")
		draft.Draft = true
		releases := []Release{draft, releaseWithAssets("v1.10.10", "darwin", "arm64")}
		got, publishing, err := SelectInstallableRelease(releases, "darwin", "arm64")
		require.NoError(t, err)
		assert.Equal(t, "v1.10.10", got.TagName)
		assert.Nil(t, publishing, "a draft is not a release the user is waiting for")
	})

	t.Run("pre-releases are never offered", func(t *testing.T) {
		pre := releaseWithAssets("v1.11.0", "darwin", "arm64")
		pre.Prerelease = true
		releases := []Release{pre, releaseWithAssets("v1.10.10", "darwin", "arm64")}
		got, _, err := SelectInstallableRelease(releases, "darwin", "arm64")
		require.NoError(t, err)
		assert.Equal(t, "v1.10.10", got.TagName, "must not move a user onto the pre-release channel")
	})

	t.Run("nothing installable", func(t *testing.T) {
		releases := []Release{emptyRelease("v1.10.11"), emptyRelease("v1.10.10")}
		got, publishing, err := SelectInstallableRelease(releases, "darwin", "arm64")
		require.ErrorIs(t, err, ErrNoInstallableRelease)
		assert.Nil(t, got)
		require.NotNil(t, publishing, "caller can still name the newest release it skipped")
		assert.Equal(t, "v1.10.11", publishing.TagName)
	})

	t.Run("no release built for this platform", func(t *testing.T) {
		// Unsupported GOOS/GOARCH — not a publish window, and must not be
		// reported as one.
		releases := []Release{
			releaseWithAssets("v1.10.11", "linux", "amd64"),
			releaseWithAssets("v1.10.10", "linux", "amd64"),
		}
		got, publishing, err := SelectInstallableRelease(releases, "windows", "arm64")
		require.ErrorIs(t, err, ErrNoInstallableRelease)
		assert.Nil(t, got)
		require.NotNil(t, publishing)
		assert.Equal(t, "v1.10.11", publishing.TagName)
	})

	t.Run("empty and untagged input", func(t *testing.T) {
		_, publishing, err := SelectInstallableRelease(nil, "darwin", "arm64")
		require.ErrorIs(t, err, ErrNoInstallableRelease)
		assert.Nil(t, publishing)

		_, _, err = SelectInstallableRelease([]Release{{TagName: "  "}}, "darwin", "arm64")
		require.ErrorIs(t, err, ErrNoInstallableRelease)
	})

	t.Run("an older asset-less release is not a publish window", func(t *testing.T) {
		releases := []Release{
			releaseWithAssets("v1.10.11", "darwin", "arm64"),
			emptyRelease("v1.9.0"),
		}
		got, publishing, err := SelectInstallableRelease(releases, "darwin", "arm64")
		require.NoError(t, err)
		assert.Equal(t, "v1.10.11", got.TagName)
		assert.Nil(t, publishing)
	})
}

// TestCheckForUpdate_PublishWindowFallsBack drives the whole path over a stub
// GitHub API: /releases/latest reports a tag with no assets (the #1759 window)
// and the listing carries an installable predecessor.
func TestCheckForUpdate_PublishWindowFallsBack(t *testing.T) {
	goos, goarch := runtime.GOOS, runtime.GOARCH

	latest := emptyRelease("v1.10.11")
	recent := []Release{
		latest,
		releaseWithAssets("v1.10.10", goos, goarch),
		releaseWithAssets("v1.10.9", goos, goarch),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(latest)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })

	isolateUpdatePaths(t)

	info, err := CheckForUpdate("1.10.9", true)
	require.NoError(t, err)
	require.NotNil(t, info)

	// The user is offered the newest release that can actually be installed,
	// with a working download URL instead of an empty one.
	assert.Equal(t, "1.10.10", info.LatestVersion)
	assert.NotEmpty(t, info.DownloadURL, "must not hand callers an empty download URL")
	assert.True(t, info.Available)
	// ...and is told which version is still landing.
	assert.Equal(t, "1.10.11", info.PublishingVersion)

	// A mid-publish answer must not be cached, or the user stays pinned to the
	// older release for a full check interval after the real one goes live.
	cached, err := CachedUpdateInfo("1.10.9")
	require.NoError(t, err)
	assert.Nil(t, cached, "publish-window results must not be persisted")
}

// TestCheckForUpdate_NoFallbackKeepsLatest pins the unsupported-platform case:
// when NO release has a binary for this platform, behaviour is unchanged — the
// newest release is still reported, and the result is cached as usual.
func TestCheckForUpdate_NoFallbackKeepsLatest(t *testing.T) {
	latest := releaseWithAssets("v1.10.11", "plan9", "mips")
	recent := []Release{latest, releaseWithAssets("v1.10.10", "plan9", "mips")}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(latest)
	})
	mux.HandleFunc("/repos/"+GitHubRepo+"/releases", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })

	isolateUpdatePaths(t)

	info, err := CheckForUpdate("1.10.9", true)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "1.10.11", info.LatestVersion)
	assert.Empty(t, info.PublishingVersion, "an unsupported platform is not a publish window")

	cached, err := CachedUpdateInfo("1.10.9")
	require.NoError(t, err)
	require.NotNil(t, cached, "normal results are still cached")
	assert.Equal(t, "1.10.11", cached.LatestVersion)
}
