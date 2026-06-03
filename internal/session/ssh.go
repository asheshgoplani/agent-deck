package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/termreply"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// sshAttachReplyQuarantine matches attachReplyQuarantine in internal/tmux/pty.go.
// Keep these in sync — they cover the same class of terminal-reply bursts on
// their respective attach paths (local tmux vs SSH remote).
const sshAttachReplyQuarantine = 500 * time.Millisecond

// sshControlDir is the directory for SSH ControlMaster sockets.
const sshControlDir = "/tmp/agent-deck-ssh"

// SSHRunner executes commands on a remote host via SSH.
type SSHRunner struct {
	Host          string // SSH destination (e.g., "user@host")
	AgentDeckPath string // Remote agent-deck binary path
	Profile       string // Remote profile name

	// configuredPath is the raw agent_deck_path from config ("" if unset). It
	// lets ResolveRemotePath decide whether to honor an explicit user path or
	// probe the remote's real binary location via $PATH (#1171).
	configuredPath string

	// runFn lets tests stub out command execution. nil = real SSH.
	runFn func(ctx context.Context, args ...string) ([]byte, error)

	// openStreamFn lets tests stub out the persistent-stream subprocess
	// without spawning real ssh. nil = real SSH (#1112 bug 2).
	openStreamFn func(ctx context.Context, args ...string) (io.WriteCloser, func() error, error)

	// remoteExecFn lets tests stub raw remote shell execution used by the
	// update/deploy path (ResolveRemotePath, DeployBinary, version checks)
	// without spawning a real ssh/scp subprocess. nil = real SSH (#1171).
	remoteExecFn func(ctx context.Context, remoteCmd string, stdin []byte) ([]byte, error)
}

// NewSSHRunner creates an SSHRunner from a RemoteConfig.
func NewSSHRunner(name string, rc RemoteConfig) *SSHRunner {
	return &SSHRunner{
		Host:           rc.Host,
		AgentDeckPath:  rc.GetAgentDeckPath(),
		configuredPath: rc.AgentDeckPath,
		Profile:        rc.GetProfile(),
	}
}

// Run executes an agent-deck command on the remote host and returns stdout.
func (r *SSHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.run(timeoutCtx, args...)
}

// OpenStream spawns a single long-running remote `agent-deck <args...>`
// subprocess over SSH and returns its stdin pipe + a close function that
// terminates the subprocess. Used by #1112 bug 2's persistent insert-mode
// stream so 100 keystrokes amortize to one ssh fork+exec instead of 100.
//
// The returned WriteCloser is goroutine-safe at the OS pipe layer; the
// caller is responsible for serializing if it needs message-level
// ordering (RemoteKeySender does this with its own mutex).
func (r *SSHRunner) OpenStream(ctx context.Context, args ...string) (io.WriteCloser, func() error, error) {
	if r.openStreamFn != nil {
		return r.openStreamFn(ctx, args...)
	}
	if err := ValidateSSHHost(r.Host); err != nil {
		return nil, nil, err
	}
	_ = os.MkdirAll(sshControlDir, 0700)

	remoteCmd := r.buildRemoteCommand(args...)
	sshArgs := r.sshBaseArgs(remoteCmd)

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stream stdin pipe: %w", err)
	}
	// Drop the subprocess's stdout/stderr — `--stream` mode prints nothing
	// on success, and surfacing partial errors would require parsing the
	// CLIOutput JSON. Failures already surface via the stdin write erroring
	// when the remote exits.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, nil, fmt.Errorf("stream start: %w", err)
	}
	closeFn := func() error {
		_ = stdin.Close()
		if cmd.Process != nil {
			// stdin close should make the remote loop exit; kill as backstop.
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		return nil
	}
	return stdin, closeFn, nil
}

