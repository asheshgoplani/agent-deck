package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsMultiRepo(t *testing.T) {
	inst := &Instance{}
	assert.False(t, inst.IsMultiRepo())

	inst.MultiRepoEnabled = true
	assert.True(t, inst.IsMultiRepo())
}

func TestAllProjectPaths(t *testing.T) {
	inst := &Instance{
		ProjectPath:     "/path/to/frontend",
		AdditionalPaths: []string{"/path/to/backend", "/path/to/shared"},
	}
	paths := inst.AllProjectPaths()
	assert.Equal(t, []string{"/path/to/frontend", "/path/to/backend", "/path/to/shared"}, paths)
}

func TestAllProjectPaths_NoAdditional(t *testing.T) {
	inst := &Instance{ProjectPath: "/path/to/project"}
	paths := inst.AllProjectPaths()
	assert.Equal(t, []string{"/path/to/project"}, paths)
}

func TestEffectiveWorkingDir(t *testing.T) {
	// Non-multi-repo: returns ProjectPath
	inst := &Instance{ProjectPath: "/path/to/project"}
	assert.Equal(t, "/path/to/project", inst.EffectiveWorkingDir())

	// Multi-repo with temp dir: returns temp dir
	inst.MultiRepoEnabled = true
	inst.MultiRepoTempDir = "/tmp/agent-deck-sessions/abc123"
	assert.Equal(t, "/tmp/agent-deck-sessions/abc123", inst.EffectiveWorkingDir())

	// Multi-repo without temp dir: falls back to ProjectPath
	inst.MultiRepoTempDir = ""
	assert.Equal(t, "/path/to/project", inst.EffectiveWorkingDir())
}

func TestCleanupMultiRepoTempDir(t *testing.T) {
	// No temp dir: no-op
	inst := &Instance{}
	assert.NoError(t, inst.CleanupMultiRepoTempDir())

	// With temp dir: removes it
	tmpDir := filepath.Join(os.TempDir(), "agent-deck-test-cleanup")
	require.NoError(t, os.MkdirAll(tmpDir, 0755))
	inst.MultiRepoTempDir = tmpDir
	assert.NoError(t, inst.CleanupMultiRepoTempDir())
	_, err := os.Stat(tmpDir)
	assert.True(t, os.IsNotExist(err))
}

func TestDeduplicateDirnames(t *testing.T) {
	tests := []struct {
		name     string
		paths    []string
		expected []string
	}{
		{
			name:     "unique names",
			paths:    []string{"/a/frontend", "/b/backend", "/c/shared"},
			expected: []string{"frontend", "backend", "shared"},
		},
		{
			name:     "duplicate names",
			paths:    []string{"/a/src", "/b/src", "/c/src"},
			expected: []string{"src", "src-1", "src-2"},
		},
		{
			name:     "mixed",
			paths:    []string{"/a/app", "/b/lib", "/c/app"},
			expected: []string{"app", "lib", "app-1"},
		},
		{
			name:     "empty",
			paths:    []string{},
			expected: []string{},
		},
		{
			name:     "single",
			paths:    []string{"/path/to/repo"},
			expected: []string{"repo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DeduplicateDirnames(tt.paths)
			assert.Equal(t, tt.expected, result)
		})
	}
}
