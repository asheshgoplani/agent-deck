package ssh

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Connection represents an SSH connection to a remote host.
// It wraps the native ssh command for maximum compatibility with
// user's SSH config, agent forwarding, and jump hosts.
type Connection struct {
	// Configuration
	Host         string // hostname or IP
	User         string // SSH username
	Port         int    // default 22
	IdentityFile string // path to private key (optional)
	JumpHost     string // jump host identifier (optional)

	// State
	mu        sync.Mutex
	connected bool
	lastError error
	lastCheck time.Time
}

// Config holds SSH connection configuration
type Config struct {
	Host         string
	User         string
	Port         int
	IdentityFile string
	JumpHost     string // Reference to another host definition
	TmuxPath     string // Path to tmux binary on remote (default: "tmux")
}

// NewConnection creates a new SSH connection with the given configuration
func NewConnection(cfg Config) *Connection {
	port := cfg.Port
	if port == 0 {
		port = 22
	}

	return &Connection{
		Host:         cfg.Host,
		User:         cfg.User,
		Port:         port,
		IdentityFile: expandPath(cfg.IdentityFile),
		JumpHost:     cfg.JumpHost,
	}
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// buildSSHArgs constructs the ssh command arguments
func (c *Connection) buildSSHArgs() []string {
	args := []string{}

	// Add jump host if configured
	if c.JumpHost != "" {
		args = append(args, "-J", c.JumpHost)
	}

	// Add identity file if configured
	if c.IdentityFile != "" {
		args = append(args, "-i", c.IdentityFile)
	}

	// Add port if non-default
	if c.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", c.Port))
	}

	// Disable strict host key checking for convenience (user can override in ~/.ssh/config)
	args = append(args, "-o", "StrictHostKeyChecking=accept-new")

	// Use batch mode for non-interactive commands (fail immediately if auth fails)
	args = append(args, "-o", "BatchMode=yes")

	// Connection timeout
	args = append(args, "-o", "ConnectTimeout=10")

	// Build target
	target := c.Host
	if c.User != "" {
		target = c.User + "@" + c.Host
	}
	args = append(args, target)

	return args
}

// TestConnection tests if the SSH connection can be established
func (c *Connection) TestConnection() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Use cached result if recent
	if time.Since(c.lastCheck) < 30*time.Second && c.lastError == nil {
		return nil
	}

	args := c.buildSSHArgs()
	args = append(args, "echo", "ok")

	cmd := exec.Command("ssh", args...)
	output, err := cmd.CombinedOutput()

	c.lastCheck = time.Now()
	if err != nil {
		c.connected = false
		c.lastError = fmt.Errorf("SSH connection failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
		return c.lastError
	}

	if !strings.Contains(string(output), "ok") {
		c.connected = false
		c.lastError = fmt.Errorf("unexpected SSH response: %s", strings.TrimSpace(string(output)))
		return c.lastError
	}

	c.connected = true
	c.lastError = nil
	return nil
}

// IsConnected returns whether the connection is currently healthy
func (c *Connection) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// LastError returns the last connection error
func (c *Connection) LastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastError
}

// RunCommand executes a command on the remote host and returns the output
// Commands are wrapped with PATH setup to find Homebrew tools on macOS
func (c *Connection) RunCommand(command string) (string, error) {
	args := c.buildSSHArgs()
	// Prepend common Homebrew paths to find tmux etc. on macOS
	// Works regardless of whether user's shell is bash or zsh
	wrappedCmd := fmt.Sprintf("PATH=/opt/homebrew/bin:/usr/local/bin:$PATH %s", command)
	args = append(args, wrappedCmd)

	cmd := exec.Command("ssh", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("remote command failed (exit %d): %s",
				exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("SSH command failed: %w", err)
	}

	return string(output), nil
}

// RunCommandWithStdin executes a command with stdin input
func (c *Connection) RunCommandWithStdin(command string, stdin io.Reader) (string, error) {
	args := c.buildSSHArgs()
	args = append(args, command)

	cmd := exec.Command("ssh", args...)
	cmd.Stdin = stdin
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("remote command failed (exit %d): %s",
				exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("SSH command failed: %w", err)
	}

	return string(output), nil
}

// StartInteractiveSession starts an interactive SSH session with PTY
// Returns the started command - caller must handle Wait()
func (c *Connection) StartInteractiveSession(command string) (*exec.Cmd, error) {
	args := c.buildSSHArgs()

	// Request PTY for interactive session
	args = append(args[:len(args)-1], "-t", args[len(args)-1])

	if command != "" {
		// Wrap command with PATH setup (same as RunCommand) to find tmux etc. on macOS
		wrappedCmd := fmt.Sprintf("PATH=/opt/homebrew/bin:/usr/local/bin:$PATH %s", command)
		args = append(args, wrappedCmd)
	}

	cmd := exec.Command("ssh", args...)
	return cmd, nil
}

// Dial establishes a connection for testing purposes
// This is primarily used to verify connectivity
func (c *Connection) Dial() error {
	return c.TestConnection()
}

// Target returns the SSH target string (user@host or just host)
func (c *Connection) Target() string {
	if c.User != "" {
		return c.User + "@" + c.Host
	}
	return c.Host
}

// HostIdentifier returns a unique identifier for this host
func (c *Connection) HostIdentifier() string {
	return c.Host
}

// ForwardPort sets up local port forwarding (for future MCP tunneling)
func (c *Connection) ForwardPort(localPort, remotePort int, remoteHost string) (*exec.Cmd, error) {
	if remoteHost == "" {
		remoteHost = "localhost"
	}

	args := c.buildSSHArgs()
	// Remove the target (last arg) temporarily
	target := args[len(args)-1]
	args = args[:len(args)-1]

	// Add port forwarding
	args = append(args, "-L", fmt.Sprintf("%d:%s:%d", localPort, remoteHost, remotePort))
	// Add -N to not execute a remote command
	args = append(args, "-N")
	// Add target back
	args = append(args, target)

	cmd := exec.Command("ssh", args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start port forward: %w", err)
	}

	// Wait briefly to ensure connection is established
	time.Sleep(100 * time.Millisecond)

	// Test if the local port is listening
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", localPort), 2*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("port forward failed to establish: %w", err)
	}
	conn.Close()

	return cmd, nil
}
