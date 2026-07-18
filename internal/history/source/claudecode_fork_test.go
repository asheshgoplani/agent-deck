package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewClaudeCodeTool_UsesClaudeConfigDir(t *testing.T) {
	// Point agent-deck's resolver at a temp dir via CLAUDE_CONFIG_DIR.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	tool := NewClaudeCodeTool()
	if tool.Name() != "claude-code" {
		t.Fatalf("Name() = %q, want claude-code", tool.Name())
	}
	// Discover on an empty (but existing) projects dir returns no projects, no error.
	projects, err := tool.Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("Discover() = %d projects, want 0", len(projects))
	}
}
