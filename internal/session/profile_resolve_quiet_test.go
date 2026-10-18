package session

import (
	"os"
	"path/filepath"
	"testing"
)

// ResolveProfileForStorageQuiet backs a poll: remote-agent checks with it
// whether the host would now pick another profile (#2756). SaveConfig
// rewrites config.json in place, so the poll can read the file empty or
// half-written. That must be an error the caller keeps its profile over, not
// the default profile, while ResolveProfileForStorage falls back as before.
func TestResolveProfileForStorageQuiet_UnreadableConfigIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	withCleanProfileEnv(t)

	if err := SaveConfig(&Config{DefaultProfile: "work"}); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveProfileForStorageQuiet(""); err != nil || got != "work" {
		t.Fatalf("readable config: got %q, %v; want work", got, err)
	}
	path, err := GetConfigPath()
	if err != nil {
		t.Fatal(err)
	}

	for _, partial := range []string{"", `{"default_profile": "wo`} {
		if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := ResolveProfileForStorageQuiet(""); err == nil {
			t.Fatalf("config %q: got %q and no error", partial, got)
		}
		// A missing inferred profile falls back to default_profile, which
		// needs the config as well.
		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-ghost"))
		if got, err := ResolveProfileForStorageQuiet(""); err == nil {
			t.Fatalf("config %q, missing inferred profile: got %q and no error", partial, got)
		}
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		// AGENTDECK_PROFILE does not read the config.
		t.Setenv("AGENTDECK_PROFILE", "env")
		if got, err := ResolveProfileForStorageQuiet(""); err != nil || got != "env" {
			t.Fatalf("config %q, AGENTDECK_PROFILE=env: got %q, %v; want env", partial, got, err)
		}
		t.Setenv("AGENTDECK_PROFILE", "")
		if got, err := ResolveProfileForStorage(""); err != nil || got != DefaultProfile {
			t.Fatalf("config %q: ResolveProfileForStorage got %q, %v; want the %s fallback", partial, got, err, DefaultProfile)
		}
	}
}
