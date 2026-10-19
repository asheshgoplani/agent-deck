package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
)

// TestHomeDerivedToolPathsRefuseTheRealHome covers the call sites that build
// ~/.claude, ~/.claude.json, ~/.codex, ~/.pi or $GEMINI_CLI_HOME/.gemini
// themselves instead of going through a guarded resolver. With HOME (or the
// override) pointed at the real home, the way an un-isolated test sees it,
// each must panic before it reads anything there, and the panic must name the
// variable the path came through.
func TestHomeDerivedToolPathsRefuseTheRealHome(t *testing.T) {
	homes := sandboxguard.RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	real := homes[0]

	cases := []struct {
		name    string
		env     map[string]string
		cfg     *UserConfig
		wantEnv string
		fn      func() string
		// wantKind, when set, must appear in the panic: the path kind of the
		// site under test, so a guarded resolver further down the same
		// function cannot stand in for it.
		wantKind string
	}{
		{"GetUserMCPRootPath", nil, nil, "HOME", GetUserMCPRootPath, ""},
		{"ClaudeSessionName", nil, nil, "HOME", func() string { return ClaudeSessionName("sid") }, ""},
		{"ClaudeSessionNameAndCwd", nil, nil, "HOME", func() string { n, c := ClaudeSessionNameAndCwd("sid"); return n + c }, ""},
		{"ClaudeSessionRecordFor", nil, nil, "HOME", func() string { r, ok := ClaudeSessionRecordFor("sid"); return fmt.Sprint(r, ok) }, ""},
		{"RecallClaudeRoots", nil, &UserConfig{}, "HOME", func() string { return fmt.Sprint(RecallClaudeRoots()) }, ""},
		{"RecallRoots codex", nil, &UserConfig{Recall: RecallSettings{Harnesses: []string{"codex", "pi"}}}, "HOME",
			func() string { return fmt.Sprint(RecallRoots()) }, "Codex home (~/.codex)"},
		{"RecallRoots pi", nil, &UserConfig{Recall: RecallSettings{Harnesses: []string{"pi"}}}, "HOME",
			func() string { return fmt.Sprint(RecallRoots()) }, "Pi dir (~/.pi)"},
		{"openCodeStorageDir", map[string]string{"XDG_DATA_HOME": ""}, nil, "HOME",
			func() string { return openCodeStorageDir(real) }, "OpenCode storage dir"},
		{"geminiUserDir via GEMINI_CLI_HOME", map[string]string{"HOME": t.TempDir(), "GEMINI_CLI_HOME": real}, nil, "GEMINI_CLI_HOME", geminiUserDir, ""},
		{"transcriptRoots", nil, nil, "HOME", func() string { return fmt.Sprint(transcriptRoots()) }, ""},
		{"conversationConfigDirCandidates", nil, nil, "HOME", func() string { return fmt.Sprint(conversationConfigDirCandidates(nil)) }, ""},
		{"claudeConfigDirRootsForInstance", map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(t.TempDir(), "claude")}, &UserConfig{}, "HOME",
			func() string { return fmt.Sprint(claudeConfigDirRootsForInstance(&Instance{Tool: "claude"})) }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", real)
			for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "GEMINI_CLI_HOME"} {
				t.Setenv(k, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if c.cfg != nil {
				injectUserConfigForTest(t, c.cfg)
			}
			got, msg := callRecover(c.fn)
			if msg == "" {
				t.Fatalf("returned %q with HOME at the real home %q, want a sandboxguard panic", got, real)
			}
			if !strings.Contains(msg, "sandboxguard:") || !strings.Contains(msg, "through "+c.wantEnv+"=") {
				t.Fatalf("panic does not come from the guard naming %s: %s", c.wantEnv, msg)
			}
			if c.wantKind != "" && !strings.Contains(msg, c.wantKind) {
				t.Fatalf("panic is not for %q, so this site is not guarded: %s", c.wantKind, msg)
			}
		})
	}
}

// injectUserConfigForTest makes LoadUserConfig return cfg while HOME points
// at the real home, where agentpaths refuses the config path and the loader
// would otherwise fall back to defaults.
func injectUserConfigForTest(t *testing.T, cfg *UserConfig) {
	t.Helper()
	userConfigCacheMu.Lock()
	oldCfg, oldMtime, oldErr := userConfigCache, userConfigCacheMtime, userConfigCacheErr
	userConfigCache, userConfigCacheMtime, userConfigCacheErr = cfg, time.Time{}, nil
	userConfigCacheMu.Unlock()
	t.Cleanup(func() {
		userConfigCacheMu.Lock()
		userConfigCache, userConfigCacheMtime, userConfigCacheErr = oldCfg, oldMtime, oldErr
		userConfigCacheMu.Unlock()
	})
}
