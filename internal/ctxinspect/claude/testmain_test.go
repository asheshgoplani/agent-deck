package claude

import (
	"os"
	"testing"
)

// TestMain isolates the package from the developer's real home directory.
//
// This is mandatory in this repository: several packages resolve agent-deck's
// live profile and config out of $HOME / $XDG_*, and an un-sandboxed test run
// has three times wiped a maintainer's live session index. Nothing here writes
// outside t.TempDir, but the isolation is unconditional so a test added later
// cannot reintroduce the hazard — and so the memory walk, which reads $HOME
// while expanding "~", can never reach the real one.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ctxinspect-claude-home-")
	if err != nil {
		panic("claude: cannot create isolated HOME for tests: " + err.Error())
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
			panic("claude: cannot isolate " + k + ": " + err.Error())
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// emptyEnv is an environment probe that reports nothing set. Tests use it so a
// stray variable in the developer's shell cannot change a result.
func emptyEnv(string) string { return "" }

// envMap returns an environment probe backed by a map.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}
