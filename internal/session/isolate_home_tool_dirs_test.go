package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestIsolateHome_ToolConfigResolversStayInSandbox runs the real resolvers
// after testutil.IsolateHome while the shell exports the tool config-dir
// variables, as a worker session started by agent-deck does for
// CLAUDE_CONFIG_DIR. Every resolver must land under the isolated HOME, and a
// real Claude hook install must not touch the "live" dir. Before IsolateHome
// cleared these variables, all of them resolved to the live dirs.
func TestIsolateHome_ToolConfigResolversStayInSandbox(t *testing.T) {
	live := t.TempDir() // stands in for the developer's real tool config
	for _, k := range testutil.ToolConfigDirEnvs {
		t.Setenv(k, filepath.Join(live, strings.ToLower(k)))
	}

	cleanup := testutil.IsolateHome()
	defer cleanup()
	ClearUserConfigCache()
	defer ClearUserConfigCache()
	home := os.Getenv("HOME")

	resolved := map[string]string{
		"GetClaudeConfigDir":       GetClaudeConfigDir(),
		"getCodexHomeDir":          getCodexHomeDir(),
		"getCopilotHomeDir":        getCopilotHomeDir(),
		"getDeepSeekHomeDir":       getDeepSeekHomeDir(),
		"geminiUserDir":            geminiUserDir(),
		"geminiTrustedFoldersPath": geminiTrustedFoldersPath(),
		"PiExtensionsDir":          PiExtensionsDir(),
	}
	for name, got := range resolved {
		if !strings.HasPrefix(got, home+string(filepath.Separator)) {
			t.Errorf("%s() = %q after IsolateHome, want a path under the isolated HOME %q", name, got, home)
		}
	}

	if _, err := InjectClaudeHooks(GetClaudeConfigDir()); err != nil {
		t.Fatalf("InjectClaudeHooks: %v", err)
	}
	var leaked []string
	_ = filepath.Walk(live, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			leaked = append(leaked, p)
		}
		return nil
	})
	if len(leaked) > 0 {
		t.Fatalf("a hook install under test wrote into the live config dir: %v", leaked)
	}
}
