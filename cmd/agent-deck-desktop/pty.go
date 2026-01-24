package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// PTY wraps a pseudo-terminal for shell interaction.
type PTY struct {
	cmd  *exec.Cmd
	file *os.File
	mu   sync.Mutex
}

// SpawnPTY creates a new PTY running the specified shell.
func SpawnPTY(shell string) (*PTY, error) {
	if shell == "" {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
	}

	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	return &PTY{
		cmd:  cmd,
		file: ptmx,
	}, nil
}

// SpawnPTYWithCommand creates a new PTY running a specific command.
func SpawnPTYWithCommand(name string, args ...string) (*PTY, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	return &PTY{
		cmd:  cmd,
		file: ptmx,
	}, nil
}

// Read reads from the PTY.
func (p *PTY) Read(buf []byte) (int, error) {
	return p.file.Read(buf)
}

// Write writes to the PTY.
func (p *PTY) Write(data []byte) (int, error) {
	return p.file.Write(data)
}

// Resize changes the PTY window size.
func (p *PTY) Resize(cols, rows uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pty.Setsize(p.file, &pty.Winsize{
		Cols: cols,
		Rows: rows,
	})
}

// Close terminates the PTY and the shell process.
func (p *PTY) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	return p.file.Close()
}

// SpawnSSHPTY creates a PTY running an SSH session to a remote host.
// The SSH connection handles PTY allocation on the remote side (-t flag).
// The local PTY is used for terminal I/O (resize, raw mode).
//
// Parameters:
//   - hostID: SSH host identifier for connection config lookup
//   - tmuxSession: tmux session name to attach on the remote host
//   - sshBridge: SSH bridge for connection management
//
// Returns a PTY wrapping the local ssh process.
func SpawnSSHPTY(hostID, tmuxSession string, sshBridge *SSHBridge) (*PTY, error) {
	// Get the SSH connection (establishes ControlMaster if not already)
	conn, err := sshBridge.GetConnection(hostID)
	if err != nil {
		return nil, err
	}

	// Get remote tmux path
	tmuxPath := sshBridge.GetTmuxPath(hostID)

	// Build the attach command with proper quoting to prevent shell injection
	attachCmd := fmt.Sprintf("%s attach-session -t %q", tmuxPath, tmuxSession)

	// Use StartInteractiveSession which builds the proper SSH command with -t for PTY
	sshCmd, err := conn.StartInteractiveSession(attachCmd)
	if err != nil {
		return nil, err
	}

	// Set terminal environment for the ssh command
	sshCmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)

	// Start the SSH command with a local PTY
	ptmx, err := pty.Start(sshCmd)
	if err != nil {
		return nil, err
	}

	return &PTY{
		cmd:  sshCmd,
		file: ptmx,
	}, nil
}
