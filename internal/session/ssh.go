package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"golang.org/x/term"
)

// sshControlDir is the directory for SSH ControlMaster sockets.
const sshControlDir = "/tmp/agent-deck-ssh"

// SSHRunner executes commands on a remote host via SSH.
type SSHRunner struct {
	Host          string // SSH destination (e.g., "user@host")
	AgentDeckPath string // Remote agent-deck binary path
	Profile       string // Remote profile name
}

// NewSSHRunner creates an SSHRunner from a RemoteConfig.
func NewSSHRunner(name string, rc RemoteConfig) *SSHRunner {
	return &SSHRunner{
		Host:          rc.Host,
		AgentDeckPath: rc.GetAgentDeckPath(),
		Profile:       rc.GetProfile(),
	}
}

// Run executes an agent-deck command on the remote host and returns stdout.
func (r *SSHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.run(timeoutCtx, args...)
}

// run executes an agent-deck command on the remote host using the provided context directly.
func (r *SSHRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	_ = os.MkdirAll(sshControlDir, 0700)

	remoteCmd := r.buildRemoteCommand(args...)

	sshArgs := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + sshControlDir + "/%r@%h:%p",
		"-o", "ControlPersist=600",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		r.Host,
		remoteCmd,
	}

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh command failed: %w: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// Attach connects interactively to a remote agent-deck session.
// This manages the local terminal directly (rather than letting SSH do it)
// so that Ctrl+Q can be intercepted regardless of the terminal's key
// reporting mode (raw byte 0x11, xterm modifyOtherKeys, or kitty CSI u).
func (r *SSHRunner) Attach(sessionID string) error {
	_ = os.MkdirAll(sshControlDir, 0700)

	remoteCmd := r.buildRemoteCommand("session", "attach", sessionID)

	sshArgs := []string{
		"-tt", // force remote PTY even though local stdin is a pipe
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + sshControlDir + "/%r@%h:%p",
		"-o", "ControlPersist=600",
		r.Host,
		remoteCmd,
	}

	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	sshStdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ssh: %w", err)
	}

	// Forward stdin to SSH, intercepting Ctrl+Q to detach
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				break
			}
			data := buf[:n]

			if idx := tmux.IndexCtrlQ(data); idx >= 0 {
				if idx > 0 {
					_, _ = sshStdin.Write(data[:idx])
				}
				_ = sshStdin.Close()
				_ = cmd.Process.Kill()
				return
			}

			if _, err := sshStdin.Write(data); err != nil {
				break
			}
		}
	}()

	_ = cmd.Wait()
	return nil
}

// RunCommand executes an arbitrary agent-deck command on the remote.
func (r *SSHRunner) RunCommand(ctx context.Context, args ...string) ([]byte, error) {
	return r.Run(ctx, args...)
}

// buildRemoteCommand safely quotes each argument for execution through the remote shell.
func (r *SSHRunner) buildRemoteCommand(args ...string) string {
	parts := []string{shellQuote(r.AgentDeckPath)}
	if r.Profile != "" && r.Profile != "default" {
		parts = append(parts, "-p", shellQuote(r.Profile))
	}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

// FetchSessions retrieves the session list from the remote agent-deck instance.
func (r *SSHRunner) FetchSessions(ctx context.Context) ([]RemoteSessionInfo, error) {
	output, err := r.Run(ctx, "list", "--json")
	if err != nil {
		return nil, err
	}

	// Handle empty/non-JSON output (e.g., "No sessions found" message)
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, nil
	}

	var sessions []RemoteSessionInfo
	if err := json.Unmarshal(trimmed, &sessions); err != nil {
		return nil, fmt.Errorf("failed to parse remote sessions: %w", err)
	}

	return sessions, nil
}

// DetectPlatform returns the remote host's OS and architecture (e.g., "linux", "amd64").
func (r *SSHRunner) DetectPlatform(ctx context.Context) (goos, goarch string, err error) {
	_ = os.MkdirAll(sshControlDir, 0700)

	// Run uname on the remote to detect OS and machine architecture
	sshArgs := r.sshBaseArgs("uname -s -m")
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("failed to detect remote platform: %w: %s", err, stderr.String())
	}

	parts := strings.Fields(strings.TrimSpace(stdout.String()))
	if len(parts) != 2 {
		return "", "", fmt.Errorf("unexpected uname output: %s", stdout.String())
	}

	// Map uname output to Go's GOOS/GOARCH naming
	switch strings.ToLower(parts[0]) {
	case "linux":
		goos = "linux"
	case "darwin":
		goos = "darwin"
	default:
		return "", "", fmt.Errorf("unsupported remote OS: %s", parts[0])
	}

	switch parts[1] {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported remote arch: %s", parts[1])
	}

	return goos, goarch, nil
}

