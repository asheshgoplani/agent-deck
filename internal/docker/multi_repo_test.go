package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithMultiRepoPaths(t *testing.T) {
	t.Parallel()

	paths := []string{"/host/frontend", "/host/backend", "/host/shared-lib"}
	cfg := NewContainerConfig("/host/frontend", WithMultiRepoPaths(paths))

	// Default project mount should be replaced
	var containerPaths []string
	for _, v := range cfg.volumes {
		containerPaths = append(containerPaths, v.containerPath)
	}

	assert.Contains(t, containerPaths, "/workspace/frontend")
	assert.Contains(t, containerPaths, "/workspace/backend")
	assert.Contains(t, containerPaths, "/workspace/shared-lib")
	assert.NotContains(t, containerPaths, "/workspace") // default removed

	// Working dir should be /workspace
	assert.Equal(t, "/workspace", cfg.workingDir)
}

func TestWithMultiRepoPaths_DuplicateNames(t *testing.T) {
	t.Parallel()

	paths := []string{"/a/src", "/b/src", "/c/src"}
	cfg := NewContainerConfig("/a/src", WithMultiRepoPaths(paths))

	var containerPaths []string
	for _, v := range cfg.volumes {
		containerPaths = append(containerPaths, v.containerPath)
	}

	require.Len(t, containerPaths, 3)
	assert.Contains(t, containerPaths, "/workspace/src")
	assert.Contains(t, containerPaths, "/workspace/src-1")
	assert.Contains(t, containerPaths, "/workspace/src-2")
}
