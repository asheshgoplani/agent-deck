package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdd_MaterializesDeclarativeLoadout locks the create-time wiring: a
// session added into a group whose [groups.X.claude] stanza declares
// skills/mcps gets the loadout materialized at `add` (before any start),
// via the same machinery as manual `skill attach` / `mcp attach`.
func TestAdd_MaterializesDeclarativeLoadout(t *testing.T) {
	home := t.TempDir()

	// Skill store with one directory skill.
	store := filepath.Join(home, "store")
	alpha := filepath.Join(store, "alpha")
	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alpha, "SKILL.md"), []byte("---\nname: alpha\n---\n# alpha\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	writeTestConfig(t, home, `
[mcps.memory]
command = "echo"

[groups."work".claude]
plugins = ["store/alpha"]
mcps = ["memory", "ghostmcp"]
`)

	if _, stderr, code := runAgentDeck(t, home, "skill", "source", "add", "store", store); code != 0 {
		t.Fatalf("skill source add failed: %s", stderr)
	}

	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "loadout-test", "-c", "claude", "-g", "work", project)
	if code != 0 {
		t.Fatalf("add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Skill symlink materialized at create time.
	target := filepath.Join(project, ".claude", "skills", "alpha")
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("expected skill symlink at %s: %v\nstderr: %s", target, err, stderr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink, mode=%v", info.Mode())
	}

	// Manifest records it.
	if data, err := os.ReadFile(filepath.Join(project, ".agent-deck", "skills.toml")); err != nil || !strings.Contains(string(data), "alpha") {
		t.Errorf("manifest missing alpha: %v\n%s", err, data)
	}

	// MCP landed in local .mcp.json.
	if data, err := os.ReadFile(filepath.Join(project, ".mcp.json")); err != nil || !strings.Contains(string(data), "memory") {
		t.Errorf(".mcp.json missing memory: %v\n%s", err, data)
	}

	// The unknown catalog name warned on stderr but did not fail the add.
	if !strings.Contains(stderr, "ghostmcp") {
		t.Errorf("expected ghostmcp warning on stderr, got:\n%s", stderr)
	}
}
