package update

import (
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
