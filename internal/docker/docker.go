// Package docker manages container lifecycle for sandboxed agent sessions.
//
// Shell assumptions: commands delivered via tmux traverse two shell layers
// (tmux's implicit /bin/sh -c and wrapIgnoreSuspend's bash -c). Values in
// ExecPrefix / ExecPrefixWithEnv are therefore unquoted — quoting is applied
// once at the wrapIgnoreSuspend boundary.
//
// Security: the Docker socket is intentionally NOT mounted into containers.
// Agents run inside a sandbox with no access to the host Docker daemon.
package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os/exec"
	"slices"
	"strings"
)

// Container manages a single Docker container lifecycle.
type Container struct {
	// name is the container name (e.g. "agent-deck-a1b2c3d4").
	name string

	// image is the Docker image to use.
	image string
}

// NewContainer creates a container handle with the given name and image.
func NewContainer(name string, image string) *Container {
	if image == "" {
		image = defaultImage
	}
	return &Container{name: name, image: image}
}

// FromName creates a container handle for an existing container by name.
// The returned handle supports lifecycle operations (Exists, IsRunning, Start,
// Stop, Remove, ExecPrefix) but not Create — use NewContainer for that.
func FromName(name string) *Container {
	return &Container{name: name}
}

// GenerateName builds a container name from a session ID and human-readable title.
// Format: agent-deck-{title}-{id8}. The 8-char ID suffix guarantees uniqueness;
// the title is just for human readability in docker ps output.
func GenerateName(sessionID string, sessionTitle string) string {
	const idLen = 8 // First 8 chars of the session UUID — enough for uniqueness.
	id := sessionID
	if len(id) > idLen {
		id = id[:idLen]
	}
	sanitized := sanitizeContainerName(sessionTitle)
	if sanitized == "" {
		return containerNamePrefix + id
	}
	return containerNamePrefix + sanitized + "-" + id
}

// sanitizeContainerName strips characters not allowed in Docker container names
// ([a-zA-Z0-9_.-]) and truncates for readability.
func sanitizeContainerName(name string) string {
	const maxLen = 30
	var b strings.Builder
	for _, c := range name {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-':
			b.WriteRune(c)
		case c == ' ':
			b.WriteByte('-')
		}
	}
	result := b.String()
	// Trim leading/trailing hyphens and dots (Docker rejects these).
	result = strings.Trim(result, "-.")
	if len(result) > maxLen {
		result = result[:maxLen]
		result = strings.TrimRight(result, "-.")
	}
	return result
}

// Name returns the container name.
func (c *Container) Name() string {
	return c.name
}

// Exists returns true if the container exists (running or stopped).
// A non-zero exit code from docker inspect indicates the container does not exist.
// Other errors (e.g. Docker daemon unreachable) are propagated.
func (c *Container) Exists(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx,
		"docker", "inspect",
		"--format", "{{.State.Status}}",
		c.name,
	).CombinedOutput()
	if err != nil {
		if isExitError(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspecting container %s: %s: %w", c.name, strings.TrimSpace(string(out)), err)
	}
	return true, nil
}

// IsRunning returns true if the container is currently running.
// Uses exit code as the primary signal rather than parsing error messages.
func (c *Container) IsRunning(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx,
		"docker", "inspect",
		"--format", "{{.State.Running}}",
		c.name,
	).CombinedOutput()
	if err != nil {
		if isExitError(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspecting container %s: %w", c.name, err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// Create creates the container from the given config without starting it.
// Returns the container ID on success. If the container already exists,
// it is treated as a no-op and the existing container ID is returned.
func (c *Container) Create(ctx context.Context, cfg *ContainerConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("cannot create container %s: nil config", c.name)
	}
	if c.image == "" {
		return "", fmt.Errorf("cannot create container %s: no image specified", c.name)
	}

	args := []string{
		"create",
		"--name", c.name,
		"--label", "managed-by=agent-deck",
		// Security hardening: drop all capabilities and prevent privilege escalation.
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		// Limit process count to prevent fork bombs.
		"--pids-limit=4096",
		// Read-only root filesystem — writable paths are explicitly mounted as tmpfs below.
		"--read-only",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
		"--tmpfs", "/var/tmp:rw,noexec,nosuid,size=128m",
		// Node.js and npm require writable cache/config directories.
		"--tmpfs", "/root/.npm:rw,nosuid,size=256m",
		"--tmpfs", "/root/.cache:rw,nosuid,size=512m",
	}

	if cfg.workingDir != "" {
		args = append(args, "--workdir", cfg.workingDir)
	}

	// Bind mounts.
	for _, v := range cfg.volumes {
		mount := fmt.Sprintf("%s:%s", v.hostPath, v.containerPath)
		if v.readOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	// Anonymous volumes (excluded directories get their own layer).
	for _, anonVol := range cfg.anonymousVolumes {
		args = append(args, "-v", anonVol)
	}

	// Environment variables. Values are passed as exec.Command args (not shell-interpreted),
	// so no shell escaping is needed here — Go's exec.Command passes them directly to the kernel.
	// Keys sorted for deterministic output and reproducible debugging.
	for _, k := range slices.Sorted(maps.Keys(cfg.environment)) {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, cfg.environment[k]))
	}

	// Resource limits.
	if cfg.cpuLimit != "" {
		args = append(args, "--cpus", cfg.cpuLimit)
	}
	if cfg.memoryLimit != "" {
		args = append(args, "--memory", cfg.memoryLimit)
	}

	args = append(args, c.image)

	// Default command keeps the container alive for docker exec.
	args = append(args, "sleep", "infinity")

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		// Idempotent: if the container already exists, treat as success.
		exists, existsErr := c.Exists(ctx)
		if existsErr == nil && exists {
			return c.name, nil
		}
		return "", fmt.Errorf("creating container %s: %s: %w", c.name, strings.TrimSpace(string(out)), err)
	}
	// Always return the container name for consistency — callers should not
	// depend on the raw docker output format (which is a full container ID).
	return c.name, nil
}

