package session

// Issue #680 — TELEGRAM_STATE_DIR (and any conductor-only env var
// placed in a [groups.<name>.claude].env_file) silently leaks into
// every child session that joins the conductor's group. The telegram
// plugin then auto-starts one `bun telegram` poller per child, all
// racing the same bot token via getUpdates (single-consumer API).
// Messages get delivered to a random child and silently disappear
// from the user's point of view.
//
// Reported impact: 10 concurrent pollers on one conductor bot token,
// ~10% delivery rate to the intended conductor.
//
// Fix: when the Claude session is NOT itself a conductor AND the
// session's group has a paired [conductors.<group>] block, append an
// `unset TELEGRAM_STATE_DIR` to the spawn env so the poller does not
// auto-start in children. TELEGRAM_STATE_DIR is the only known-bad
// conductor-only env var today; keeping this hardcoded avoids a
// schema change at release-cut time. Users with legitimate reasons
// to inherit TELEGRAM_STATE_DIR into a child can split their envrc
// files (documented workaround, unchanged).

import (
	"os"
	"strings"
	"testing"
	"time"
)

// resetUserConfigCache installs a test-crafted UserConfig and aligns the
// cache mtime with the real config.toml mtime (if any) so LoadUserConfig
// does not silently refresh the cache from disk mid-test. Returns a
// restore func.
func resetUserConfigCache(t *testing.T, cfg *UserConfig) func() {
	t.Helper()
	var fileMtime time.Time
	if configPath, pathErr := GetUserConfigPath(); pathErr == nil {
		if st, err := os.Stat(configPath); err == nil {
			fileMtime = st.ModTime()
		}
	}
	userConfigCacheMu.Lock()
	prev := userConfigCache
	prevMtime := userConfigCacheMtime
	userConfigCache = cfg
	userConfigCacheMtime = fileMtime
	userConfigCacheMu.Unlock()
	return func() {
		userConfigCacheMu.Lock()
		userConfigCache = prev
		userConfigCacheMtime = prevMtime
		userConfigCacheMu.Unlock()
	}
}

// configWithConductorAndGroup returns a UserConfig that mirrors the
// documented conductor pattern: [conductors.<name>] AND
// [groups.<name>.claude].env_file both pointing at the same envrc.
func configWithConductorAndGroup(name, envFilePath string) *UserConfig {
	cfg := &UserConfig{
		MCPs: make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{
			name: {
				Claude: ConductorClaudeSettings{
					EnvFile: envFilePath,
				},
			},
		},
		Groups: map[string]GroupSettings{
			name: {
				Claude: GroupClaudeSettings{
					EnvFile: envFilePath,
				},
			},
		},
	}
	// IgnoreMissingEnvFiles default: true. Leave Shell zero so
	// buildSourceCmd uses the `[ -f ... ] && source ...` form.
	return cfg
}

// Child session in a conductor's group MUST strip TELEGRAM_STATE_DIR
// after sourcing the group env_file so the telegram plugin does not
// auto-start in children and race the conductor for getUpdates.
func TestIssue680_ChildSession_StripsTelegramStateDir(t *testing.T) {
	cfg := configWithConductorAndGroup("opengraphdb", "/tmp/opengraphdb.envrc")
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "child-1",
		GroupPath:   "opengraphdb",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	if !strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("child session in conductor group should strip TELEGRAM_STATE_DIR\nbuildEnvSourceCommand() = %q", got)
	}
}

// Conductor session MUST keep TELEGRAM_STATE_DIR — that's literally
// why the conductor pattern exists.
func TestIssue680_ConductorSession_KeepsTelegramStateDir(t *testing.T) {
	cfg := configWithConductorAndGroup("opengraphdb", "/tmp/opengraphdb.envrc")
	defer resetUserConfigCache(t, cfg)()

	conductor := &Instance{
		Title:       "conductor-opengraphdb",
		GroupPath:   "opengraphdb",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := conductor.buildEnvSourceCommand()

	if strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("conductor session must NOT strip TELEGRAM_STATE_DIR\nbuildEnvSourceCommand() = %q", got)
	}
}

// S8 (v1.7.40) broadened the strip: TELEGRAM_STATE_DIR is a
// telegram-plugin-only env var, so any non-channel-owning claude
// session should lose it regardless of whether the group has a paired
// conductors block. The old narrow predicate here was a
// conservative-safety compromise that left `agent-deck launch`
// children leaking pollers. This test now asserts the new (broader)
// behavior: child in a plain group with env_file STILL strips TSD.
func TestIssue680_ChildSession_NoConductorBlock_StripsUnderS8(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{}, // no matching conductor
		Groups: map[string]GroupSettings{
			"plain-group": {
				Claude: GroupClaudeSettings{
					EnvFile: "/tmp/plain.envrc",
				},
			},
		},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "child-1",
		GroupPath:   "plain-group",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	if !strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("S8 broadening: non-channel-owning child must strip TELEGRAM_STATE_DIR even in a non-conductor group\nbuildEnvSourceCommand() = %q", got)
	}
}

// S8 (v1.7.40): under the broadened predicate the strip fires even
// when there is no env_file to source. The strip is appended after
// the sources slice and stands on its own as a final unconditional
// unset for non-channel-owning claude sessions. The original issue
// #680 concern (guarding a "stray unset" when nothing is sourced) is
// obsolete: the strip is now load-bearing in exactly that case.
func TestIssue680_ChildSession_NoGroupEnvFile_StripsUnderS8(t *testing.T) {
	cfg := &UserConfig{
		MCPs: make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{
			"opengraphdb": {Claude: ConductorClaudeSettings{EnvFile: "/tmp/c.envrc"}},
		},
		Groups: map[string]GroupSettings{
			"opengraphdb": {}, // no EnvFile
		},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "child-1",
		GroupPath:   "opengraphdb",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	if !strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("S8 broadening: child must strip TSD even without env_file\nbuildEnvSourceCommand() = %q", got)
	}
}
