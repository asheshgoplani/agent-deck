package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionSearch_FindsMessageContent exercises issue #483 via the CLI.
// Seeds the isolated HOME with a Claude projects directory containing JSONL
// files that agent-deck's global-search index will discover, then invokes
// `session search <query>` and expects matching sessions + snippets.
func TestSessionSearch_FindsMessageContent(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	// Seed ~/.claude/projects/<proj>/<uuid>.jsonl — the index walks this tree.
	projectDir := filepath.Join(home, ".claude", "projects", "-Users-test-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := `{"sessionId":"a1b2c3d4-e5f6-7890-abcd-ef1234567890","type":"user","message":{"role":"user","content":"implement MCP server for observability metrics"},"cwd":"/Users/test/project"}
{"sessionId":"a1b2c3d4-e5f6-7890-abcd-ef1234567890","type":"assistant","message":{"role":"assistant","content":"I'll scaffold an MCP server that exposes Prometheus-style metrics."}}`
	if err := os.WriteFile(
		filepath.Join(projectDir, "a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"),
		[]byte(jsonl), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Control session that must NOT match.
	jsonl2 := `{"sessionId":"b2c3d4e5-f6a7-8901-bcde-f23456789012","type":"user","message":{"role":"user","content":"refactor the login widget styles"},"cwd":"/Users/test/project"}`
	if err := os.WriteFile(
		filepath.Join(projectDir, "b2c3d4e5-f6a7-8901-bcde-f23456789012.jsonl"),
		[]byte(jsonl2), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Invoke `session search observability --json`.
	stdout, stderr, code := runAgentDeck(t, home, "session", "search", "observability", "--json")
	if code != 0 {
		t.Fatalf("session search failed (%d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Expected JSON: {"query":"observability","results":[{"session_id":...,"snippet":...,"cwd":...}]}
	var resp struct {
		Query   string `json:"query"`
		Results []struct {
			SessionID string `json:"session_id"`
			Snippet   string `json:"snippet"`
			CWD       string `json:"cwd"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nstdout: %s", err, stdout)
	}
	if resp.Query != "observability" {
		t.Errorf("query = %q, want %q", resp.Query, "observability")
	}
	if len(resp.Results) != 1 {
		t.Fatalf("want 1 result, got %d: %+v", len(resp.Results), resp.Results)
	}
	r := resp.Results[0]
	if r.SessionID != "a1b2c3d4-e5f6-7890-abcd-ef1234567890" {
		t.Errorf("session_id = %q, want the matching UUID", r.SessionID)
	}
	if !strings.Contains(strings.ToLower(r.Snippet), "observability") {
		t.Errorf("snippet %q should contain the query term", r.Snippet)
	}
	if r.CWD != "/Users/test/project" {
		t.Errorf("cwd = %q, want %q", r.CWD, "/Users/test/project")
	}
}

// TestSessionSearch_EmptyQuery requires a query argument.
func TestSessionSearch_EmptyQuery(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	_, stderr, code := runAgentDeck(t, home, "session", "search")
	if code == 0 {
		t.Fatal("expected non-zero exit for empty query")
	}
	if !strings.Contains(strings.ToLower(stderr), "query") &&
		!strings.Contains(strings.ToLower(stderr), "usage") {
		t.Errorf("stderr should mention query / usage, got: %s", stderr)
	}
}

// TestSessionSearch_NoMatches returns an empty results array, exit 0.
func TestSessionSearch_NoMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", "-Users-test-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Empty projects directory — the index will come up with zero entries.

	stdout, stderr, code := runAgentDeck(t, home, "session", "search", "xyzzy-no-match", "--json")
	if code != 0 {
		t.Fatalf("session search no-matches failed (%d)\nstderr: %s", code, stderr)
	}
	var resp struct {
		Query   string        `json:"query"`
		Results []interface{} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(resp.Results))
	}
}
