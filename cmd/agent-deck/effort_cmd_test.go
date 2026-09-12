package main

// CLI parity for the new-session dialog's "Reasoning effort" row: `add` and
// `launch` accept --effort, validate it against the tool, and surface it in
// --json output alongside the model fields.

import (
	"encoding/json"
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
