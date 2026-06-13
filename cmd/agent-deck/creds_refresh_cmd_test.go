// Tests for `agent-deck creds-refresh` config-dir resolution (issue #1414).
//
// The keep-warm daemon's default coverage was hardcoded to ~/.claude and
// ~/.claude-work. Hosts with additional account profiles declared in
// config.toml ([profiles.<name>.claude].config_dir, e.g. ~/.claude-seminno)
// silently left those canonicals UN-warmed, so their concurrent sessions kept
// racing Anthropic's single-use rotating refresh token — the exact mid-turn
// 401 signature of #1414. Defaults must derive from the user config.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// writeCreds creates dir with a placeholder .credentials.json so the dir
// passes resolveCredConfigDirs's "has credentials" filter. The content is
// never parsed by resolution — a stub is sufficient and no real token is
// ever written.
func writeCreds(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsDir(dirs []string, want string) bool {
	for _, d := range dirs {
		if d == want {
			return true
		}
	}
	return false
}

// The #1414 regression: a profile declared in config.toml with its own
// config_dir must be part of the default keep-warm set.
func TestResolveCredConfigDirs_DefaultsIncludeConfiguredProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacy := filepath.Join(home, ".claude")
	seminno := filepath.Join(home, ".claude-seminno")
	writeCreds(t, legacy)
	writeCreds(t, seminno)

	cfg := &session.UserConfig{
		Profiles: map[string]session.ProfileSettings{
			"seminno": {Claude: session.ProfileClaudeSettings{ConfigDir: "~/.claude-seminno"}},
		},
	}

	dirs := resolveCredConfigDirs(nil, cfg)
	if !containsDir(dirs, legacy) {
		t.Errorf("legacy default %s missing from %v", legacy, dirs)
	}
	if !containsDir(dirs, seminno) {
		t.Errorf("profile config dir %s missing from %v — #1414 keep-warm coverage gap", seminno, dirs)
	}
}

// Group- and conductor-scoped config_dir overrides are also spawnable account
// canonicals and must be kept warm.
func TestResolveCredConfigDirs_DefaultsIncludeGroupAndConductorDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	groupDir := filepath.Join(home, ".claude-group")
	condDir := filepath.Join(home, ".claude-cond")
	globalDir := filepath.Join(home, ".claude-global")
	writeCreds(t, groupDir)
	writeCreds(t, condDir)
	writeCreds(t, globalDir)

	cfg := &session.UserConfig{
		Claude: session.ClaudeSettings{ConfigDir: "~/.claude-global"},
		Groups: map[string]session.GroupSettings{
			"g": {Claude: session.GroupClaudeSettings{ConfigDir: "~/.claude-group"}},
		},
		Conductors: map[string]session.ConductorOverrides{
			"c": {Claude: session.ConductorClaudeSettings{ConfigDir: "~/.claude-cond"}},
		},
	}

	dirs := resolveCredConfigDirs(nil, cfg)
	for _, want := range []string{groupDir, condDir, globalDir} {
		if !containsDir(dirs, want) {
			t.Errorf("config-declared dir %s missing from %v", want, dirs)
		}
	}
}

// Two declarations aliasing ONE canonical (e.g. a symlinked profile dir) must
// resolve to a single keep-warm entry — refreshing one canonical twice per
// tick is wasted lock traffic against Claude's own refresh lock.
func TestResolveCredConfigDirs_DedupesSymlinkAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	canonical := filepath.Join(home, ".claude-real")
	writeCreds(t, canonical)
	alias := filepath.Join(home, ".claude-alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}

	cfg := &session.UserConfig{
		Profiles: map[string]session.ProfileSettings{
			"real":  {Claude: session.ProfileClaudeSettings{ConfigDir: "~/.claude-real"}},
			"alias": {Claude: session.ProfileClaudeSettings{ConfigDir: "~/.claude-alias"}},
		},
	}

	dirs := resolveCredConfigDirs(nil, cfg)
	matches := 0
	for _, d := range dirs {
		resolved, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		canonResolved, err := filepath.EvalSymlinks(canonical)
		if err != nil {
			t.Fatal(err)
		}
		if resolved == canonResolved {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("canonical %s appears %d times in %v; want exactly 1", canonical, matches, dirs)
	}
}

// Explicit --config-dir flags pin the set exactly: config-derived candidates
// must NOT be appended (operator intent wins, matching prior behavior).
func TestResolveCredConfigDirs_ExplicitFlagsBypassConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pinned := filepath.Join(home, ".claude-pinned")
	other := filepath.Join(home, ".claude-other")
	writeCreds(t, pinned)
	writeCreds(t, other)

	cfg := &session.UserConfig{
		Profiles: map[string]session.ProfileSettings{
			"other": {Claude: session.ProfileClaudeSettings{ConfigDir: "~/.claude-other"}},
		},
	}

	dirs := resolveCredConfigDirs(stringSliceFlag{pinned}, cfg)
	if len(dirs) != 1 || dirs[0] != pinned {
		t.Errorf("explicit flags must pin the set; got %v, want [%s]", dirs, pinned)
	}
}

// Dirs without a .credentials.json are filtered out (pre-existing semantics:
// a declared-but-never-logged-in profile must not error every tick).
func TestResolveCredConfigDirs_SkipsDirsWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	empty := filepath.Join(home, ".claude-empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &session.UserConfig{
		Profiles: map[string]session.ProfileSettings{
			"empty": {Claude: session.ProfileClaudeSettings{ConfigDir: "~/.claude-empty"}},
		},
	}

	dirs := resolveCredConfigDirs(nil, cfg)
	if containsDir(dirs, empty) {
		t.Errorf("dir without credentials %s must be filtered; got %v", empty, dirs)
	}
}

// A nil config (load failure) must degrade to the legacy pair, never panic.
func TestResolveCredConfigDirs_NilConfigFallsBackToLegacyPair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacy := filepath.Join(home, ".claude")
	writeCreds(t, legacy)

	dirs := resolveCredConfigDirs(nil, nil)
	if !containsDir(dirs, legacy) {
		t.Errorf("nil config must still cover legacy %s; got %v", legacy, dirs)
	}
}
