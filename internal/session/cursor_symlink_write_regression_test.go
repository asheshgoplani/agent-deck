package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// This regression test pins the symlink-preserving write for the Cursor MCP
// writer: a dotfiles-managed ~/.cursor/mcp.json that is a symlink must be updated
// through the link, leaving the symlink intact. See internal/atomicfile.

func TestWriteCursorGlobalMCP_PreservesSymlink(t *testing.T) {
	configFile := filepath.Join(session.GetCursorConfigDir(), "mcp.json")
	realPath := symlinkedFile(t, configFile, "{}")

	if err := session.WriteCursorGlobalMCP(nil); err != nil {
		t.Fatalf("WriteCursorGlobalMCP: %v", err)
	}
	session.ClearAllCursorMCPInfoCache()

	assertStillSymlink(t, configFile)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mcpServers") {
		t.Fatalf("mcpServers not written through symlink to target; got: %s", data)
	}
}
