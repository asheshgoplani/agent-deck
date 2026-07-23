package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

const (
	RemoteKindSSH      = "ssh"
	RemoteKindAgentbox = "agentbox"
)

// IsCanonicalAgentboxAgent reports whether agent names one of the runtimes
// exposed by the Agentbox workspace image.
func IsCanonicalAgentboxAgent(agent string) bool {
	switch strings.TrimSpace(agent) {
	case "claude-code", "codex", "pi-fireworks":
		return true
	default:
		return false
	}
}

type RemoteCreateOptions struct {
	Tool         string
	Title        string
	Path         string
	Group        string
	ModelID      string
	Orchestrator string
	Agent        string
	Runtime      string
}

type RemoteCreateResult struct {
	SessionID          string
	Attachable         bool
	AttachCommand      string
	LocalAttachCommand string
}

type RemoteRunner interface {
	FetchSessions(ctx context.Context) ([]RemoteSessionInfo, error)
	FetchSessionOutput(ctx context.Context, sessionID string) (string, error)
	FetchSessionPane(ctx context.Context, sessionID string) (string, error)
	FetchCostSummary(ctx context.Context) (*costs.RemoteCostSummary, error)
	MeasureLatency(ctx context.Context) (time.Duration, error)
	CreateSession(ctx context.Context, opts RemoteCreateOptions) (RemoteCreateResult, error)
	DeleteSession(ctx context.Context, sessionID string) error
	StopSession(ctx context.Context, sessionID string) error
	RestartSession(ctx context.Context, sessionID string) error
	RenameSession(ctx context.Context, sessionID string, newTitle string) error
	Attach(sessionID string) error
}

func NewRemoteRunner(name string, rc RemoteConfig) RemoteRunner {
	if rc.GetKind() == RemoteKindAgentbox {
		return NewAgentboxRunner(name, rc)
	}
	return NewSSHRunner(name, rc)
}

type AgentboxRunner struct {
	RemoteName string
	BaseURL    string
	Token      string

	client      *http.Client
	execCommand func(command string) error
	hostname    func() (string, error)
}

func NewAgentboxRunner(name string, rc RemoteConfig) *AgentboxRunner {
	return &AgentboxRunner{
		RemoteName: name,
		BaseURL:    strings.TrimRight(strings.TrimSpace(rc.GetURL()), "/"),
		Token:      strings.TrimSpace(rc.Token),
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		execCommand: runLocalAttachCommand,
		hostname:    os.Hostname,
	}
}

type agentboxWorkspace struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Orchestrator       string `json:"orchestrator"`
	Agent              string `json:"agent"`
	Model              string `json:"model"`
	Runtime            string `json:"runtime"`
	Cwd                string `json:"cwd"`
	Status             string `json:"status"`
	AttachCommand      string `json:"attachCommand"`
	LocalAttachCommand string `json:"localAttachCommand"`
	ClaimedTaskCount   int    `json:"claimedTaskCount"`
	CreatedAt          string `json:"createdAt"`
}

type agentboxAttachResponse struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	AttachCommand      string `json:"attachCommand"`
	LocalAttachCommand string `json:"localAttachCommand"`
}

