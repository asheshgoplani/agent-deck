package session_test

import (
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// This regression test pins the symlink-preserving durable write for agent-deck's
// own config.toml: a dotfiles-managed ~/.config/agent-deck/config.toml that is a
// symlink must be updated through the link, leaving the symlink intact. See
// internal/atomicfile.WriteFileDurable.

func TestSaveUserConfig_PreservesSymlink(t *testing.T) {
	configPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	realPath := symlinkedFile(t, configPath, "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	assertStillSymlink(t, configPath)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Agent Deck Configuration") {
		t.Fatalf("config not written through symlink to target; got: %s", data)
	}
}
