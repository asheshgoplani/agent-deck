package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name     string
		v1, v2   string
		expected int
	}{
		{"equal versions", "1.0.0", "1.0.0", 0},
		{"v1 less than v2", "1.0.0", "1.0.1", -1},
		{"v1 greater than v2", "2.0.0", "1.9.9", 1},
		{"with v prefix", "v1.2.3", "v1.2.3", 0},
		{"mixed prefix", "v1.0.0", "1.0.1", -1},
		{"major difference", "0.8.85", "0.9.0", -1},
		{"patch difference", "0.8.84", "0.8.85", -1},
		{"two-part version padded", "1.0", "1.0.0", 0},
		{"single-part version", "2", "1.9.9", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CompareVersions(tt.v1, tt.v2)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseChangelog(t *testing.T) {
	content := `# Changelog

## [0.8.85] - 2026-01-28

### Fixed
- Fix notification bar truncation

### Added
- New analytics panel

## [0.8.84] - 2026-01-27

### Fixed
- Fix status detection race
`

	entries := ParseChangelog(content)
	require.Len(t, entries, 2)

	assert.Equal(t, "0.8.85", entries[0].Version)
	assert.Equal(t, "2026-01-28", entries[0].Date)
	assert.Contains(t, entries[0].Content, "Fix notification bar truncation")
	assert.Contains(t, entries[0].Content, "New analytics panel")

	assert.Equal(t, "0.8.84", entries[1].Version)
	assert.Equal(t, "2026-01-27", entries[1].Date)
	assert.Contains(t, entries[1].Content, "Fix status detection race")
}

func TestParseChangelogEmpty(t *testing.T) {
	entries := ParseChangelog("")
	assert.Empty(t, entries)
}

func TestParseChangelogNoHeaders(t *testing.T) {
	entries := ParseChangelog("Just some text\nwithout version headers\n")
	assert.Empty(t, entries)
}

func TestGetChangesBetweenVersions(t *testing.T) {
	entries := []ChangelogEntry{
		{Version: "0.8.85", Date: "2026-01-28", Content: "latest changes"},
		{Version: "0.8.84", Date: "2026-01-27", Content: "middle changes"},
		{Version: "0.8.83", Date: "2026-01-26", Content: "old changes"},
	}

	tests := []struct {
		name          string
		current       string
		latest        string
		expectedCount int
		expectedFirst string
	}{
		{"one version behind", "0.8.84", "0.8.85", 1, "0.8.85"},
		{"two versions behind", "0.8.83", "0.8.85", 2, "0.8.85"},
		{"up to date", "0.8.85", "0.8.85", 0, ""},
		{"with v prefix", "v0.8.83", "v0.8.85", 2, "0.8.85"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetChangesBetweenVersions(entries, tt.current, tt.latest)
			assert.Len(t, result, tt.expectedCount)
			if tt.expectedCount > 0 {
				assert.Equal(t, tt.expectedFirst, result[0].Version)
			}
		})
	}
}

func TestFormatChangelogForDisplay(t *testing.T) {
	t.Run("empty entries", func(t *testing.T) {
		result := FormatChangelogForDisplay(nil)
		assert.Empty(t, result)
	})

	t.Run("section headers and bullet items", func(t *testing.T) {
		entries := []ChangelogEntry{
			{
				Version: "0.8.85",
				Date:    "2026-01-28",
				Content: "### Fixed\n- Bug fix one\n- Bug fix two\n\n### Added\n- New feature",
			},
		}
		result := FormatChangelogForDisplay(entries)
		assert.Contains(t, result, "v0.8.85")
		assert.Contains(t, result, "2026-01-28")
		assert.Contains(t, result, "[Fixed]")
		assert.Contains(t, result, "- Bug fix one")
		assert.Contains(t, result, "- Bug fix two")
		assert.Contains(t, result, "[Added]")
		assert.Contains(t, result, "- New feature")
	})

	t.Run("preserves unrecognized lines", func(t *testing.T) {
		entries := []ChangelogEntry{
			{
				Version: "1.0.0",
				Content: "### Changed\n- Item one\nSome plain text line\n  nested content",
			},
		}
		result := FormatChangelogForDisplay(entries)
		assert.Contains(t, result, "Some plain text line",
			"non-empty lines that don't match any prefix pattern should still appear")
	})

	t.Run("version without date", func(t *testing.T) {
		entries := []ChangelogEntry{
			{Version: "0.1.0", Content: "- Initial release"},
		}
		result := FormatChangelogForDisplay(entries)
		assert.Contains(t, result, "v0.1.0")
		assert.NotContains(t, result, "()")
	})
}

func TestUpdateBridgePy_NoConductorDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	err := UpdateBridgePy()
	require.NoError(t, err)

	condDir := filepath.Join(tmpHome, ".agent-deck", "conductor")
	_, statErr := os.Stat(condDir)
	assert.True(t, os.IsNotExist(statErr), "conductor dir should not be created when not installed")
}

