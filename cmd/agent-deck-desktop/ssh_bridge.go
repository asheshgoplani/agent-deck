package main

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/ssh"
)

// SSHBridge provides SSH connectivity to remote hosts for the desktop app.
// It bridges to the existing internal/ssh infrastructure used by the TUI.
type SSHBridge struct {
	pool *ssh.Pool
}

// NewSSHBridge creates a new SSH bridge and initializes the connection pool
// with hosts from the user's config.toml.
func NewSSHBridge() *SSHBridge {
	// Initialize the SSH pool from config.toml [ssh_hosts.X] sections
	session.InitSSHPool()

	return &SSHBridge{
		pool: ssh.DefaultPool(),
	}
}

// RunCommand executes a command on a remote host and returns the output.
// The hostID should match a configured [ssh_hosts.X] section in config.toml.
func (b *SSHBridge) RunCommand(hostID, command string) (string, error) {
	conn, err := b.pool.Get(hostID)
	if err != nil {
		return "", fmt.Errorf("SSH connection failed for %s: %w", hostID, err)
	}
	return conn.RunCommand(command)
}

// TestConnection tests if a remote host is reachable.
// Returns nil if the connection is successful.
func (b *SSHBridge) TestConnection(hostID string) error {
	return b.pool.TestConnection(hostID)
}

// GetConnection returns the SSH connection for a host, creating one if needed.
func (b *SSHBridge) GetConnection(hostID string) (*ssh.Connection, error) {
	return b.pool.Get(hostID)
}

// IsHostConfigured returns true if the hostID is configured in config.toml.
func (b *SSHBridge) IsHostConfigured(hostID string) bool {
	_, exists := b.pool.GetConfig(hostID)
	return exists
}

// ListConfiguredHosts returns all configured SSH host IDs.
func (b *SSHBridge) ListConfiguredHosts() []string {
	return b.pool.ListHosts()
}

// GetHostStatus returns connection status for all configured hosts.
func (b *SSHBridge) GetHostStatus() []ssh.Status {
	return b.pool.Status()
}

// Close closes a specific SSH connection and its ControlMaster socket.
func (b *SSHBridge) Close(hostID string) {
	b.pool.Close(hostID)
}

// CloseAll closes all SSH connections in the pool.
func (b *SSHBridge) CloseAll() {
	b.pool.CloseAll()
}

// GetTmuxPath returns the tmux binary path for a remote host.
// Falls back to "tmux" if not explicitly configured.
func (b *SSHBridge) GetTmuxPath(hostID string) string {
	cfg, exists := b.pool.GetConfig(hostID)
	if !exists || cfg.TmuxPath == "" {
		return "tmux"
	}
	return cfg.TmuxPath
}
