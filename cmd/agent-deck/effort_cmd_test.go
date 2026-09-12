package main

// CLI parity for the new-session dialog rows: `add` and `launch` accept
// --effort (Reasoning effort) and the Claude Options flags, validate them
// against the tool, and surface them in --json output alongside the model fields.

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestApplyCLIEffortOverride(t *testing.T) {
	claude := session.NewInstanceWithTool("effort-claude", "/tmp/test", "claude")
	if err := applyCLIEffortOverride(claude, " high "); err != nil {
		t.Fatalf("applyCLIEffortOverride(claude, high) error = %v", err)
	}
	if got := claude.LaunchReasoningEffort(); got != "high" {
		t.Fatalf("claude LaunchReasoningEffort() = %q, want high", got)
	}

	codex := session.NewInstanceWithTool("effort-codex", "/tmp/test", "codex")
	if err := applyCLIEffortOverride(codex, "minimal"); err != nil {
		t.Fatalf("applyCLIEffortOverride(codex, minimal) error = %v", err)
	}
	if got := codex.LaunchReasoningEffort(); got != "minimal" {
		t.Fatalf("codex LaunchReasoningEffort() = %q, want minimal", got)
	}

	// Empty is "tool default" and never an error, even for tools without effort.
	shell := session.NewInstanceWithTool("effort-shell", "/tmp/test", "shell")
	if err := applyCLIEffortOverride(shell, ""); err != nil {
		t.Fatalf("empty effort must be a no-op, got %v", err)
	}

	// Values outside the tool's set are refused with the valid options listed.
	err := applyCLIEffortOverride(claude, "turbo")
	if err == nil || !strings.Contains(err.Error(), "low, medium, high, xhigh, max") {
		t.Fatalf("invalid effort error = %v, want the valid claude levels listed", err)
	}
	if err := applyCLIEffortOverride(shell, "high"); err == nil {
		t.Fatal("effort on a tool without effort support must be refused")
	}

	jsonData := map[string]interface{}{}
	addEffortJSON(jsonData, claude)
	if jsonData["effort"] != "high" {
		t.Fatalf("addEffortJSON = %v, want effort:high", jsonData)
	}
	jsonData = map[string]interface{}{}
	addEffortJSON(jsonData, shell)
	if _, present := jsonData["effort"]; present {
		t.Fatalf("addEffortJSON on a session without an override must omit the key, got %v", jsonData)
	}
}

func TestAddEffortFlagPersistsAndSurfacesInShow(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}

	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", "effort-add-test",
		"-c", "claude",
		"--model", "claude-opus-5",
		"--effort", "high",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf("agent-deck add --effort failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	var addResp struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if addResp.Model != "claude-opus-5" || addResp.Effort != "high" {
		t.Fatalf("add fields = model:%q effort:%q, want claude-opus-5/high", addResp.Model, addResp.Effort)
	}

	stdout, stderr, code = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	if code != 0 {
		t.Fatalf("agent-deck session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var showResp struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal([]byte(stdout), &showResp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	if showResp.Effort != "high" {
		t.Fatalf("session show effort = %q, want high", showResp.Effort)
	}

	// An invalid level is refused before anything is persisted.
	stdout, stderr, code = runAgentDeck(t, home,
		"add", "-t", "effort-bad", "-c", "claude", "--effort", "turbo", "--no-parent", projectDir,
	)
	if code == 0 {
		t.Fatal("agent-deck add --effort turbo should fail")
	}
	if !strings.Contains(stderr+stdout, "invalid reasoning effort") {
		t.Fatalf("expected an invalid reasoning effort error, got stderr: %s", stderr)
	}
}

// The Claude Options rows of the New Session dialog have CLI twins on add and
// launch: --continue, --skip-permissions, --auto-mode, --chrome, --teammate-mode.
// They persist into ClaudeOptions and are echoed in --json output.
func TestApplyCLIClaudeOptionFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	f := registerClaudeOptionFlags(fs)
	if err := fs.Parse([]string{"--skip-permissions", "--chrome", "--continue"}); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool("opts-claude", "/tmp/test", "claude")
	if err := applyCLIClaudeOptionFlags(inst, f); err != nil {
		t.Fatalf("applyCLIClaudeOptionFlags: %v", err)
	}
	opts := inst.GetClaudeOptions()
	if opts == nil || !opts.SkipPermissions || !opts.UseChrome || opts.SessionMode != "continue" || opts.AutoMode || opts.UseTeammateMode {
		t.Fatalf("ClaudeOptions = %+v, want skip+chrome+continue only", opts)
	}
	jsonData := map[string]interface{}{}
	addClaudeOptionsJSON(jsonData, inst)
	if jsonData["skip_permissions"] != true || jsonData["chrome"] != true || jsonData["session_mode"] != "continue" {
		t.Fatalf("addClaudeOptionsJSON = %v", jsonData)
	}
	if _, present := jsonData["auto_mode"]; present {
		t.Fatalf("unset options must be omitted, got %v", jsonData)
	}

	// Non-claude tools refuse the flags; no flags set is a no-op everywhere.
	codex := session.NewInstanceWithTool("opts-codex", "/tmp/test", "codex")
	if err := applyCLIClaudeOptionFlags(codex, f); err == nil {
		t.Fatal("claude option flags on a codex session must be refused")
	}
	none := registerClaudeOptionFlags(flag.NewFlagSet("n", flag.ContinueOnError))
	if err := applyCLIClaudeOptionFlags(codex, none); err != nil {
		t.Fatalf("no flags set must be a no-op, got %v", err)
	}

	// --continue and --resume-session are different session modes.
	resumed := session.NewInstanceWithTool("opts-resume", "/tmp/test", "claude")
	if err := resumed.SetClaudeOptions(&session.ClaudeOptions{SessionMode: "resume", ResumeSessionID: "abc"}); err != nil {
		t.Fatal(err)
	}
	if err := applyCLIClaudeOptionFlags(resumed, f); err == nil {
		t.Fatal("--continue with --resume-session must be refused")
	}
}

func TestAddClaudeOptionFlagsPersistAndSurfaceInShow(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "opts-add", "-c", "claude", "--skip-permissions", "--teammate-mode", "--continue", "--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var addResp struct {
		ID           string `json:"id"`
		SessionMode  string `json:"session_mode"`
		Skip         bool   `json:"skip_permissions"`
		TeammateMode bool   `json:"teammate_mode"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if addResp.SessionMode != "continue" || !addResp.Skip || !addResp.TeammateMode {
		t.Fatalf("add fields = %+v", addResp)
	}
	stdout, stderr, code = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var showResp struct {
		SessionMode  string `json:"session_mode"`
		Skip         bool   `json:"skip_permissions"`
		TeammateMode bool   `json:"teammate_mode"`
	}
	if err := json.Unmarshal([]byte(stdout), &showResp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	if showResp.SessionMode != "continue" || !showResp.Skip || !showResp.TeammateMode {
		t.Fatalf("show fields = %+v", showResp)
	}

	stdout, _, code = runAgentDeck(t, home, "add", "-t", "opts-bad", "-c", "gemini", "--chrome", "--no-parent", projectDir)
	if code == 0 || !strings.Contains(stdout, "only apply to claude") {
		t.Fatalf("--chrome on gemini should be refused, exit=%d stdout=%s", code, stdout)
	}
}
