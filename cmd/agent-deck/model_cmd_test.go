package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestApplyCLIModelOverride(t *testing.T) {
	inst := session.NewInstanceWithTool("codex-test", "/tmp/test", "codex")
	if err := applyCLIModelOverride(inst, " gpt-5.5 "); err != nil {
		t.Fatalf("applyCLIModelOverride() error = %v", err)
	}
	opts := inst.GetCodexOptions()
	if opts == nil || opts.Model != "gpt-5.5" {
		t.Fatalf("CodexOptions.Model = %v, want gpt-5.5", opts)
	}
}

func TestAddModelFlagPersistsAndSurfacesInStatus(t *testing.T) {
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
		"-t", "model-add-test",
		"-c", "codex",
		"--model", "gpt-5.5",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf("agent-deck add --model failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	var addResp struct {
		ID           string `json:"id"`
		ModelID      string `json:"model_id"`
		Model        string `json:"model"`
		ModelVersion string `json:"model_version"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if addResp.ModelID != "gpt-5.5" || addResp.Model != "GPT" || addResp.ModelVersion != "5.5" {
		t.Fatalf("add model fields = id:%q model:%q version:%q", addResp.ModelID, addResp.Model, addResp.ModelVersion)
	}

	stdout, stderr, code = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	if code != 0 {
		t.Fatalf("agent-deck session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var showResp struct {
		ModelID      string `json:"model_id"`
		Model        string `json:"model"`
		ModelVersion string `json:"model_version"`
	}
	if err := json.Unmarshal([]byte(stdout), &showResp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	if showResp.ModelID != "gpt-5.5" || showResp.Model != "GPT" || showResp.ModelVersion != "5.5" {
		t.Fatalf("show model fields = id:%q model:%q version:%q", showResp.ModelID, showResp.Model, showResp.ModelVersion)
	}

	stdout, stderr, code = runAgentDeck(t, home, "status", "--json", "--verbose")
	if code != 0 {
		t.Fatalf("agent-deck status --json --verbose failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var statusResp struct {
		Sessions []struct {
			ID           string `json:"id"`
			ModelID      string `json:"model_id"`
			Model        string `json:"model"`
			ModelVersion string `json:"model_version"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(stdout), &statusResp); err != nil {
		t.Fatalf("parse status response: %v\nstdout: %s", err, stdout)
	}
	var found bool
	for _, sess := range statusResp.Sessions {
		if sess.ID != addResp.ID {
			continue
		}
		found = true
		if sess.ModelID != "gpt-5.5" || sess.Model != "GPT" || sess.ModelVersion != "5.5" {
			t.Fatalf("status model fields = id:%q model:%q version:%q", sess.ModelID, sess.Model, sess.ModelVersion)
		}
	}
	if !found {
		t.Fatalf("status --json --verbose did not include added session; response: %s", stdout)
	}

	stdout, stderr, code = runAgentDeck(t, home, "status", "--verbose")
	if code != 0 {
		t.Fatalf("agent-deck status --verbose failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "GPT 5.5") {
		t.Fatalf("status --verbose missing model display; stdout: %s", stdout)
	}
}

// TestSessionSetModelPersists asserts that `agent-deck session set <id> model <m>`
// persists the operator's selected model into the session's tool_data so it
// survives a restart (issue #1436, follow-up to #1431). The restart-side
// consumption already prefers the persisted per-session model over
// [claude].default_model (see buildClaudeExtraFlags); this verifies the
// agent-deck-side persistence path that #1436 adds.
//
// Failure mode on main: `model` is not a valid mutable field, so the CLI
// rejects the command with an "invalid field" error (non-zero exit).
func TestSessionSetModelPersists(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "model-set-test", "-c", "claude", "--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("agent-deck add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var addResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if addResp.ID == "" {
		t.Fatalf("add returned empty id; stdout: %s", stdout)
	}

	// Switch the model — the operator's choice we want to survive restart.
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", addResp.ID, "model", "opus",
	)
	if code != 0 {
		t.Fatalf(
			"agent-deck session set <id> model failed (exit %d) — feature missing on main\n"+
				"stdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}

	// session show surfaces the persisted per-session model.
	stdout, _, code = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s", code, stdout)
	}
	var showResp struct {
		ModelID string `json:"model_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &showResp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	if showResp.ModelID != "opus" {
		t.Errorf("session set model did not persist opus; model_id = %q\nshow: %s", showResp.ModelID, stdout)
	}

	// Re-set to a different model: the new choice must replace the old one.
	_, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", addResp.ID, "model", "sonnet",
	)
	if code != 0 {
		t.Fatalf("re-set model failed (exit %d)\nstderr: %s", code, stderr)
	}
	stdout, _, _ = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	_ = json.Unmarshal([]byte(stdout), &showResp)
	if showResp.ModelID != "sonnet" {
		t.Errorf("re-set model did not persist sonnet; model_id = %q", showResp.ModelID)
	}

	// Clear via empty-string value: `model ""` must reset the override.
	_, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", addResp.ID, "model", "",
	)
	if code != 0 {
		t.Fatalf("clear model failed (exit %d)\nstderr: %s", code, stderr)
	}
	stdout, _, _ = runAgentDeck(t, home, "session", "show", addResp.ID, "--json")
	showResp.ModelID = ""
	_ = json.Unmarshal([]byte(stdout), &showResp)
	if showResp.ModelID != "" {
		t.Errorf("clear via \"\" did not reset model; model_id = %q", showResp.ModelID)
	}
}