type agentboxErrorResponse struct {
	Error   string `json:"error"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ResolvedAttachCommand struct {
	Command string
	Local   bool
}

type AgentboxHTTPError struct {
	Method         string
	RequestPath    string
	StatusCode     int
	RemoteError    string
	WorkspaceState string
	Message        string
}

func (e *AgentboxHTTPError) Error() string {
	return fmt.Sprintf("agentbox %s %s: %s", e.Method, e.RequestPath, e.Message)
}

func (r *AgentboxRunner) FetchSessions(ctx context.Context) ([]RemoteSessionInfo, error) {
	var workspaces []agentboxWorkspace
	if err := r.doJSON(ctx, http.MethodGet, "/v1/workspaces", nil, &workspaces); err != nil {
		return nil, err
	}
	sessions := make([]RemoteSessionInfo, 0, len(workspaces))
	for _, workspace := range workspaces {
		sessions = append(sessions, r.workspaceToRemoteSession(workspace))
	}
	return sessions, nil
}

func (r *AgentboxRunner) FetchSessionOutput(ctx context.Context, sessionID string) (string, error) {
	return "", fmt.Errorf("agentbox remote preview is unavailable for workspace %s", sessionID)
}

func (r *AgentboxRunner) FetchSessionPane(ctx context.Context, sessionID string) (string, error) {
	return "", fmt.Errorf("agentbox remote pane preview is unavailable for workspace %s", sessionID)
}

func (r *AgentboxRunner) FetchCostSummary(ctx context.Context) (*costs.RemoteCostSummary, error) {
	return nil, nil
}

func (r *AgentboxRunner) MeasureLatency(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	var workspaces []agentboxWorkspace
	if err := r.doJSON(ctx, http.MethodGet, "/v1/workspaces", nil, &workspaces); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

func (r *AgentboxRunner) CreateSession(ctx context.Context, opts RemoteCreateOptions) (RemoteCreateResult, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return RemoteCreateResult{}, fmt.Errorf("agentbox create requires --name")
	}
	if strings.TrimSpace(opts.Orchestrator) == "" {
		return RemoteCreateResult{}, fmt.Errorf("agentbox create requires --orchestrator")
	}
	if strings.TrimSpace(opts.Agent) == "" {
		return RemoteCreateResult{}, fmt.Errorf("agentbox create requires --agent")
	}
	if !IsCanonicalAgentboxAgent(opts.Agent) {
		return RemoteCreateResult{}, fmt.Errorf("agentbox agent must be exactly one of: claude-code, codex, or pi-fireworks")
	}
	if strings.TrimSpace(opts.ModelID) == "" {
		return RemoteCreateResult{}, fmt.Errorf("agentbox create requires --model")
	}
	if strings.TrimSpace(opts.Runtime) == "" {
		return RemoteCreateResult{}, fmt.Errorf("agentbox create requires --runtime")
	}

	payload := map[string]string{
		"name":         strings.TrimSpace(opts.Title),
		"orchestrator": strings.TrimSpace(opts.Orchestrator),
		"agent":        strings.TrimSpace(opts.Agent),
		"model":        strings.TrimSpace(opts.ModelID),
		"runtime":      strings.TrimSpace(opts.Runtime),
	}
	if cwd := strings.TrimSpace(opts.Path); cwd != "" && cwd != "." {
		payload["cwd"] = cwd
	}

	var workspace agentboxWorkspace
	if err := r.doJSON(ctx, http.MethodPost, "/v1/workspaces", payload, &workspace); err != nil {
		return RemoteCreateResult{}, err
	}
	if workspace.ID == "" {
		return RemoteCreateResult{}, fmt.Errorf("agentbox create returned an empty workspace ID")
	}
	sessionInfo := r.workspaceToRemoteSession(workspace)
	return RemoteCreateResult{
		SessionID:          workspace.ID,
		Attachable:         sessionInfo.Attachable,
		AttachCommand:      sessionInfo.AttachCommand,
		LocalAttachCommand: sessionInfo.LocalAttachCommand,
	}, nil
}

func (r *AgentboxRunner) DeleteSession(ctx context.Context, sessionID string) error {
	return r.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/workspaces/%s/destroy", url.PathEscape(sessionID)), map[string]bool{"force": false}, nil)
}

func (r *AgentboxRunner) StopSession(ctx context.Context, sessionID string) error {
	return r.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/workspaces/%s/stop", url.PathEscape(sessionID)), map[string]any{}, nil)
}

func (r *AgentboxRunner) RestartSession(ctx context.Context, sessionID string) error {
	return r.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/workspaces/%s/start", url.PathEscape(sessionID)), map[string]any{}, nil)
}

func (r *AgentboxRunner) RenameSession(ctx context.Context, sessionID string, newTitle string) error {
	return fmt.Errorf("agentbox workspaces do not support rename through agent-deck yet")
}

func (r *AgentboxRunner) ResolveAttach(ctx context.Context, sessionID string) (ResolvedAttachCommand, error) {
	var response agentboxAttachResponse
	if err := r.doJSON(ctx, http.MethodGet, fmt.Sprintf("/v1/workspaces/%s/attach", url.PathEscape(sessionID)), nil, &response); err != nil {
		return ResolvedAttachCommand{}, err
	}
	useLocal := r.shouldUseLocalAttachCommand()
	command := strings.TrimSpace(response.AttachCommand)
	if useLocal && strings.TrimSpace(response.LocalAttachCommand) != "" {
		command = strings.TrimSpace(response.LocalAttachCommand)
	}
	if command == "" {
		return ResolvedAttachCommand{}, fmt.Errorf("agentbox attach for workspace %s returned no attach command", sessionID)
	}
	return ResolvedAttachCommand{Command: command, Local: useLocal}, nil
}

func (r *AgentboxRunner) Attach(sessionID string) error {
	intent, err := r.ResolveAttach(context.Background(), sessionID)
	if err != nil {
		return err
	}
	return r.execCommand(intent.Command)
}

func (r *AgentboxRunner) AttachCreatedResult(result RemoteCreateResult) error {
	useLocal := r.shouldUseLocalAttachCommand()
	command := strings.TrimSpace(result.AttachCommand)
	if useLocal && strings.TrimSpace(result.LocalAttachCommand) != "" {
		command = strings.TrimSpace(result.LocalAttachCommand)
	}
	if command == "" {
		return fmt.Errorf("agentbox create for workspace %s returned no attach command", result.SessionID)
	}
	return r.execCommand(command)
}

func (r *AgentboxRunner) workspaceToRemoteSession(workspace agentboxWorkspace) RemoteSessionInfo {
	attachable := workspace.Status == "running" &&
		(strings.TrimSpace(workspace.AttachCommand) != "" || strings.TrimSpace(workspace.LocalAttachCommand) != "")
	return RemoteSessionInfo{
		ID:                 workspace.ID,
		Title:              workspace.Name,
		Path:               workspace.Cwd,
		Group:              workspace.Orchestrator,
		Tool:               normalizeAgentboxTool(workspace.Agent),
		Agent:              workspace.Agent,
		Model:              workspace.Model,
		Runtime:            workspace.Runtime,
		Orchestrator:       workspace.Orchestrator,
		Status:             remoteStatusFromLifecycle(workspace.Status),
		LifecycleStatus:    workspace.Status,
		ClaimedTaskCount:   workspace.ClaimedTaskCount,
		Attachable:         attachable,
		AttachCommand:      workspace.AttachCommand,
		LocalAttachCommand: workspace.LocalAttachCommand,
		CreatedAt:          workspace.CreatedAt,
		RemoteName:         r.RemoteName,
	}
}

func normalizeAgentboxTool(agent string) string {
	switch strings.TrimSpace(agent) {
	case "claude", "claude-code":
		return "claude"
	case "pi", "pi-fireworks":
		return "pi"
	default:
		return strings.TrimSpace(agent)
	}
}

func remoteStatusFromLifecycle(status string) string {
	switch strings.TrimSpace(status) {
	case "running":
		return "running"
	case "stopped":
		return "stopped"
	case "cleanup_required", "destroying", "destroyed":
		return "error"
	default:
		return strings.TrimSpace(status)
	}
}

func (r *AgentboxRunner) doJSON(ctx context.Context, method string, requestPath string, body any, out any) error {
	if err := validateAgentboxURL(r.BaseURL); err != nil {
		return err
	}

	fullURL := r.BaseURL + requestPath
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("agentbox %s %s: encode request: %w", method, requestPath, err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, payload)
	if err != nil {
		return fmt.Errorf("agentbox %s %s: build request: %w", method, requestPath, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("agentbox %s %s: %w", method, requestPath, err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("agentbox %s %s: read response: %w", method, requestPath, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return translateAgentboxHTTPError(method, requestPath, resp.StatusCode, responseBody)
	}
	if out == nil || len(bytes.TrimSpace(responseBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("agentbox %s %s: decode response: %w", method, requestPath, err)
	}
	return nil
}

func validateAgentboxURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("agentbox remote is missing a URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid agentbox URL %q: %w", raw, err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("invalid agentbox URL %q: missing host", raw)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("invalid agentbox URL %q: unsupported scheme %q", raw, parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("invalid agentbox URL %q: remote cleartext HTTP is only allowed for localhost", raw)
}

func translateAgentboxHTTPError(method string, requestPath string, statusCode int, body []byte) error {
	var payload agentboxErrorResponse
	_ = json.Unmarshal(body, &payload)

	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = strings.TrimSpace(payload.Error)
	}
	switch payload.Error {
	case "workspace_not_running":
		if payload.Status != "" {
			message = fmt.Sprintf("workspace is %s; start it before attaching", payload.Status)
		} else {
			message = "workspace is not running"
		}
	case "workspace_not_attachable":
		if payload.Status != "" {
			message = fmt.Sprintf("workspace is %s and cannot be attached", payload.Status)
		} else {
			message = "workspace cannot be attached"
		}
	case "workspace_runtime_unavailable":
		message = "workspace runtime is unavailable"
	case "workspace_running":
		message = "workspace is still running; stop it before destroying"
	case "workspace_destroyed":
		message = "workspace is destroyed"
	case "workspaces_unavailable":
		message = "agentbox workspaces are unavailable"
	case "workspace_root_unconfigured":
		message = "agentbox workspace root is unconfigured"
	case "workspace_root_conflict":
		message = "agentbox workspace root conflicts with an existing checkout"
	case "workspace_disk_exhausted":
		message = "agentbox workspace disk budget is exhausted"
	case "workspace_unavailable":
		message = "agentbox workspace is unavailable"
	case "invalid_request":
		message = "agentbox rejected the request"
	case "invalid_state":
		if message == "" || message == payload.Error {
			message = "agentbox rejected the workspace state transition"
		}
	case "not_found":
		message = "workspace not found"
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &AgentboxHTTPError{
		Method:         method,
		RequestPath:    requestPath,
		StatusCode:     statusCode,
		RemoteError:    strings.TrimSpace(payload.Error),
		WorkspaceState: strings.TrimSpace(payload.Status),
		Message:        message,
	}
}

func (r *AgentboxRunner) shouldUseLocalAttachCommand() bool {
	parsed, err := url.Parse(r.BaseURL)
	if err != nil {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	localHost, err := r.hostname()
	if err != nil {
		return false
	}
	localHost = strings.TrimSpace(localHost)
	if localHost == "" {
		return false
	}
	return strings.EqualFold(host, localHost) || strings.HasPrefix(strings.ToLower(host), strings.ToLower(localHost)+".")
}

func (r *AgentboxRunner) ResolveListedAttach(sessionInfo RemoteSessionInfo) (ResolvedAttachCommand, error) {
	command := strings.TrimSpace(sessionInfo.AttachCommand)
	useLocal := r.shouldUseLocalAttachCommand()
	if useLocal && strings.TrimSpace(sessionInfo.LocalAttachCommand) != "" {
		command = strings.TrimSpace(sessionInfo.LocalAttachCommand)
	}
	if command == "" {
		if strings.TrimSpace(sessionInfo.LifecycleStatus) != "" {
			return ResolvedAttachCommand{}, fmt.Errorf("workspace is %s; start it before attaching", sessionInfo.LifecycleStatus)
		}
		return ResolvedAttachCommand{}, fmt.Errorf("agentbox attach for workspace %s returned no attach command", sessionInfo.ID)
	}
	return ResolvedAttachCommand{Command: command, Local: useLocal}, nil
}

func runLocalAttachCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("empty attach command")
	}
	cmd := exec.Command("sh", "-lc", command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
