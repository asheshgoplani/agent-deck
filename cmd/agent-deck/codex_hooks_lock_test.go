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

func TestCodexHooksUninstallWithoutConfigSkipsLock(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	env := []string{"CODEX_HOME=" + codexHome}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "codex-hooks", "uninstall")
	if code != 0 || !strings.Contains(stdout, "No agent-deck Codex hook found") {
		t.Fatalf("uninstall without config: %d %s %s", code, stdout, stderr)
	}
	if _, err := os.Stat(codexHome); !os.IsNotExist(err) {
		t.Fatalf("no-op uninstall created the codex dir: %v", err)
	}
}