// Start starts a stopped container.
// If the container is already running, this is a no-op.
func (c *Container) Start(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "docker", "start", c.name).CombinedOutput()
	if err != nil {
		// Idempotent: if the container is already running, treat as success.
		running, runErr := c.IsRunning(ctx)
		if runErr == nil && running {
			return nil
		}
		return fmt.Errorf("starting container %s: %s: %w", c.name, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Stop gracefully stops a running container.
func (c *Container) Stop(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "docker", "stop", c.name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stopping container %s: %s: %w", c.name, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Remove removes the container and its anonymous volumes.
// If force is true, a running container is killed first.
// If the container does not exist, this is a no-op.
func (c *Container) Remove(ctx context.Context, force bool) error {
	args := []string{"rm", "-v"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, c.name)

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		// Idempotent: container already gone is not an error.
		if isExitError(err) && strings.Contains(strings.ToLower(outStr), "no such container") {
			return nil
		}
		return fmt.Errorf("removing container %s: %s: %w", c.name, outStr, err)
	}
	return nil
}

// ExecPrefix returns the command prefix for running a command inside this container.
// Returns ["docker", "exec", "-it", name].
func (c *Container) ExecPrefix() []string {
	return []string{"docker", "exec", "-it", c.name}
}

// ExecPrefixWithEnv returns the command prefix with -e flags for runtime env vars.
// Each token is a discrete argument suitable for exec.Command (no shell quoting).
// Use ShellJoinArgs to convert to a shell-safe string when embedding in bash -c.
// Keys are sorted for deterministic output.
func (c *Container) ExecPrefixWithEnv(env map[string]string) []string {
	args := []string{"docker", "exec", "-it"}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, env[k]))
	}
	args = append(args, c.name)
	return args
}

// ShellJoinArgs joins command arguments into a shell-safe string.
// Each argument is single-quoted to prevent shell interpretation of special
// characters (spaces, quotes, $, backticks, semicolons, etc.).
// Arguments that are simple (alphanumeric, hyphens, underscores, dots, slashes,
// equals, colons, commas) are left unquoted for readability.
func ShellJoinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuoteArg(arg)
	}
	return strings.Join(quoted, " ")
}

// shellQuoteArg returns a shell-safe representation of a single argument.
// Simple arguments are returned as-is; others are single-quoted with
// internal single quotes escaped via the '"'"' pattern.
func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	// Safe chars that don't need quoting in POSIX shell.
	safe := true
	for _, c := range arg {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '/' || c == '=' || c == ':' || c == ',') {
			safe = false
			break
		}
	}
	if safe {
		return arg
	}
	// Single-quote the argument, escaping any internal single quotes.
	escaped := strings.ReplaceAll(arg, `'`, `'"'"'`)
	return "'" + escaped + "'"
}

// EnsureImage checks that image exists locally, pulling it if missing.
func EnsureImage(ctx context.Context, image string) error {
	if imageExistsLocally(ctx, image) {
		return nil
	}
	return pullImage(ctx, image)
}

// NewContainerConfig creates a ContainerConfig for a sandboxed session.
// projectPath is the host directory to mount as /workspace (must be non-empty).
// Optional ContainerConfigOption functions customize mounts, limits, and environment.
func NewContainerConfig(projectPath string, opts ...ContainerConfigOption) *ContainerConfig {
	if projectPath == "" {
		slog.Warn("NewContainerConfig called with empty projectPath")
	}

	cfg := &ContainerConfig{
		workingDir:    containerWorkDir,
		containerHome: containerHome,
		environment:   make(map[string]string),
	}

	// Mount project directory.
	if projectPath != "" {
		cfg.volumes = append(cfg.volumes, VolumeMount{
			hostPath:      projectPath,
			containerPath: containerWorkDir,
		})
	}

	// Apply caller-supplied options.
	for _, opt := range opts {
		opt(cfg)
	}

	// IS_SANDBOX=1 allows Claude Code to use --dangerously-skip-permissions in container.
	// Set after options to prevent caller-supplied values from disabling sandbox mode.
	cfg.environment["IS_SANDBOX"] = "1"

	return cfg
}

// ListManagedContainers returns names of all containers with the managed-by=agent-deck label.
func ListManagedContainers(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx,
		"docker", "ps", "-a",
		"--filter", "label=managed-by=agent-deck",
		"--format", "{{.Names}}",
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("listing managed containers: %s: %w", strings.TrimSpace(string(out)), err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// imageExistsLocally returns true if the image is available in the local docker cache.
func imageExistsLocally(ctx context.Context, image string) bool {
	err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run()
	return err == nil
}

// pullImage pulls a docker image from the registry.
func pullImage(ctx context.Context, image string) error {
	out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pulling image %s: %s: %w", image, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// isExitError returns true if the error is an exec.ExitError (non-zero exit code).
func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}
