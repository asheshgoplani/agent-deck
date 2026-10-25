// Package provider runs sandbox providers: external commands that speak the
// agent-deck provider protocol (version 1) to create, list, inspect and
// destroy sandboxes, and registers a created sandbox as an SSH remote.
//
// A provider is configured as [providers.<name>] (session.ProviderConfig).
// agent-deck runs its command with exactly one argument, the action
// (create, list, status, destroy); the request is one JSON object on stdin
// and the response one JSON object on stdout. Nothing else goes in argv, so
// names, ids, labels and anything the provider treats as secret never show
// on a command line. The command runs in its own process group, which is
// killed whole on timeout. Provider output is untrusted and validated
// (Sandbox.Validate) before anything is written from it.
//
// This package imports internal/session; session must never import it.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ProtocolVersion is the provider protocol agent-deck speaks.
const ProtocolVersion = 1

// Protocol actions: the only argv a provider command ever receives.
const (
	ActionCreate  = "create"
	ActionList    = "list"
	ActionStatus  = "status"
	ActionDestroy = "destroy"
)

const (
	// maxStdout caps a provider response.
	maxStdout = 1 << 20
	// maxStderr caps the stderr kept for an error message.
	maxStderr = 64 << 10
	// maxErrorText caps the provider text quoted in an error, in runes.
	maxErrorText = 2000
	// killWait bounds how long Wait may take after the group is killed
	// (pipes held open by a grandchild that escaped the group).
	killWait = 3 * time.Second
)

var providerLog = logging.ForComponent("provider")

// Auth says how ssh authenticates to a sandbox.
type Auth struct {
	// Type is "key" (KeyPath names a private key file) or "agent" (the
	// user's ssh agent).
	Type    string `json:"type"`
	KeyPath string `json:"key_path,omitempty"`
}

// Sandbox is one provider sandbox, as a provider reports it.
type Sandbox struct {
	ID        string            `json:"id"`
	Host      string            `json:"host"`
	Port      int               `json:"port,omitempty"`
	User      string            `json:"user"`
	Auth      Auth              `json:"auth"`
	Labels    map[string]string `json:"labels,omitempty"`
	State     string            `json:"state,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	HostKeys  []string          `json:"host_keys,omitempty"`
}

// Client runs one configured provider's command.
type Client struct {
	Name   string
	Config session.ProviderConfig
}

// Lookup returns the client for the provider configured as name.
func Lookup(cfg *session.UserConfig, name string) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("provider %q is not configured (no config loaded)", name)
	}
	pc, ok := cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured (add [providers.%s] to config.toml)", name, name)
	}
	return &Client{Name: name, Config: pc}, nil
}

// Names returns the configured provider names, sorted.
func Names(cfg *session.UserConfig) []string {
	if cfg == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type request struct {
	Protocol int               `json:"protocol"`
	Action   string            `json:"action"`
	Name     string            `json:"name,omitempty"`
	ID       string            `json:"id,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

type response struct {
	Protocol  *int      `json:"protocol"`
	Error     string    `json:"error,omitempty"`
	Sandbox   *Sandbox  `json:"sandbox,omitempty"`
	Sandboxes []Sandbox `json:"sandboxes,omitempty"`
	OK        bool      `json:"ok,omitempty"`
}

// Create asks the provider for a new sandbox named name (the remote name)
// with labels. When the provider created a sandbox but its answer fails
// validation, the sandbox is returned with the error so the caller can
// destroy it; the sandbox is nil only when there is nothing to clean up.
func (c *Client) Create(ctx context.Context, name string, labels map[string]string) (*Sandbox, error) {
	resp, err := c.call(ctx, request{Action: ActionCreate, Name: name, Labels: labels}, c.Config.GetCreateTimeout())
	if err != nil {
		return nil, err
	}
	if resp.Sandbox == nil {
		return nil, fmt.Errorf("provider %q create: response has no sandbox", c.Name)
	}
	sb := resp.Sandbox
	if err := sb.Validate(); err != nil {
		if validID(sb.ID) {
			return sb, fmt.Errorf("provider %q create returned an invalid sandbox: %w", c.Name, err)
		}
		return nil, fmt.Errorf("provider %q create returned an invalid sandbox: %w", c.Name, err)
	}
	return sb, nil
}

// List returns every sandbox the provider reports.
func (c *Client) List(ctx context.Context) ([]Sandbox, error) {
	resp, err := c.call(ctx, request{Action: ActionList}, c.Config.GetTimeout())
	if err != nil {
		return nil, err
	}
	for i := range resp.Sandboxes {
		if err := resp.Sandboxes[i].Validate(); err != nil {
			return nil, fmt.Errorf("provider %q list returned an invalid sandbox: %w", c.Name, err)
		}
	}
	return resp.Sandboxes, nil
}

// Status returns the provider's current view of sandbox id.
func (c *Client) Status(ctx context.Context, id string) (*Sandbox, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid sandbox id %q", id)
	}
	resp, err := c.call(ctx, request{Action: ActionStatus, ID: id}, c.Config.GetTimeout())
	if err != nil {
		return nil, err
	}
	if resp.Sandbox == nil {
		return nil, fmt.Errorf("provider %q status: response has no sandbox", c.Name)
	}
	if err := resp.Sandbox.Validate(); err != nil {
		return nil, fmt.Errorf("provider %q status returned an invalid sandbox: %w", c.Name, err)
	}
	return resp.Sandbox, nil
}

