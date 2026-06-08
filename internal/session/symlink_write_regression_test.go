package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// These regression tests pin the fix for the symlink-clobber bug: writing a
// Claude config file that is a dotfiles-managed symlink must update the real
// target and leave the symlink intact (previously os.Rename replaced the link
// with a regular file). See internal/atomicfile.

func symlinkedFile(t *testing.T, linkPath, contents string) (realPath string) {
	t.Helper()
	realDir := t.TempDir()
	realPath = filepath.Join(realDir, "real"+filepath.Ext(linkPath))
	if err := os.WriteFile(realPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// linkPath may be a process-global path (e.g. GetClaudeConfigDir()/.claude.json)
	// shared across tests in this package's isolated HOME. Clear any leftover and
	// remove ours on cleanup so tests don't collide.
	_ = os.Remove(linkPath)
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(linkPath) })
	return realPath
}

func assertStillSymlink(t *testing.T, linkPath string) {
	t.Helper()
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s was clobbered into a regular file", linkPath)
	}
}

func TestInjectClaudeHooks_PreservesSymlink(t *testing.T) {
	configDir := t.TempDir()
	link := filepath.Join(configDir, "settings.json")
	realPath := symlinkedFile(t, link, "{}")

	if _, err := session.InjectClaudeHooks(configDir); err != nil {
		t.Fatalf("InjectClaudeHooks: %v", err)
	}

	assertStillSymlink(t, link)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hook-handler") {
		t.Fatalf("hooks not written through symlink to target; got: %s", data)
	}
}

func TestRemoveClaudeHooks_PreservesSymlink(t *testing.T) {
	configDir := t.TempDir()
	link := filepath.Join(configDir, "settings.json")
	symlinkedFile(t, link, "{}")

	if _, err := session.InjectClaudeHooks(configDir); err != nil {
		t.Fatalf("InjectClaudeHooks: %v", err)
	}
	if _, err := session.RemoveClaudeHooks(configDir); err != nil {
		t.Fatalf("RemoveClaudeHooks: %v", err)
	}

	assertStillSymlink(t, link)
}

func TestPreAcceptClaudeTrust_PreservesSymlink(t *testing.T) {
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, ".claude.json")
	realPath := symlinkedFile(t, link, "{}")

	parentDir := "/tmp/agent-deck-trust-test-parent"
	if err := session.PreAcceptClaudeTrust(link, parentDir); err != nil {
		t.Fatalf("PreAcceptClaudeTrust: %v", err)
	}

	assertStillSymlink(t, link)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hasTrustDialogAccepted") {
		t.Fatalf("trust entry not written through symlink to target; got: %s", data)
	}
	if !strings.Contains(string(data), parentDir) {
		t.Fatalf("parentDir key missing from target; got: %s", data)
	}
}

func TestWriteGlobalMCP_PreservesSymlink(t *testing.T) {
	configFile := filepath.Join(session.GetClaudeConfigDir(), ".claude.json")
	realPath := symlinkedFile(t, configFile, "{}")

	if err := session.WriteGlobalMCP(nil); err != nil {
		t.Fatalf("WriteGlobalMCP: %v", err)
	}

	assertStillSymlink(t, configFile)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mcpServers") {
		t.Fatalf("mcpServers not written through symlink to target; got: %s", data)
	}
}

func TestWriteUserMCP_PreservesSymlink(t *testing.T) {
	configFile := session.GetUserMCPRootPath()
	if configFile == "" {
		t.Skip("no user MCP root path resolved")
	}
	realPath := symlinkedFile(t, configFile, "{}")

	if err := session.WriteUserMCP(nil); err != nil {
		t.Fatalf("WriteUserMCP: %v", err)
	}

	assertStillSymlink(t, configFile)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mcpServers") {
		t.Fatalf("mcpServers not written through symlink to target; got: %s", data)
	}
}

func TestClearProjectMCPs_PreservesSymlink(t *testing.T) {
	configFile := filepath.Join(session.GetClaudeConfigDir(), ".claude.json")
	realPath := symlinkedFile(t, configFile, `{"projects":{"/tmp/p":{"mcpServers":{"x":{}}}}}`)

	if err := session.ClearProjectMCPs("/tmp/p"); err != nil {
		t.Fatalf("ClearProjectMCPs: %v", err)
	}

	assertStillSymlink(t, configFile)
	if _, err := os.Stat(realPath); err != nil {
		t.Fatalf("target missing after write: %v", err)
	}
}