// run executes an agent-deck command on the remote host using the provided context directly.
func (r *SSHRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	if r.runFn != nil {
		return r.runFn(ctx, args...)
	}
	if err := ValidateSSHHost(r.Host); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(sshControlDir, 0700)

	remoteCmd := r.buildRemoteCommand(args...)
	sshArgs := r.sshBaseArgs(remoteCmd)

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
// Uses a local PTY so that SSH can detect the terminal dimensions and
// propagate them to the remote side. Handles SIGWINCH to keep the remote
// PTY in sync when the local terminal is resized, and sends SIGWINCH to
// self on detach so Bubble Tea re-queries the terminal size.
func (r *SSHRunner) Attach(sessionID string) error {
	if err := ValidateSSHHost(r.Host); err != nil {
		return err
	}
	_ = os.MkdirAll(sshControlDir, 0700)

	sshArgs := r.buildAttachArgs(sessionID)

	cmd := exec.Command("ssh", sshArgs...)

	// Start SSH with a local PTY pre-sized to the controlling terminal so the
	// remote tmux client connects full-width from frame one (#1167). A bare
	// pty.Start creates the PTY at the 80x24 default, which under the remote
	// session's window-size=largest pins the pane to ~half a wide terminal
	// until an async SIGWINCH grows it. Shares the local-attach helper so both
	// paths size identically.
	ptmx, err := tmux.StartAttachPTY(cmd, os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to start ssh with pty: %w", err)
	}
	defer ptmx.Close()

	// Set the PTY slave to raw mode so all bytes pass through transparently.
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		return fmt.Errorf("failed to set pty raw mode: %w", err)
	}

	// Save original terminal state and set raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Handle SIGWINCH to resize the PTY when the local terminal is resized.
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	sigwinchDone := make(chan struct{})
	defer func() {
		signal.Stop(sigwinch)
		close(sigwinchDone)
	}()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-sigwinchDone:
				return
			case _, ok := <-sigwinch:
				if !ok {
					return
				}
				if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
					_ = pty.Setsize(ptmx, ws)
				}
			}
		}
	}()

	// Initial resize to propagate current terminal dimensions.
	sigwinch <- syscall.SIGWINCH

	detachCh := make(chan struct{})
	outputDone := make(chan struct{})

	// Copy PTY output to stdout.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(outputDone)
		_, _ = io.Copy(os.Stdout, ptmx)
	}()

	// Read stdin, intercept Ctrl+Q (all encodings), forward the rest.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				break
			}
			data := buf[:n]

			if idx := tmux.IndexCtrlQ(data); idx >= 0 {
				if idx > 0 {
					_, _ = ptmx.Write(data[:idx])
				}
				close(detachCh)
				return
			}

			if _, err := ptmx.Write(data); err != nil {
				break
			}
		}
	}()

	// Wait for SSH to exit.
	cmdDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmdDone <- cmd.Wait()
	}()

	// Block until detach or SSH exit.
	select {
	case <-detachCh:
	case <-cmdDone:
	}

	// Cleanup: close PTY and wait for output to drain.
	_ = ptmx.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-outputDone:
	case <-time.After(50 * time.Millisecond):
	}
	termreply.QuarantineFor(sshAttachReplyQuarantine)

	// Reset terminal styles that may have leaked from the remote session.
	_, _ = os.Stdout.WriteString("\x1b]8;;\x1b\\\x1b[0m\x1b[24m\x1b[39m\x1b[49m")

	// Send SIGWINCH to self so Bubble Tea re-queries terminal dimensions
	// and redraws the TUI with the correct layout on return.
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		_ = p.Signal(syscall.SIGWINCH)
	}

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

type remoteSessionOutputJSON struct {
	Content string `json:"content"`
}

func parseRemoteSessionOutput(output []byte) (string, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return "", nil
	}

	var parsed remoteSessionOutputJSON
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse remote session output: %w", err)
	}

	return parsed.Content, nil
}

// FetchSessionOutput retrieves the last response content for a remote session.
func (r *SSHRunner) FetchSessionOutput(ctx context.Context, sessionID string) (string, error) {
	output, err := r.Run(ctx, "session", "output", sessionID, "--json")
	if err != nil {
		return "", err
	}

	return parseRemoteSessionOutput(output)
}

// FetchSessionPane retrieves the tmux capture-pane content for a remote session.
// #1101: Local TUI previews render capture-pane content (ANSI + tool UI chrome).
// Remote previews used to fetch only the parsed transcript text via
// FetchSessionOutput, which is why claude-formatted output never showed for
// SSH sessions. FetchSessionPane closes that gap by asking the remote for the
// raw pane content via `session output --pane --json`.
func (r *SSHRunner) FetchSessionPane(ctx context.Context, sessionID string) (string, error) {
	output, err := r.Run(ctx, "session", "output", sessionID, "--pane", "--json")
	if err != nil {
		return "", err
	}

	return parseRemoteSessionOutput(output)
}

