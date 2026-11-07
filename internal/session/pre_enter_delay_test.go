package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Devin's input editor coalesces a body+Enter arriving within ~100–150ms into
// one burst and inserts the Enter as a newline instead of submitting, so the
// session layer wires a wider pre-Enter delay for Devin-compatible tools only.
// Compatibility resolves through IsDevinCompatible so custom tools wrapping
// devin (compatible_with or a devin command) get the delay too. Other tools
// must keep the transport default (zero override) so their send path stays
// byte- and timing-identical.
func TestPreEnterDelayForTool(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)
	isolateConfigHomeXDG(t)

	agentDeckDir := filepath.Join(tmpDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", agentDeckDir, err)
	}

	cfg := &UserConfig{
		Tools: map[string]ToolDef{
			"mydevin": {Command: "devin-wrapper", CompatibleWith: "devin"},
		},
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	for _, tool := range []string{"devin", "mydevin"} {
		if got := preEnterDelayForTool(tool); got != 300*time.Millisecond {
			t.Fatalf("preEnterDelayForTool(%q) = %v, want 300ms", tool, got)
		}
	}
	for _, tool := range []string{"", "shell", "claude", "codex", "gemini", "opencode"} {
		if got := preEnterDelayForTool(tool); got != 0 {
			t.Fatalf("preEnterDelayForTool(%q) = %v, want 0 (transport default)", tool, got)
		}
	}
}
