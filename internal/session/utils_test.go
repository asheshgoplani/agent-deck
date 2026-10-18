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
		"Documents",
		".hidden",
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
			name:     "Trailing slash omits hidden directories",
			input:    tmpDir + string(os.PathSeparator),
			expected: []string{filepath.Join(tmpDir, "Documents"), filepath.Join(tmpDir, "personal"), filepath.Join(tmpDir, "playground"), filepath.Join(tmpDir, "projects"), filepath.Join(tmpDir, "work")},
		},
		{
			name:     "Dot segment discovers hidden directories",
			input:    tmpDir + string(os.PathSeparator) + ".",
			expected: []string{filepath.Join(tmpDir, ".hidden")},
		},
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
			name:     "Lowercase prefix matches capitalized directory",
			input:    filepath.Join(tmpDir, "doc"),
			expected: []string{filepath.Join(tmpDir, "Documents")},
		},
		{
			name:     "Uppercase prefix matches lowercase directories",
			input:    filepath.Join(tmpDir, "P"),
			expected: []string{filepath.Join(tmpDir, "personal"), filepath.Join(tmpDir, "playground"), filepath.Join(tmpDir, "projects")},
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

func TestGetDirectoryCompletionsTildeSymlinkParent(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{home, filepath.Join(outside, "sub"), filepath.Join(outside, "project")} {
		require.NoError(t, os.MkdirAll(dir, 0755))
	}
	require.NoError(t, os.Symlink(filepath.Join(outside, "sub"), filepath.Join(home, "link")))
	resolvedOutside, err := filepath.EvalSymlinks(outside)
	require.NoError(t, err)
	t.Setenv("HOME", home)
	for _, prefix := range []string{"~/link/../", "~/link/../pr", "~/link/../project"} {
		got, err := GetDirectoryCompletions(prefix)
		require.NoError(t, err)
		for _, path := range got {
			if len(path) > 0 && path[0] == '~' {
				path = filepath.Join(home, path[1:])
			}
			info, err := os.Stat(path)
			require.NoError(t, err, "completion must point to the scanned directory")
			require.True(t, info.IsDir())
		}
		assert.Contains(t, got, filepath.Join(resolvedOutside, "project"))
	}
	got, err := GetDirectoryCompletions("~/link/..")
	require.NoError(t, err)
	assert.Equal(t, []string{resolvedOutside}, got)
	require.Len(t, got, 1)
	projectPath, err := ResolveProjectPath(got[0])
	require.NoError(t, err)
	assert.Equal(t, resolvedOutside, projectPath)
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

func TestCompletionCycler_MatchesAndIndex(t *testing.T) {
	cycler := &CompletionCycler{}

	// Inactive cycler exposes no matches and no selection.
	assert.Nil(t, cycler.Matches())
	assert.Equal(t, -1, cycler.Index())

	matches := []string{"/a", "/b", "/c"}
	cycler.SetMatches(matches)

	// Before the first Next(), nothing is selected yet.
	assert.Equal(t, matches, cycler.Matches())
	assert.Equal(t, -1, cycler.Index())

	cycler.Next()
	assert.Equal(t, 0, cycler.Index())
	cycler.Next()
	assert.Equal(t, 1, cycler.Index())

	// Wrap around.
	cycler.Next()
	cycler.Next()
	assert.Equal(t, 0, cycler.Index())

	cycler.Reset()
	assert.Nil(t, cycler.Matches())
	assert.Equal(t, -1, cycler.Index())
}

// A huge directory must not make one completion read every entry.
func TestReadDirBoundedCapsAndSorts(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"d", "b", "e", "a", "c"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0755))
	}
	all, err := readDirBounded(dir, 100)
	require.NoError(t, err)
	names := make([]string, 0, len(all))
	for _, e := range all {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, names)

	capped, err := readDirBounded(dir, 3)
	require.NoError(t, err)
	assert.Len(t, capped, 3)

	_, err = readDirBounded(filepath.Join(dir, "missing"), 3)
	assert.Error(t, err)
}
