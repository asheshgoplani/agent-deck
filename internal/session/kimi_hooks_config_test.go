package session_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestGetKimiConfigDirPrecedence(t *testing.T) {
	t.Run("explicit agent-deck config wins", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
		t.Setenv("KIMI_CODE_HOME", filepath.Join(home, "ambient"))
		configPath, err := session.GetUserConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, []byte("[kimi]\nconfig_dir = \"~/explicit\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		session.ClearUserConfigCache()
		t.Cleanup(session.ClearUserConfigCache)
		if got, want := session.GetKimiConfigDir(), filepath.Join(home, "explicit"); got != want {
			t.Fatalf("GetKimiConfigDir = %q, want %q", got, want)
		}
	})

	t.Run("ambient root then default", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
		t.Setenv("KIMI_CODE_HOME", filepath.Join(home, "ambient"))
		session.ClearUserConfigCache()
		t.Cleanup(session.ClearUserConfigCache)
		if got, want := session.GetKimiConfigDir(), filepath.Join(home, "ambient"); got != want {
			t.Fatalf("ambient GetKimiConfigDir = %q, want %q", got, want)
		}
		t.Setenv("KIMI_CODE_HOME", "")
		if got, want := session.GetKimiConfigDir(), filepath.Join(home, ".kimi-code"); got != want {
			t.Fatalf("default GetKimiConfigDir = %q, want %q", got, want)
		}
	})
}
