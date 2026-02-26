package docker

import "errors"

// Sentinel errors for Docker operations.
var (
	// ErrDockerNotAvailable indicates the docker CLI is not installed.
	ErrDockerNotAvailable = errors.New("docker CLI is not installed or not in PATH")

	// ErrDaemonNotRunning indicates the docker daemon is not running.
	ErrDaemonNotRunning = errors.New("docker daemon is not running; start Docker and try again")
)