// CheckBinary checks if agent-deck exists at the configured path on the remote.
// Returns the version string if found, or empty string if not found.
func (r *SSHRunner) CheckBinary(ctx context.Context) (version string, found bool) {
	_ = os.MkdirAll(sshControlDir, 0700)

	remoteCmd := shellQuote(r.AgentDeckPath) + " version"
	sshArgs := r.sshBaseArgs(remoteCmd)
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "ssh", sshArgs...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return "", false
	}

	out := strings.TrimSpace(stdout.String())
	// Output is like "Agent Deck v0.20.2"
	if idx := strings.LastIndex(out, "v"); idx >= 0 {
		return strings.TrimSpace(out[idx+1:]), true
	}
	return out, true
}

// DeployBinary uploads a binary to the remote at the configured agent-deck path.
func (r *SSHRunner) DeployBinary(ctx context.Context, binaryData []byte) error {
	_ = os.MkdirAll(sshControlDir, 0700)

	// Write binary to temp file locally
	tmpFile, err := os.CreateTemp("", "agent-deck-remote-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(binaryData); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	// Ensure remote directory exists
	remoteDir := r.AgentDeckPath
	if idx := strings.LastIndex(remoteDir, "/"); idx > 0 {
		mkdirCmd := "mkdir -p " + shellQuote(remoteDir[:idx])
		mkdirArgs := r.sshBaseArgs(mkdirCmd)
		mkdirCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		mkCmd := exec.CommandContext(mkdirCtx, "ssh", mkdirArgs...)
		_ = mkCmd.Run()
	}

	// SCP the binary to the remote
	scpArgs := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + sshControlDir + "/%r@%h:%p",
		"-o", "ControlPersist=600",
		"-o", "ConnectTimeout=10",
		tmpPath,
		r.Host + ":" + r.AgentDeckPath,
	}

	scpCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	scpCmd := exec.CommandContext(scpCtx, "scp", scpArgs...)
	var stderr bytes.Buffer
	scpCmd.Stderr = &stderr
	if err := scpCmd.Run(); err != nil {
		return fmt.Errorf("scp failed: %w: %s", err, stderr.String())
	}

	// Make executable
	chmodCmd := "chmod +x " + shellQuote(r.AgentDeckPath)
	chmodArgs := r.sshBaseArgs(chmodCmd)
	chmodCtx, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	cCmd := exec.CommandContext(chmodCtx, "ssh", chmodArgs...)
	_ = cCmd.Run()

	return nil
}

// sshBaseArgs returns common SSH args for running a raw command on the remote.
func (r *SSHRunner) sshBaseArgs(remoteCmd string) []string {
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + sshControlDir + "/%r@%h:%p",
		"-o", "ControlPersist=600",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		r.Host,
		remoteCmd,
	}
}

// CreateSession creates and starts a new session on the remote, returning its ID.
// It runs "add --quick --json" to create the session, then "session start" to
// launch the tmux process, so the session is ready to attach.
func (r *SSHRunner) CreateSession(ctx context.Context) (string, error) {
	// Step 1: Create the session
	output, err := r.Run(ctx, "add", "--quick", "--json")
	if err != nil {
		return "", fmt.Errorf("failed to create remote session: %w", err)
	}

	var result struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("failed to parse remote add output: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("remote add returned empty session ID")
	}

	// Step 2: Start the session so it has a tmux process to attach to.
	// Use ID to avoid ambiguity when titles are duplicated.
	startCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := r.run(startCtx, "session", "start", result.ID); err != nil {
		return "", fmt.Errorf("failed to start remote session: %w", err)
	}

	return result.ID, nil
}

// DeleteSession removes a session on the remote host.
func (r *SSHRunner) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := r.Run(ctx, "remove", sessionID)
	return err
}

// StopSession stops a session process on the remote host without removing metadata.
func (r *SSHRunner) StopSession(ctx context.Context, sessionID string) error {
	_, err := r.Run(ctx, "session", "stop", sessionID)
	return err
}

// RestartSession restarts a session on the remote host.
func (r *SSHRunner) RestartSession(ctx context.Context, sessionID string) error {
	_, err := r.Run(ctx, "session", "restart", sessionID)
	return err
}

// RemoteSessionInfo represents a session from a remote agent-deck instance.
type RemoteSessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	Group     string `json:"group"`
	Tool      string `json:"tool"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`

	// Set locally, not from JSON
	RemoteName string `json:"-"`
}