// FetchCostSummary retrieves the remote agent-deck's cost summary as JSON.
// #1101: the local TUI's status-line cost segment used to show only events
// written to the local cost_events table — remote sessions' Stop hooks write
// to the remote DB, so their spend never surfaced locally. The TUI calls this
// per configured remote and folds the totals into the displayed figures.
//
// Returns nil with no error when the remote returns empty output (older
// agent-deck builds that predate `costs summary --json`). Callers should
// treat a nil summary as "remote not available; render local-only totals".
func (r *SSHRunner) FetchCostSummary(ctx context.Context) (*costs.RemoteCostSummary, error) {
	output, err := r.Run(ctx, "costs", "summary", "--json")
	if err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, nil
	}

	var summary costs.RemoteCostSummary
	if err := json.Unmarshal(trimmed, &summary); err != nil {
		return nil, fmt.Errorf("failed to parse remote cost summary: %w", err)
	}
	return &summary, nil
}

// DetectPlatform returns the remote host's OS and architecture (e.g., "linux", "amd64").
func (r *SSHRunner) DetectPlatform(ctx context.Context) (goos, goarch string, err error) {
	if err := ValidateSSHHost(r.Host); err != nil {
		return "", "", err
	}
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

// defaultRemoteInstallSubpath mirrors where install.sh places the binary,
// relative to the remote user's $HOME (#1171).
const defaultRemoteInstallSubpath = ".local/bin/agent-deck"

// remoteExec runs a raw command string on the remote shell via ssh, optionally
// piping stdin (used to stream the binary during deploy). Stubbable in tests
// via remoteExecFn so the update path needs no real remote (#1171).
func (r *SSHRunner) remoteExec(ctx context.Context, remoteCmd string, stdin []byte) ([]byte, error) {
	if r.remoteExecFn != nil {
		return r.remoteExecFn(ctx, remoteCmd, stdin)
	}
	if err := ValidateSSHHost(r.Host); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(sshControlDir, 0700)

	sshArgs := r.sshBaseArgs(remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("remote command failed: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// parseRemoteVersion extracts the version from `agent-deck version` output,
// e.g. "Agent Deck v0.20.2" -> "0.20.2".
func parseRemoteVersion(raw string) string {
	out := strings.TrimSpace(raw)
	if idx := strings.LastIndex(out, "v"); idx >= 0 {
		return strings.TrimSpace(out[idx+1:])
	}
	return out
}

// versionAt runs `<path> version` on the remote and parses the reported
// version. found is false when the binary cannot be executed (missing/not on
// $PATH).
func (r *SSHRunner) versionAt(ctx context.Context, path string) (version string, found bool) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := r.remoteExec(timeoutCtx, shellQuote(path)+" version", nil)
	if err != nil {
		return "", false
	}
	return parseRemoteVersion(string(out)), true
}

// CheckBinary reports the version of agent-deck as found on the remote's $PATH.
// Returns found=false if the binary is not installed / not on $PATH.
func (r *SSHRunner) CheckBinary(ctx context.Context) (version string, found bool) {
	return r.versionAt(ctx, r.AgentDeckPath)
}

// remoteHome resolves the remote user's $HOME, or "" on failure.
func (r *SSHRunner) remoteHome(ctx context.Context) string {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := r.remoteExec(timeoutCtx, `printf %s "$HOME"`, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// expandHome replaces a leading ~ / $HOME in path with the remote user's home,
// so the result is an absolute path safe to shell-quote. Returns path unchanged
// if it is already absolute or $HOME cannot be resolved.
func (r *SSHRunner) expandHome(ctx context.Context, path string) string {
	rest := ""
	switch {
	case path == "~" || path == "$HOME":
		rest = ""
	case strings.HasPrefix(path, "~/"):
		rest = path[1:] // keep leading "/"
	case strings.HasPrefix(path, "$HOME/"):
		rest = path[len("$HOME"):]
	default:
		return path
	}
	home := r.remoteHome(ctx)
	if home == "" {
		return path
	}
	return strings.TrimRight(home, "/") + rest
}

// ResolveRemotePath determines the absolute filesystem path the remote actually
// executes agent-deck from. This is the heart of the #1171 fix: deploying to a
// bare relative name ("agent-deck") landed the binary in ~/agent-deck while the
// remote ran ~/.local/bin/agent-deck from its $PATH. Resolution order:
//  1. an explicit agent_deck_path from config (with ~ expanded), else
//  2. `command -v agent-deck` — the binary the remote's $PATH actually runs, else
//  3. the install.sh default: $HOME/.local/bin/agent-deck.
func (r *SSHRunner) ResolveRemotePath(ctx context.Context) string {
	if p := strings.TrimSpace(r.configuredPath); p != "" {
		return r.expandHome(ctx, p)
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := r.remoteExec(probeCtx, "command -v agent-deck 2>/dev/null", nil); err == nil {
		// command -v can emit multiple lines; take the first absolute path.
		for _, line := range strings.Split(string(out), "\n") {
			if p := strings.TrimSpace(line); strings.HasPrefix(p, "/") {
				return p
			}
		}
	}

	if home := r.remoteHome(ctx); home != "" {
		return strings.TrimRight(home, "/") + "/" + defaultRemoteInstallSubpath
	}
	return "~/" + defaultRemoteInstallSubpath
}

// DeployBinary streams binaryData to remotePath on the remote, creating the
// parent directory and marking it executable. It pipes through `ssh "cat > ..."`
// rather than scp so the remote shell handles the path uniformly; remotePath is
// expected to be absolute (see ResolveRemotePath) (#1171).
func (r *SSHRunner) DeployBinary(ctx context.Context, binaryData []byte, remotePath string) error {
	dir := remotePath
	if idx := strings.LastIndex(remotePath, "/"); idx > 0 {
		dir = remotePath[:idx]
	}

	// Stage to a sibling temp file and atomically rename it into place rather
	// than redirecting onto remotePath directly. agent-deck keeps a long-lived
	// `session attach` process running from remotePath, so truncating it in
	// place (`cat > remotePath`) makes the kernel reject the write with ETXTBSY
	// ("text file busy"). rename(2) only repoints the directory entry, so it
	// succeeds while the old binary is still executing (the running process
	// keeps the now-unlinked inode); the next launch picks up the new binary.
	tmpPath := remotePath + ".new"
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s && chmod +x %s && mv -f %s %s",
		shellQuote(dir), shellQuote(tmpPath), shellQuote(tmpPath),
		shellQuote(tmpPath), shellQuote(remotePath))

	deployCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if _, err := r.remoteExec(deployCtx, cmd, binaryData); err != nil {
		return fmt.Errorf("failed to deploy binary to %s: %w", remotePath, err)
	}
	return nil
}

// InstallBinary resolves the remote's real agent-deck path, deploys binaryData
// there, then verifies the remote actually runs expectedVersion from its $PATH.
// It returns an actionable error instead of a false success when the deployed
// binary is not the one the remote executes (#1171).
func (r *SSHRunner) InstallBinary(ctx context.Context, binaryData []byte, expectedVersion string) error {
	path := r.ResolveRemotePath(ctx)
	if err := r.DeployBinary(ctx, binaryData, path); err != nil {
		return err
	}

	want := strings.TrimPrefix(expectedVersion, "v")

	// The binary the remote actually runs: bare `agent-deck` through its $PATH.
	if pathVer, found := r.versionAt(ctx, "agent-deck"); found && pathVer == want {
		return nil
	} else if found {
		// Something is on $PATH but it is not what we just deployed.
		return fmt.Errorf("deployed v%s to %s, but the remote runs v%s from $PATH — "+
			"set agent_deck_path to the $PATH binary or fix the remote's PATH", want, path, pathVer)
	}

	// Nothing on $PATH. If the deployed binary itself reports the right version,
	// the install worked but the location is not on $PATH yet.
	if deployedVer, found := r.versionAt(ctx, path); found && deployedVer == want {
		return fmt.Errorf("installed v%s at %s, but it is not on the remote's $PATH — "+
			"add %s to PATH or set agent_deck_path to a $PATH location", want, path, path)
	}

	return fmt.Errorf("post-deploy verification failed: remote does not report v%s at %s or on $PATH", want, path)
}

// sshConnOpts returns the SSH -o options shared by every connection agent-deck
// makes. They are the single source of truth for agent-deck's host-key stance:
//
//   - Host-key checking is left at ssh's secure default. agent-deck NEVER passes
//     StrictHostKeyChecking=no and NEVER points UserKnownHostsFile at /dev/null,
//     so an unknown or changed host key surfaces ssh's "Host key verification
//     failed" error (verified against the user's ~/.ssh/known_hosts) instead of
//     being silently trusted — MITM protection.
//   - BatchMode=yes makes that failure fast and non-interactive on EVERY path
//     (run, stream, deploy, and attach), so an unknown key or a missing
//     credential errors clearly instead of hanging on a prompt.
//   - ConnectTimeout bounds the dial.
//
// See the README "Remote Instances" section for the documented assumption.
func (r *SSHRunner) sshConnOpts() []string {
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + sshControlDir + "/%r@%h:%p",
		"-o", "ControlPersist=600",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
	}
}

// ValidateSSHHost rejects host strings ssh would misinterpret as options rather
// than a destination. A host beginning with "-" (e.g. "-oProxyCommand=…") is
// argument injection: passed as a discrete argv element, ssh parses it as a
// flag and can be coerced into running an arbitrary local command. Whitespace
// and empty hosts are rejected too. Hosts come from the user's own config, but
// this closes the option-injection vector cheaply (#1206).
func ValidateSSHHost(host string) error {
	h := strings.TrimSpace(host)
	if h == "" {
		return fmt.Errorf("ssh host is empty")
	}
	if strings.HasPrefix(h, "-") {
		return fmt.Errorf("invalid ssh host %q: must not begin with '-' (ssh would parse it as an option)", host)
	}
	if strings.ContainsAny(h, " \t\r\n") {
		return fmt.Errorf("invalid ssh host %q: must not contain whitespace", host)
	}
	return nil
}

// sshBaseArgs returns common SSH args for running a raw command on the remote.
func (r *SSHRunner) sshBaseArgs(remoteCmd string) []string {
	return append(r.sshConnOpts(), r.Host, remoteCmd)
}

// buildAttachArgs builds the ssh argv for an interactive attach. It shares
// sshConnOpts() with every other path so the host-key/BatchMode stance is
// identical (#1206 regression: Attach() previously omitted BatchMode and
// ConnectTimeout, so an unknown host key could hang on a prompt instead of
// failing fast). "-tt" forces a remote PTY.
func (r *SSHRunner) buildAttachArgs(sessionID string) []string {
	remoteCmd := r.buildRemoteCommand("session", "attach", sessionID)
	args := append([]string{"-tt"}, r.sshConnOpts()...)
	return append(args, r.Host, remoteCmd)
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
		// Compensate: the remote DB has the row but no tmux process. Best-effort
		// delete with a fresh context so an upstream cancellation doesn't skip
		// the cleanup. Surface the original start failure.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = r.DeleteSession(cleanupCtx, result.ID)
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

// RemoteLatency is a live round-trip-time sample for a configured remote.
// Tracked per remote host (not per session) — multiple sessions on the same
// host share the same connection, so latency is a host-level metric. See
// issue #1103.
type RemoteLatency struct {
	// MS is the round-trip time in milliseconds. Meaningful only when
	// Offline is false.
	MS int
	// Offline is true when the most recent measurement attempt failed
	// (network error, SSH dead, remote agent-deck binary missing, etc).
	Offline bool
	// MeasuredAt is when the sample was taken; zero value means never measured.
	MeasuredAt time.Time
}

// MeasureLatency measures the round-trip time of a lightweight noop call
// to the remote agent-deck binary. Returns the elapsed duration on success.
//
// Implementation note: we run `agent-deck --version` because it is the
// cheapest possible call (no DB read, no tmux probe, no network back to
// services). The ControlMaster socket is persisted across calls so we
// measure mostly network RTT after the first hit, which is exactly what
// the user wants to see in the header per #1103.
func (r *SSHRunner) MeasureLatency(ctx context.Context) (time.Duration, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := r.run(timeoutCtx, "--version"); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}
