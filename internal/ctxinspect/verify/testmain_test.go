package verify

import (
	"os"
	"testing"
)

// TestMain isolates the package from the developer's real home directory.
//
// Nothing in this package reads $HOME, but the isolation is unconditional: this
// repository has three times had a test run resolve agent-deck's live profile
// out of the real home and destroy it, and "this package does not do that yet"
// is not a property a later test preserves.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ctxverify-home-")
	if err != nil {
		panic("ctxinspect/verify: cannot create isolated HOME for tests: " + err.Error())
	}
	for k, v := range map[string]string{
		"HOME":              dir,
		"XDG_CONFIG_HOME":   dir + "/.config",
		"XDG_DATA_HOME":     dir + "/.local/share",
		"XDG_CACHE_HOME":    dir + "/.cache",
		"XDG_STATE_HOME":    dir + "/.local/state",
		"CLAUDE_CONFIG_DIR": dir + "/.claude",
		"AGENTDECK_PROFILE": "_test",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic("ctxinspect/verify: cannot isolate " + k + ": " + err.Error())
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
