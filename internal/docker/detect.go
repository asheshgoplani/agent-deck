package docker

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// IsDockerAvailable returns true if the docker CLI is installed and in PATH.
func IsDockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// IsDaemonRunning returns true if the docker daemon is responsive.
// A 5-second defensive timeout is applied so callers without a deadline cannot block indefinitely.
func IsDaemonRunning(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// CheckAvailability verifies both the CLI and the daemon are usable.
// Returns nil when docker is ready, or a descriptive error.
func CheckAvailability(ctx context.Context) error {
	if !IsDockerAvailable() {
		return ErrDockerNotAvailable
	}
	if !IsDaemonRunning(ctx) {
		return ErrDaemonNotRunning
	}
	return nil
}
