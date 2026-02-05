package session

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDirectoryCompletions(t *testing.T) {
	// Setup temporary directory structure
	tmpDir := t.TempDir()

	// Create some directories
	dirs := []string{
		"projects",
		"playground",
		"personal",
		"work/agent-deck",
		"work/other",
	}
	for _, d := range dirs {
		err := os.MkdirAll(filepath.Join(tmpDir, d), 0755)
		require.NoError(t, err)
	}

	// Create some files (should be ignored)
	files := []string{
		"README.md",
		"projects/todo.txt",
	}
	for _, f := range files {
		err := os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0644)
		require.NoError(t, err)
	}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Absolute path prefix",
			input:    filepath.Join(tmpDir, "p"),
			expected: []string{filepath.Join(tmpDir, "personal"), filepath.Join(tmpDir, "playground"), filepath.Join(tmpDir, "projects")},
		},
		{
			name:     "Nested absolute path",
			input:    filepath.Join(tmpDir, "work/a"),
			expected: []string{filepath.Join(tmpDir, "work/agent-deck")},
		},
		{
			name:     "No matches",
			input:    filepath.Join(tmpDir, "xyz"),
			expected: nil,
		},
		{
			name:     "Exact match directory (should return itself and any subdirs starting with it)",
			input:    filepath.Join(tmpDir, "projects"),
			expected: []string{filepath.Join(tmpDir, "projects")},
		},
		{
			name:     "Trailing slash lists directory contents",
			input:    filepath.Join(tmpDir, "work") + string(os.PathSeparator),
			expected: []string{filepath.Join(tmpDir, "work/agent-deck"), filepath.Join(tmpDir, "work/other")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := GetDirectoryCompletions(tt.input)
			assert.NoError(t, err)
			sort.Strings(results)
			sort.Strings(tt.expected)
			assert.Equal(t, tt.expected, results)
		})
	}
}

func TestCompletionCycler(t *testing.T) {
	cycler := &CompletionCycler{}

	// 1. Initial state
	assert.Equal(t, "", cycler.Next())

	// 2. Set matches and cycle
	matches := []string{"/a", "/b", "/c"}
	cycler.SetMatches(matches)
	assert.True(t, cycler.IsActive())

	assert.Equal(t, "/a", cycler.Next())
	assert.Equal(t, "/b", cycler.Next())
	assert.Equal(t, "/c", cycler.Next())

	// 3. Wrap around
	assert.Equal(t, "/a", cycler.Next())

	// 4. Reset
	cycler.Reset()
	assert.False(t, cycler.IsActive())
	assert.Equal(t, "", cycler.Next())

	// 5. Change matches
	cycler.SetMatches([]string{"/x"})
	assert.Equal(t, "/x", cycler.Next())
	assert.Equal(t, "/x", cycler.Next())
}
