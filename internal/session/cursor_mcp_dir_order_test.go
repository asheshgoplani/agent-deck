package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WriteCursorProjectMCP used to MkdirAll <project>/.cursor before the
// containment check ran. Nothing may be created before that check: a dangling
// .cursor link out of the project must get the containment refusal (not a
// mkdir error) and leave nothing behind outside the project.
func TestWriteCursorProjectMCP_DanglingDirLinkRefusedBeforeMkdir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmp, "elsewhere", "made")
	if err := os.Symlink(outside, filepath.Join(proj, ".cursor")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetUserConfigCache(t, &UserConfig{MCPs: map[string]MCPDef{"cat": {Command: "echo"}}}))

	err := WriteCursorProjectMCP(proj, []string{"cat"})
	if err == nil || !strings.Contains(err.Error(), "refusing to write") {
		t.Fatalf("expected the containment refusal, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Dir(outside)); !os.IsNotExist(statErr) {
		t.Fatalf("created a directory outside the project (stat err %v)", statErr)
	}
}

// Without the eager MkdirAll the .cursor directory must still be created for
// a project that has none yet.
func TestWriteCursorProjectMCP_CreatesMissingCursorDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetUserConfigCache(t, &UserConfig{MCPs: map[string]MCPDef{"cat": {Command: "echo"}}}))

	if err := WriteCursorProjectMCP(proj, []string{"cat"}); err != nil {
		t.Fatalf("WriteCursorProjectMCP: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(proj, ".cursor", "mcp.json"))
	if err != nil || !strings.Contains(string(data), `"cat"`) {
		t.Fatalf("mcp.json not written (err %v): %s", err, data)
	}
}
