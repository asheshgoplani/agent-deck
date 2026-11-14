package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestCodexHooksCommandsRespectConfigLock(t *testing.T) {
	for _, command := range []string{"install", "uninstall"} {
		t.Run(command, func(t *testing.T) {
			home := t.TempDir()
			codexHome := filepath.Join(home, "codex")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(codexHome, "config.toml")
			original := "model = \"example\"\n"
			if command == "uninstall" {
				original = codexNotifyLine + "\n" + original
			}
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			lock, err := session.AcquireConfigFileLock(path)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()
			env := []string{"CODEX_HOME=" + codexHome}
			_, stderr, code := runAgentDeckEnv(t, home, "", env, "codex-hooks", command)
			lock.Release()
			if code == 0 || !strings.Contains(stderr, "locking codex config") {
				t.Fatalf("command ignored lock: %d %s", code, stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatalf("locked config changed: %s %v", data, err)
			}
			_, stderr, code = runAgentDeckEnv(t, home, "", env, "codex-hooks", command)
			if code != 0 {
				t.Fatalf("retry after release failed: %d %s", code, stderr)
			}
		})
	}
}
