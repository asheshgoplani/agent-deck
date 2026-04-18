package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGroupChange_RootToSubgroup exercises issue #447 via the CLI boundary.
// Creates two sessions (one in groupA, one in groupB), then changes groupA
// to become a subgroup of groupB. The session originally in groupA must
// land under the new path groupB/groupA.
func TestGroupChange_RootToSubgroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a session in group "alpha".
	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "sess-alpha", "-c", "claude", "-g", "alpha",
		"--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("add alpha failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	var alphaResp struct{ ID string }
	if err := json.Unmarshal([]byte(stdout), &alphaResp); err != nil {
		t.Fatalf("unmarshal alpha: %v\n%s", err, stdout)
	}

	// Create a session in group "beta".
	stdout, stderr, code = runAgentDeck(t, home,
		"add", "-t", "sess-beta", "-c", "claude", "-g", "beta",
		"--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("add beta failed (%d)\n%s\n%s", code, stdout, stderr)
	}

	// Change alpha to be a subgroup of beta.
	stdout, stderr, code = runAgentDeck(t, home, "group", "change", "alpha", "beta", "--json")
	if code != 0 {
		t.Fatalf("group change failed (%d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Verify alpha session is now at path beta/alpha.
	stdout, stderr, code = runAgentDeck(t, home, "session", "show", alphaResp.ID, "--json")
	if code != 0 {
		t.Fatalf("session show failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	var showResp struct {
		GroupPath string `json:"group"`
	}
	if err := json.Unmarshal([]byte(stdout), &showResp); err != nil {
		t.Fatalf("unmarshal show: %v\n%s", err, stdout)
	}
	if showResp.GroupPath != "beta/alpha" {
		t.Fatalf("group_path = %q, want %q", showResp.GroupPath, "beta/alpha")
	}

	// Verify "group list" shows beta with alpha as a subgroup.
	stdout, _, code = runAgentDeck(t, home, "group", "list", "--json")
	if code != 0 {
		t.Fatalf("group list failed (%d)\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "beta/alpha") {
		t.Errorf("expected 'beta/alpha' in group list output, got:\n%s", stdout)
	}
	if strings.Contains(stdout, `"alpha"`) && !strings.Contains(stdout, "beta/alpha") {
		t.Errorf("root-level 'alpha' should no longer exist")
	}
}

// TestGroupChange_MoveToRoot verifies the "no dest" form moves a subgroup
// back to root level.
func TestGroupChange_MoveToRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a session inside parent/child.
	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "nested", "-c", "claude", "-g", "parent/child",
		"--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("add nested failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	var resp struct{ ID string }
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Move parent/child to root (no dest arg).
	stdout, stderr, code = runAgentDeck(t, home, "group", "change", "parent/child", "--json")
	if code != 0 {
		t.Fatalf("group change to root failed (%d)\n%s\n%s", code, stdout, stderr)
	}

	// Verify the session's group_path is now just "child".
	stdout, _, code = runAgentDeck(t, home, "session", "show", resp.ID, "--json")
	if code != 0 {
		t.Fatalf("session show failed (%d)\n%s", code, stdout)
	}
	var show struct {
		GroupPath string `json:"group"`
	}
	if err := json.Unmarshal([]byte(stdout), &show); err != nil {
		t.Fatalf("unmarshal show: %v", err)
	}
	if show.GroupPath != "child" {
		t.Fatalf("group_path = %q, want %q", show.GroupPath, "child")
	}
}

// TestGroupChange_RejectsCircular ensures the CLI refuses circular moves.
func TestGroupChange_RejectsCircular(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, code := runAgentDeck(t, home,
		"add", "-t", "nested", "-c", "claude", "-g", "a/b",
		"--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("setup failed (%d)", code)
	}

	// Attempt to move 'a' under its own descendant 'a/b' — must fail.
	_, stderr, code := runAgentDeck(t, home, "group", "change", "a", "a/b")
	if code == 0 {
		t.Fatal("expected non-zero exit for circular move")
	}
	if !strings.Contains(strings.ToLower(stderr), "circular") &&
		!strings.Contains(strings.ToLower(stderr), "descendant") &&
		!strings.Contains(strings.ToLower(stderr), "itself") {
		t.Errorf("stderr should mention the circular/descendant reason, got: %s", stderr)
	}
}
