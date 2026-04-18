package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddExtraArgFlag asserts that `agent-deck add --extra-arg <token>`
// is parsed (repeatable) and the args persist on the new session.
//
// Failure mode on main:
//
//	flag provided but not defined: -extra-arg
//	(exit 2 from flag.NewFlagSet ExitOnError)
func TestAddExtraArgFlag(t *testing.T) {
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
		"-t", "ea-add-test",
		"-c", "claude",
		"--extra-arg", "--agent",
		"--extra-arg", "my-agent",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf(
			"agent-deck add --extra-arg failed (exit %d) — feature missing\n"+
				"stdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}

	listJSON := readSessionsJSON(t, home)

	if !strings.Contains(listJSON, "--agent") {
		t.Errorf("persisted sessions missing --agent token; got:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, "my-agent") {
		t.Errorf("persisted sessions missing my-agent value; got:\n%s", listJSON)
	}
}

// TestSessionSetExtraArgs asserts that
// `agent-deck session set <id> extra-args <space-separated>` updates the field.
func TestSessionSetExtraArgs(t *testing.T) {
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
		"-t", "ea-set-test",
		"-c", "claude",
		"--no-parent",
		"--json",
		projectDir,
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

	// Use `--` terminator so Go's flag package leaves --model alone.
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", addResp.ID, "extra-args",
		"--", "--model", "opus",
	)
	if code != 0 {
		t.Fatalf(
			"agent-deck session set <id> extra-args failed (exit %d) — feature missing\n"+
				"stdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}

	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, "--model") {
		t.Errorf("session set extra-args did not persist --model; list output:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, "opus") {
		t.Errorf("session set extra-args did not persist opus; list output:\n%s", listJSON)
	}

	// Clear via empty-string value: `extra-args ""` must reset the list.
	_, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", addResp.ID, "extra-args", "",
	)
	if code != 0 {
		t.Fatalf("clear extra-args failed (exit %d)\nstderr: %s", code, stderr)
	}
	listJSON = readSessionsJSON(t, home)
	if strings.Contains(listJSON, "--model") || strings.Contains(listJSON, "opus") {
		t.Errorf("clear via \"\" did not reset extra-args; list output:\n%s", listJSON)
	}

	// Empty tokens mixed with real ones must be dropped (avoid emitting
	// literal '' to claude). `extra-args -- "" --model "" opus` → [--model, opus].
	_, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", addResp.ID, "extra-args",
		"--", "", "--model", "", "opus",
	)
	if code != 0 {
		t.Fatalf("extra-args with mixed empty tokens failed (exit %d)\nstderr: %s", code, stderr)
	}
	listJSON = readSessionsJSON(t, home)
	if !strings.Contains(listJSON, "--model") || !strings.Contains(listJSON, "opus") {
		t.Errorf("real tokens missing after mixed-empty set; list output:\n%s", listJSON)
	}
	// Ensure JSON array does not contain an empty-string element.
	var listResp []struct {
		ID        string   `json:"id"`
		ExtraArgs []string `json:"extra_args"`
	}
	if err := json.Unmarshal([]byte(listJSON), &listResp); err != nil {
		t.Fatalf("parse list JSON: %v", err)
	}
	var match *struct {
		ID        string   `json:"id"`
		ExtraArgs []string `json:"extra_args"`
	}
	for i := range listResp {
		if listResp[i].ID == addResp.ID {
			match = &listResp[i]
			break
		}
	}
	if match == nil {
		t.Fatalf("session %s not found in list", addResp.ID)
	}
	for _, tok := range match.ExtraArgs {
		if tok == "" {
			t.Errorf("empty token leaked into persisted ExtraArgs: %v", match.ExtraArgs)
		}
	}
	if len(match.ExtraArgs) != 2 {
		t.Errorf("expected exactly 2 tokens after empty-strip, got %v", match.ExtraArgs)
	}
}

// TestExtraArgsOnlyForClaude asserts the tool-restriction contract, same
// shape as TestChannelsOnlyForClaude in channels_cmd_test.go:246.
//
// On main the field is universally rejected. Positive+negative arms are
// required to prevent a false-PASS where the generic rejection trivially
// satisfies a unilateral negative test.
func TestExtraArgsOnlyForClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	claudeProj := filepath.Join(home, "claude-proj")
	shellProj := filepath.Join(home, "shell-proj")
	for _, p := range []string{claudeProj, shellProj} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Positive control: claude session accepts extra-args.
	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "ea-claude-ok", "-c", "claude", "--no-parent", "--json", claudeProj,
	)
	if code != 0 {
		t.Fatalf("add claude failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var claudeResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claudeResp); err != nil {
		t.Fatalf("parse claude add response: %v\nstdout: %s", err, stdout)
	}

	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", claudeResp.ID, "extra-args",
		"--", "--model", "opus",
	)
	if code != 0 {
		t.Fatalf(
			"positive control failed: setting extra-args on CLAUDE session "+
				"should succeed (exit %d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}

	// Negative control: shell session rejects extra-args with a tool-specific message.
	stdout, stderr, code = runAgentDeck(t, home,
		"add", "-t", "ea-shell-reject", "-c", "bash", "--no-parent", "--json", shellProj,
	)
	if code != 0 {
		t.Fatalf("add shell failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var shellResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &shellResp); err != nil {
		t.Fatalf("parse shell add response: %v\nstdout: %s", err, stdout)
	}

	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", "--json", shellResp.ID, "extra-args",
		"--", "--model", "opus",
	)
	if code == 0 {
		t.Fatalf(
			"negative control failed: extra-args on non-claude session must be rejected\n"+
				"stdout: %s\nstderr: %s",
			stdout, stderr,
		)
	}
	combined := strings.ToLower(stdout + stderr)
	if strings.Contains(combined, "invalid field") {
		t.Errorf(
			"shell-session error should be a tool-restriction message, "+
				"NOT a generic 'invalid field'; got:\nstdout: %s\nstderr: %s",
			stdout, stderr,
		)
	}
	mustMentionTool := strings.Contains(combined, "claude") &&
		(strings.Contains(combined, "only") ||
			strings.Contains(combined, "supported") ||
			strings.Contains(combined, "requires"))
	if !mustMentionTool {
		t.Errorf(
			"shell-session error must mention claude AND a restriction word "+
				"(only/supported/requires); got:\nstdout: %s\nstderr: %s",
			stdout, stderr,
		)
	}
}
