package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddEnvFlag asserts that `agent-deck add --env KEY=VALUE` is parsed
// (repeatable) and the env vars persist on the new session (all tools).
func TestAddEnvFlag(t *testing.T) {
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
		"-t", "env-add-test",
		"-c", "gemini",
		"--env", "FOO=bar",
		"--env", "HTTPS_PROXY=http://127.0.0.1:8080",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf("agent-deck add --env failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	// Plaintext warning must be emitted on stderr.
	if !strings.Contains(stderr, "plaintext") {
		t.Errorf("expected plaintext warning on stderr; got: %s", stderr)
	}

	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, "FOO=bar") {
		t.Errorf("persisted sessions missing FOO=bar; got:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, "HTTPS_PROXY=http://127.0.0.1:8080") {
		t.Errorf("persisted sessions missing HTTPS_PROXY; got:\n%s", listJSON)
	}
}

// TestAddEnvFlag_InvalidKeyRejected asserts an invalid env key is rejected by
// the flag validator before the session is created.
func TestAddEnvFlag_InvalidKeyRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", "env-bad-test",
		"-c", "claude",
		"--env", "1BAD=x",
		"--no-parent",
		projectDir,
	)
	if code == 0 {
		t.Fatalf("expected non-zero exit for invalid env key; stderr: %s", stderr)
	}
}

// TestSessionSetEnv asserts `session set <id> env KEY=VALUE` upserts and
// `env KEY=` unsets, delegating to session.SetField.
func TestSessionSetEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "env-set-test", "-c", "claude", "--no-parent", "--json", projectDir,
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

	// Upsert.
	_, stderr, code = runAgentDeck(t, home, "session", "set", "--json", addResp.ID, "env", "FOO=bar")
	if code != 0 {
		t.Fatalf("session set env FOO=bar failed (exit %d)\nstderr: %s", code, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, "FOO=bar") {
		t.Errorf("session set env did not persist FOO=bar; list:\n%s", listJSON)
	}

	// Replace value.
	_, stderr, code = runAgentDeck(t, home, "session", "set", "--json", addResp.ID, "env", "FOO=baz")
	if code != 0 {
		t.Fatalf("session set env FOO=baz failed (exit %d)\nstderr: %s", code, stderr)
	}
	listJSON = readSessionsJSON(t, home)
	if strings.Contains(listJSON, "FOO=bar") || !strings.Contains(listJSON, "FOO=baz") {
		t.Errorf("replace did not take effect; list:\n%s", listJSON)
	}

	// Unset via empty value.
	_, stderr, code = runAgentDeck(t, home, "session", "set", "--json", addResp.ID, "env", "FOO=")
	if code != 0 {
		t.Fatalf("session set env FOO= (unset) failed (exit %d)\nstderr: %s", code, stderr)
	}
	listJSON = readSessionsJSON(t, home)
	if strings.Contains(listJSON, "FOO=baz") {
		t.Errorf("unset did not remove FOO; list:\n%s", listJSON)
	}

	// Invalid key rejected (both set and unset).
	_, _, code = runAgentDeck(t, home, "session", "set", addResp.ID, "env", "1BAD=x")
	if code == 0 {
		t.Errorf("expected non-zero exit for invalid env key on set")
	}
	_, _, code = runAgentDeck(t, home, "session", "set", addResp.ID, "env", "1BAD=")
	if code == 0 {
		t.Errorf("expected non-zero exit for invalid env key on unset")
	}
}
