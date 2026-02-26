package workspace

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipIfNoDocker skips the test when the Docker daemon is not reachable.
func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available, skipping")
	}
}

func TestNewDockerRuntime(t *testing.T) {
	skipIfNoDocker(t)

	rt, err := NewDockerRuntime()
	require.NoError(t, err)
	require.NotNil(t, rt)
	require.NotNil(t, rt.cli)

	// Verify we can actually talk to the daemon.
	_, err = rt.cli.Ping(context.Background())
	assert.NoError(t, err)
}

func TestDockerRuntimeImplementsInterface(t *testing.T) {
	// Compile-time check that DockerRuntime satisfies ContainerRuntime.
	var _ ContainerRuntime = (*DockerRuntime)(nil)
}
