package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// RemoteSwitchPreview is the remote deck's `session switch-preview --json`
// answer. The remote runs the same read-only preview a local session gets,
// against its own config.toml, transcripts and registry: account status and
// refusals describe the remote host, never this controller.
type RemoteSwitchPreview struct {
	SourceTitle         string               `json:"source_title"`
	SourceTool          string               `json:"source_tool"`
	SourceAccount       string               `json:"source_account"`
	SourceAccountStatus string               `json:"source_account_status"`
	SourceSessionID     string               `json:"source_session_id"`
	TargetHarness       string               `json:"target_harness"`
	TargetAccount       string               `json:"target_account"`
	TargetAccountStatus string               `json:"target_account_status"`
	Capability          string               `json:"capability"`
	Execution           string               `json:"execution"`
	Inclusions          []string             `json:"fidelity_inclusions"`
	Exclusions          []string             `json:"fidelity_exclusions"`
	Refusal             *RemoteSwitchRefusal `json:"refusal,omitempty"`
	Warnings            []string             `json:"warnings,omitempty"`
}

// RemoteSwitchRefusal mirrors SwitchRefusal as the remote reports it.
type RemoteSwitchRefusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RemoteSwitchResult is the remote deck's `session switch --json` answer for
// either shape (same-harness native switch or a distinct cross-harness
// target). Only the fields the controller reports are decoded.
type RemoteSwitchResult struct {
	Success          bool   `json:"success"`
	Status           string `json:"status"`
	Pending          bool   `json:"pending"`
	RecoveryRequired bool   `json:"recovery_required"`
	Error            string `json:"error,omitempty"`
	Code             string `json:"code,omitempty"`
	OperationID      string `json:"operation_id,omitempty"`
	TargetID         string `json:"target_id,omitempty"`
	TargetCreated    bool   `json:"target_created"`
	TargetReady      bool   `json:"target_ready"`
	// SourceArchived reports what the remote did to the source row: a ready
	// cross-harness target supersedes it (archived, reversible); a pending
	// or failed transfer and every native switch leave it as it was. Sent by
	// remotes from 1.16.10; older remotes omit it and the controller then
	// derives it from TargetReady, which is the engine's own rule.
	SourceArchived     *bool    `json:"source_archived,omitempty"`
	SourceSupersededBy string   `json:"source_superseded_by,omitempty"`
	MissingContract    string   `json:"missing_contract,omitempty"`
	ID                 string   `json:"id,omitempty"`
	Title              string   `json:"title,omitempty"`
	OldTool            string   `json:"old_tool,omitempty"`
	NewTool            string   `json:"new_tool,omitempty"`
	OldAccount         string   `json:"old_account"`
	NewAccount         string   `json:"new_account"`
	Continuity         string   `json:"continuity,omitempty"`
	DestinationReady   bool     `json:"destination_ready"`
	Restarted          bool     `json:"restarted"`
	LossDisclosure     []string `json:"loss_disclosure,omitempty"`
}

// ValidateRemoteSwitchSelector admits the session selector (id or title) a
// remote switch request may carry: printable letters, digits, space and
// ._-, no leading dash, no path separator, at most 200 bytes. Anything else
// is refused before any exec, on both the CLI passthrough and the TUI path.
func ValidateRemoteSwitchSelector(selector string) error {
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("session selector is empty")
	}
	if len(selector) > 200 {
		return fmt.Errorf("session selector is longer than 200 bytes")
	}
	if strings.HasPrefix(selector, "-") {
		return fmt.Errorf("session selector %q must not start with a dash", selector)
	}
	for _, r := range selector {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("session selector %q contains %q; only letters, digits, space, '.', '_' and '-' are forwarded", selector, r)
		}
	}
	return nil
}

// ValidateRemoteSwitchToken admits a harness or account name forwarded to a
// remote: a single [A-Za-z0-9._-] token that is not path- or flag-shaped.
// Config directories are resolved by the remote from these names; a path
// never travels. Empty is the target default and is not sent at all.
func ValidateRemoteSwitchToken(kind, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 100 {
		return fmt.Errorf("%s %q is longer than 100 bytes", kind, value)
	}
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, ".") {
		return fmt.Errorf("%s %q must not start with '-' or '.'", kind, value)
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("%s %q contains %q; only [A-Za-z0-9._-] names are forwarded (a path is never sent to a remote)", kind, value, r)
		}
	}
	return nil
}

func validateRemoteSwitchRequest(sessionID, harness, account string) error {
	if err := ValidateRemoteSwitchSelector(sessionID); err != nil {
		return err
	}
	if err := ValidateRemoteSwitchToken("harness", strings.TrimSpace(harness)); err != nil {
		return err
	}
	return ValidateRemoteSwitchToken("account", strings.TrimSpace(account))
}