// Destroy asks the provider to destroy sandbox id.
func (c *Client) Destroy(ctx context.Context, id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid sandbox id %q", id)
	}
	resp, err := c.call(ctx, request{Action: ActionDestroy, ID: id}, c.Config.GetTimeout())
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("provider %q destroy did not confirm (no \"ok\":true)", c.Name)
	}
	return nil
}

// commandPath resolves the provider command: an absolute path, or a bare
// name looked up in PATH. A relative path with a separator is refused, since
// it would depend on the working directory.
func (c *Client) commandPath() (string, error) {
	cmd := strings.TrimSpace(c.Config.Command)
	switch {
	case cmd == "":
		return "", fmt.Errorf("provider %q has no command configured", c.Name)
	case filepath.IsAbs(cmd):
		return filepath.Clean(cmd), nil
	case strings.ContainsRune(cmd, filepath.Separator):
		return "", fmt.Errorf("provider %q command %q must be an absolute path or a name on PATH", c.Name, cmd)
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return "", fmt.Errorf("resolving provider %q command %q: %w", c.Name, cmd, err)
	}
	return path, nil
}

// env is the provider's environment: the filtered agent-deck env plus the
// protocol variables (any inherited copies replaced).
func (c *Client) env() []string {
	base := childenv.ForLaunch("")
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "AGENTDECK_PROVIDER_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"AGENTDECK_PROVIDER_PROTOCOL="+strconv.Itoa(ProtocolVersion),
		"AGENTDECK_PROVIDER_NAME="+c.Name,
	)
}

// call runs one protocol action and decodes its response.
func (c *Client) call(ctx context.Context, req request, timeout time.Duration) (*response, error) {
	req.Protocol = ProtocolVersion
	path, err := c.commandPath()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encoding provider %q %s request: %w", c.Name, req.Action, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 -- the command is the user's configured provider, run
	// without a shell; its only argument is a fixed protocol action.
	cmd := exec.CommandContext(runCtx, path, req.Action)
	cmd.Env = c.env()
	cmd.Stdin = bytes.NewReader(body)
	stdout := &capWriter{limit: maxStdout}
	stderr := &capWriter{limit: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = killWait

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	// A provider may leave a helper running (a port forward, say) that still
	// holds stdout; once the provider itself exited 0, what it wrote counts.
	if errors.Is(runErr, exec.ErrWaitDelay) && exitCode == 0 {
		runErr = nil
	}
	// Only the action, provider name, duration and exit code are logged:
	// requests carry labels and responses or stderr may carry anything.
	providerLog.Debug("provider_call",
		slog.String("provider", c.Name),
		slog.String("action", req.Action),
		slog.Int64("duration_ms", elapsed.Milliseconds()),
		slog.Int("exit_code", exitCode))

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, fmt.Errorf("provider %q %s timed out after %s%s", c.Name, req.Action, timeout, stderrSuffix(stderr))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("provider %q %s: %w", c.Name, req.Action, ctxErr)
	}
	if runErr != nil {
		return nil, fmt.Errorf("provider %q %s failed: %w%s", c.Name, req.Action, runErr, stderrSuffix(stderr))
	}
	if stdout.overflow {
		return nil, fmt.Errorf("provider %q %s output exceeds %d bytes", c.Name, req.Action, maxStdout)
	}
	resp, err := decodeResponse(stdout.buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("provider %q %s: %w%s", c.Name, req.Action, err, stderrSuffix(stderr))
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("provider %q %s failed: %s", c.Name, req.Action, sanitize(resp.Error))
	}
	return resp, nil
}

// decodeResponse parses exactly one protocol response object.
func decodeResponse(data []byte) (*response, error) {
	trimmed := bytes.TrimSpace(data)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var resp response
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("decoding provider response: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decoding provider response: trailing data after the JSON object")
	}
	if resp.Protocol == nil {
		return nil, fmt.Errorf("provider response has no protocol field")
	}
	if *resp.Protocol != ProtocolVersion {
		return nil, fmt.Errorf("provider speaks protocol %d, agent-deck speaks %d", *resp.Protocol, ProtocolVersion)
	}
	return &resp, nil
}

func stderrSuffix(w *capWriter) string {
	text := sanitize(w.buf.String())
	if text == "" {
		return ""
	}
	return ": " + text
}

// sanitize makes provider text safe to show: control characters (terminal
// escapes included) dropped, line breaks folded to spaces, length capped.
func sanitize(s string) string {
	s = ansiEscape.ReplaceAllString(strings.ToValidUTF8(s, "?"), "")
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			r = ' '
		case unicode.IsControl(r) || r == 0x7f:
			continue
		}
		if n >= maxErrorText {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ansiEscape matches terminal escape sequences (CSI and OSC).
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?")

// capWriter keeps up to limit bytes and drops the rest, noting the overflow.
type capWriter struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
			w.overflow = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.overflow = true
	}
	return len(p), nil
}
