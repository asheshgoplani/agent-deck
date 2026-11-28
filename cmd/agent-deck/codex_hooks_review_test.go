package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestAddCodexHooksExplainsSkippedInstallation(t *testing.T) {
	for _, tc := range []struct {
		name, tool, reason string
		extra              []string
	}{
		{"other tool", "shell", "does not use Codex", nil},
		{"SSH", "codex", "remote host", []string{"--ssh", "alice@example.invalid", "--remote-path", "/srv/project"}},
		{"sandbox", "codex", "sandbox", []string{"--sandbox", "--sandbox-image", "example/sandbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			h := filepath.Join(home, "codex")
			env := []string{"CODEX_HOME=" + h}
			args := []string{"add", "--install-codex-hooks", "--json", "--no-parent", "-t", "retained", "-c", tc.tool}
			args = append(args, tc.extra...)
			args = append(args, home)
			out, stderr, code := runAgentDeckEnv(t, home, "", env, args...)
			if code != 0 || !json.Valid([]byte(out)) || !strings.Contains(stderr, tc.reason) || strings.Count(stderr, "Codex hooks:") != 1 {
				t.Fatalf("missing skip result: %d %s %s", code, out, stderr)
			}
			if _, err := os.Stat(h); !os.IsNotExist(err) {
				t.Fatalf("wrote unrelated Codex home: %v", err)
			}
			listed, stderr, code := runAgentDeckEnv(t, home, "", env, "list", "--json")
			if code != 0 || !json.Valid([]byte(listed)) || !strings.Contains(listed, "retained") {
				t.Fatalf("registration lost: %d %s %s", code, listed, stderr)
			}
		})
	}
}
func TestAddCodexHooksExplainsExistingNotify(t *testing.T) {
	for _, tc := range []struct{ name, content, reason string }{
		{"installed", codexNotifyLine + "\n", "already installed"},
		{"custom", "notify = [\"other\"]\n", "existing notify setting preserved"},
		{"quoted", "\"notify\" = [\"other\"]\n", "existing notify setting preserved"},
		{"empty", "notify = []\n", "existing notify setting preserved"},
		{"legacy", "[notify]\nprogram = [\"agent-deck\", \"codex-notify\"]\n", "existing notify setting preserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			h := filepath.Join(home, "codex")
			path := filepath.Join(h, "config.toml")
			if err := os.MkdirAll(h, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o640); err != nil {
				t.Fatal(err)
			}
			out, stderr, code := runAgentDeckEnv(t, home, "", []string{"CODEX_HOME=" + h}, "add", "--install-codex-hooks", "--json", "--no-parent", "-c", "codex", home)
			if code != 0 || !json.Valid([]byte(out)) || !strings.Contains(stderr, tc.reason) || strings.Count(stderr, "Codex hooks:") != 1 || strings.Contains(stderr, "Warning: Codex hooks:") {
				t.Fatalf("incorrect informational result: %d %s %s", code, out, stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.content {
				t.Fatalf("existing notify changed: %s %v", data, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("mode changed: %v %v", info, err)
			}
		})
	}
}
func TestCodexHooksManualInstallUpgradesPreservedLegacy(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmarked", true: "marked"}[marked], func(t *testing.T) {
			home := t.TempDir()
			h := filepath.Join(home, "codex")
			path := filepath.Join(h, "config.toml")
			legacy := "[notify]\nprogram = [\"agent-deck\", \"codex-notify\"]\n"
			if marked {
				legacy = codexNotifyMarkerBegin + "\n" + legacy + codexNotifyMarkerEnd + "\n"
			}
			original := "# keep this setting\nmodel = \"gpt-5\"\n\n" + legacy
			if err := os.MkdirAll(h, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
				t.Fatal(err)
			}
			env := []string{"CODEX_HOME=" + h}
			out, stderr, code := runAgentDeckEnv(t, home, "", env, "add", "--install-codex-hooks", "--json", "--no-parent", "-c", "codex", home)
			if code != 0 || !json.Valid([]byte(out)) {
				t.Fatalf("creation failed: %d %s %s", code, out, stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatalf("creation migrated legacy settings: %s %v", data, err)
			}
			out, stderr, code = runAgentDeckEnv(t, home, "", env, "codex-hooks", "install")
			if code != 0 {
				t.Fatalf("manual migration failed: %d %s %s", code, out, stderr)
			}
			data, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Model  string   `toml:"model"`
				Notify []string `toml:"notify"`
			}
			if err := toml.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.Model != "gpt-5" || !reflect.DeepEqual(cfg.Notify, []string{"agent-deck", "codex-notify"}) || !strings.Contains(string(data), "# keep this setting") {
				t.Fatalf("manual migration lost settings: %s", data)
			}
		})
	}
}

func TestCodexHooksManualInstallPreservesDanglingSymlink(t *testing.T) {
	home := t.TempDir()
	h := filepath.Join(home, "codex")
	path := filepath.Join(h, "config.toml")
	target := filepath.Join(home, "target.toml")
	if err := os.MkdirAll(h, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runAgentDeckEnv(t, home, "", []string{"CODEX_HOME=" + h}, "codex-hooks", "install")
	if code != 0 {
		t.Fatalf("manual installation into dangling link failed: %d %s", code, stderr)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), codexNotifyLine) {
		t.Fatalf("target not installed: %s %v", data, err)
	}
}