// remoteSwitchTargetArgs renders the explicit target the way the local CLI
// takes it. Empty values are the target defaults and are not sent.
func remoteSwitchTargetArgs(harness, account string) []string {
	var args []string
	if harness = strings.TrimSpace(harness); harness != "" {
		args = append(args, "--to-harness", harness)
	}
	if account = strings.TrimSpace(account); account != "" {
		args = append(args, "--to-account", account)
	}
	return args
}

// SwitchPreview asks the remote deck what switching one of its sessions to
// harness/account would do, through its own `session switch-preview --json`.
// A refusal is decoded and returned as a preview (the remote exits 1 with the
// JSON still on stdout); only missing or undecodable output is an error, so
// an old remote, an unknown session or an unreachable host never pass as a
// clean preflight.
func (r *SSHRunner) SwitchPreview(ctx context.Context, sessionID, harness, account string) (*RemoteSwitchPreview, error) {
	if err := validateRemoteSwitchRequest(sessionID, harness, account); err != nil {
		return nil, err
	}
	args := append([]string{"session", "switch-preview", sessionID}, remoteSwitchTargetArgs(harness, account)...)
	output, runErr := r.Run(ctx, append(args, "--json")...)
	var preview RemoteSwitchPreview
	if err := decodeRemoteJSON(output, &preview); err != nil {
		if runErr != nil {
			return nil, runErr
		}
		return nil, fmt.Errorf("remote switch-preview: %w", err)
	}
	if runErr != nil && preview.Refusal == nil {
		return nil, runErr
	}
	return &preview, nil
}

// SwitchSession runs the remote deck's own `session switch` for one of its
// sessions. confirmContextLoss is forwarded for a cross-harness transfer
// after the user accepted the preview's loss disclosure. The remote's
// registry, transcripts, config dirs and credentials never leave that host.
//
// A pending result exits 0 and is returned as-is. A failed switch exits 1
// with a JSON error body; that body is decoded and returned alongside the
// error so callers can report recovery_required and the remote's message.
func (r *SSHRunner) SwitchSession(ctx context.Context, sessionID, harness, account string, confirmContextLoss bool) (*RemoteSwitchResult, error) {
	if err := validateRemoteSwitchRequest(sessionID, harness, account); err != nil {
		return nil, err
	}
	args := append([]string{"session", "switch", sessionID}, remoteSwitchTargetArgs(harness, account)...)
	if confirmContextLoss {
		args = append(args, "--confirm-context-loss")
	}
	output, runErr := r.Run(ctx, append(args, "--json")...)
	var result RemoteSwitchResult
	if err := decodeRemoteJSON(output, &result); err != nil {
		if runErr != nil {
			return nil, runErr
		}
		return nil, fmt.Errorf("remote switch: %w", err)
	}
	if runErr != nil {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			return &result, runErr
		}
		return &result, fmt.Errorf("remote switch failed: %s", message)
	}
	return &result, nil
}

// SourceWasArchived reports whether the remote archived (superseded) the
// source row. The explicit field wins; a remote too old to send it archives
// exactly when the cross-harness target became ready.
func (r *RemoteSwitchResult) SourceWasArchived(cross bool) bool {
	if r == nil {
		return false
	}
	if r.SourceArchived != nil {
		return *r.SourceArchived
	}
	return cross && r.TargetReady
}

// FetchAccountsForHarness lists the named account slots the remote has
// configured for harness (its `accounts`), so the Edit Session dialog on a
// remote row offers the server's slots, never this machine's. Claude keeps
// the original `accounts --json` form a 1.16.x remote answers; Codex needs
// the harness filter. Pi has no named slots. Only names travel back.
func (r *SSHRunner) FetchAccountsForHarness(ctx context.Context, harness string) ([]string, error) {
	switch canonicalSwitchHarness(harness) {
	case "claude":
		return r.FetchAccounts(ctx)
	case "codex":
		output, err := r.Run(ctx, "accounts", "--harness", "codex", "--json")
		if err != nil {
			return nil, err
		}
		return parseRemoteAccountNames(output)
	default:
		return nil, nil
	}
}

// decodeRemoteJSON decodes the first JSON object on the remote's stdout.
// Anything else (a banner, a usage line from an older binary, nothing) is
// reported so no caller mistakes it for an answer.
func decodeRemoteJSON(output []byte, v any) error {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("unexpected remote output: %q", truncateRemoteOutput(string(trimmed)))
	}
	return json.NewDecoder(bytes.NewReader(trimmed)).Decode(v)
}

func truncateRemoteOutput(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