func TestUpdateBridgePy_UsesEmbeddedTemplateAndBacksUpExistingFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	condDir := filepath.Join(tmpHome, ".agent-deck", "conductor")
	require.NoError(t, os.MkdirAll(condDir, 0o755))

	bridgePath := filepath.Join(condDir, "bridge.py")
	legacyContent := "# legacy bridge\nprint('old bridge')\n"
	require.NoError(t, os.WriteFile(bridgePath, []byte(legacyContent), 0o755))

	err := UpdateBridgePy()
	require.NoError(t, err)

	backupPath := bridgePath + ".backup"
	backupContent, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	assert.Equal(t, legacyContent, string(backupContent))

	newContent, err := os.ReadFile(bridgePath)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(newContent), "Conductor Bridge: Telegram & Slack"),
		"bridge.py should be refreshed from the embedded multi-platform template")
}

func TestNormalizeReleaseTag(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"whitespace", "  ", ""},
		{"plain semver", "1.7.4", "v1.7.4"},
		{"already prefixed", "v1.7.4", "v1.7.4"},
		{"uppercase prefix", "V1.7.4", "v1.7.4"},
		{"surrounding whitespace", "  1.7.4  ", "v1.7.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeReleaseTag(tt.input))
		})
	}
}

func TestFetchReleaseByTag(t *testing.T) {
	rel := Release{
		TagName: "v1.7.4",
		Name:    "v1.7.4",
		HTMLURL: "https://example/releases/v1.7.4",
		Assets: []Asset{{
			Name:               "agent-deck_1.7.4_darwin_arm64.tar.gz",
			BrowserDownloadURL: "https://example/download/agent-deck_1.7.4_darwin_arm64.tar.gz",
			Size:               123,
		}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/"+GitHubRepo+"/releases/tags/v1.7.4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rel)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })

	t.Run("accepts plain semver", func(t *testing.T) {
		rel, err := FetchReleaseByTag("1.7.4")
		require.NoError(t, err)
		require.NotNil(t, rel)
		assert.Equal(t, "v1.7.4", rel.TagName)
		assert.Equal(t, "https://example/download/agent-deck_1.7.4_darwin_arm64.tar.gz",
			GetAssetURLForPlatform(rel, "darwin", "arm64"))
	})

	t.Run("accepts prefixed tag", func(t *testing.T) {
		rel, err := FetchReleaseByTag("v1.7.4")
		require.NoError(t, err)
		assert.Equal(t, "v1.7.4", rel.TagName)
	})

	t.Run("missing tag", func(t *testing.T) {
		_, err := FetchReleaseByTag("99.0.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("empty tag", func(t *testing.T) {
		_, err := FetchReleaseByTag("  ")
		require.Error(t, err)
	})
}

func TestHomebrewUpgradeHint(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantOK   bool
		wantHint string
	}{
		{
			name:     "mac arm64 cellar",
			path:     "/opt/homebrew/Cellar/agent-deck/0.19.14/bin/agent-deck",
			wantOK:   true,
			wantHint: "brew upgrade asheshgoplani/tap/agent-deck",
		},
		{
			name:     "mac intel cellar",
			path:     "/usr/local/Cellar/agent-deck/0.19.14/bin/agent-deck",
			wantOK:   true,
			wantHint: "brew upgrade asheshgoplani/tap/agent-deck",
		},
		{
			name:     "linuxbrew cellar",
			path:     "/home/linuxbrew/.linuxbrew/Cellar/agent-deck/0.19.14/bin/agent-deck",
			wantOK:   true,
			wantHint: "brew upgrade asheshgoplani/tap/agent-deck",
		},
		{
			name:   "standalone local binary",
			path:   "/Users/ashesh/.local/bin/agent-deck",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint, ok := HomebrewUpgradeHint(tt.path)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantHint, hint)
		})
	}
}
