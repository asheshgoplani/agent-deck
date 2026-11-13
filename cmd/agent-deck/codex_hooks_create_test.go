package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Exercise registration through the real CLI without launching an agent.
// Remote add invokes this same command on the owning host.
func TestAddCodexInstallsMissingNotify(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		preserve      bool
	}{
		{name: "missing config"},
		{name: "existing settings", content: "# keep this comment\nmodel = \"gpt-5\"\n\n[projects.\"/work\"]\ntrust_level = \"trusted\"\n"},
		{name: "nested notify", content: "[profiles.work]\nnotify = [\"profile-notifier\"]\n"},
		{name: "custom notify", content: "notify = [\"my-notifier\"]\n", preserve: true},
		{name: "quoted notify", content: "\"notify\" = [\"my-notifier\"]\n", preserve: true},
		{name: "disabled notify", content: "notify = []\n", preserve: true},
		{name: "installed", content: codexNotifyLine + "\n", preserve: true},
		{name: "legacy", content: "[notify]\nprogram = [\"agent-deck\", \"codex-notify\"]\n", preserve: true},
		{name: "invalid TOML", content: "model = [\n", preserve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			codexHome := filepath.Join(home, "codex home")
			configPath := filepath.Join(codexHome, "config.toml")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.content != "" {
				if err := os.WriteFile(configPath, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, title := range []string{"first", "second"} {
				output, stderr, code := runAgentDeckEnv(t, home, "", []string{"CODEX_HOME=" + codexHome, "AGENTDECK_SKIP_UPDATE_CHECK=1"},
					"add", "--install-codex-hooks", "--json", "--no-parent", "-t", title, "-c", "codex", home)
				if code != 0 {
					t.Fatalf("add: exit=%d\n%s\n%s", code, output, stderr)
				}
				if !json.Valid([]byte(output)) {
					t.Fatalf("add polluted JSON output: %s", output)
				}
				got, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if tc.preserve {
					if string(got) != tc.content {
						t.Fatalf("existing notify or invalid config changed: %q", got)
					}
					continue
				}
				var cfg map[string]any
				if err := toml.Unmarshal(got, &cfg); err != nil {
					t.Fatal(err)
				}
				if codexHooksStateForConfig(configPath) != codexHooksInstalled || cfg["notify"] == nil {
					t.Fatalf("missing root notify after add: %s", got)
				}
				if strings.Count(string(got), codexNotifyMarkerBegin) != 1 || !strings.HasSuffix(string(got), tc.content) {
					t.Fatalf("config was duplicated or existing bytes changed: %s", got)
				}
				info, err := os.Stat(configPath)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("config permissions changed: %v, %v", info, err)
				}
			}
		})
	}
}

func TestAddCodexHooksUsesAccountHome(t *testing.T) {
	home := t.TempDir()
	accountHome := filepath.Join(home, "account-codex")
	defaultHome := filepath.Join(home, "default-codex")
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[profiles.work.codex]\nconfig_dir = " + strconv.Quote(accountHome) + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", []string{"CODEX_HOME=" + defaultHome},
		"add", "--install-codex-hooks", "--json", "--no-parent", "--account", "work", "-c", "codex", home)
	if code != 0 {
		t.Fatalf("add with account: exit=%d\n%s\n%s", code, stdout, stderr)
	}
	if codexHooksStateForConfig(filepath.Join(accountHome, "config.toml")) != codexHooksInstalled {
		t.Fatal("did not install into the selected account's home")
	}
	if _, err := os.Stat(defaultHome); !os.IsNotExist(err) {
		t.Fatalf("wrote to the wrong Codex home: %v", err)
	}
}

func TestInstallMissingCodexHooksIsolation(t *testing.T) {
	for _, kind := range []string{"other tool", "ssh", "sandbox"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", filepath.Join(home, "default-codex"))
			inst := session.NewInstanceWithTool("hooks", home, "codex")
			switch kind {
			case "other tool":
				inst.Tool = "shell"
			case "ssh":
				inst.SSHHost = "example.invalid"
			case "sandbox":
				inst.Sandbox = session.NewSandboxConfig("example/sandbox")
			case "command home":
				inst.Command = "CODEX_HOME='" + filepath.Join(home, "command home") + "' codex"
			}
			if err := installMissingCodexHooks(inst); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(home, "default-codex")); !os.IsNotExist(err) {
				t.Fatalf("wrote to the caller's Codex home: %v", err)
			}
			if kind == "command home" && codexHooksStateForConfig(filepath.Join(home, "command home", "config.toml")) != codexHooksInstalled {
				t.Fatal("did not install in the command's Codex home")
			}
		})
	}
}

func TestInstallMissingCodexHooksRefusesUnsafeWrites(t *testing.T) {
	for _, kind := range []string{"read-only", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			path := filepath.Join(home, "config.toml")
			original := "# keep\nmodel = \"gpt-5\"\n"
			target := path
			switch kind {
			case "read-only":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses read-only permissions")
				}
				if err := os.WriteFile(path, []byte(original), 0o400); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target = filepath.Join(home, "target.toml")
				if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := installMissingCodexHooks(session.NewInstanceWithTool("hooks", home, "codex")); err == nil {
				t.Fatal("expected a warning-worthy error")
			}
			if kind != "directory" {
				got, err := os.ReadFile(target)
				if err != nil || string(got) != original {
					t.Fatalf("changed protected config: %q, %v", got, err)
				}
			}
		})
	}
}

func TestInstallMissingCodexHooksRejectsRelativeHome(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	t.Setenv("CODEX_HOME", "relative-codex")
	if err := installMissingCodexHooks(session.NewInstanceWithTool("hooks", home, "codex")); err == nil {
		t.Fatal("a relative Codex home must not be resolved against the caller's directory")
	}
	if _, err := os.Stat(filepath.Join(home, "relative-codex")); !os.IsNotExist(err) {
		t.Fatalf("wrote to a relative Codex home: %v", err)
	}
}

func TestInstallMissingCodexHooksConcurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	inst := session.NewInstanceWithTool("hooks", home, "codex")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := installMissingCodexHooks(inst); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), codexNotifyMarkerBegin) != 1 {
		t.Fatalf("concurrent creation duplicated hooks: %s", got)
	}
}

func TestLaunchCodexInstallsMissingNotify(t *testing.T) {
	home, project := setupLaunchGroupResolveTest(t)
	codexHome := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codexHome)
	stdout, stderr, code := runAgentDeck(t, home, "launch", "--install-codex-hooks", project, "-c", "codex", "-t", "hooks", "--no-parent", "--no-wait", "--json")
	if code != 0 {
		t.Fatalf("launch: exit=%d\n%s\n%s", code, stdout, stderr)
	}
	if codexHooksStateForConfig(filepath.Join(codexHome, "config.toml")) != codexHooksInstalled {
		t.Fatal("launch did not install the notify hook")
	}
}

func TestAddCodexHooksRequiresOptIn(t *testing.T) {
	home := t.TempDir()
	h := filepath.Join(home, "codex")
	_, stderr, code := runAgentDeckEnv(t, home, "", []string{"CODEX_HOME=" + h}, "add", "--json", "--no-parent", "-c", "codex", home)
	if code != 0 {
		t.Fatal(stderr)
	}
	if _, err := os.Stat(h); !os.IsNotExist(err) {
		t.Fatal("creation without opt-in modified Codex home", err)
	}
}
func TestInstallMissingCodexHooksRejectsCustomCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	inst := session.NewInstanceWithTool("hooks", home, "codex")
	inst.Command = "codex --model example"
	if err := installMissingCodexHooks(inst); err == nil {
		t.Fatal("expected refusal")
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("wrote ambiguous home", err)
	}
}

func TestAddCodexDoesNotReinstallRemovedHooks(t *testing.T) {
	home := t.TempDir()
	h := filepath.Join(home, "codex")
	env := []string{"CODEX_HOME=" + h}
	for _, args := range [][]string{
		{"add", "--install-codex-hooks", "--no-parent", "-t", "first", "-c", "codex", home},
		{"codex-hooks", "uninstall"},
		{"add", "--no-parent", "-t", "second", "-c", "codex", home},
	} {
		_, stderr, code := runAgentDeckEnv(t, home, "", env, args...)
		if code != 0 {
			t.Fatal(stderr)
		}
	}
	if codexHooksStateForConfig(filepath.Join(h, "config.toml")) != codexHooksNotInstalled {
		t.Fatal("ordinary creation undid uninstall")
	}
}

func TestAddCodexHookFailurePreservesRegistration(t *testing.T) {
	for _, kind := range []string{"locked", "custom command"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			codexHome := filepath.Join(home, "codex")
			command := "codex"
			if kind == "locked" {
				lock, err := session.AcquireConfigFileLock(filepath.Join(codexHome, "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			} else {
				command = "codex --model example"
			}
			env := []string{"CODEX_HOME=" + codexHome}
			out, stderr, code := runAgentDeckEnv(t, home, "", env, "add", "--install-codex-hooks", "--json", "--no-parent", "-t", "retained", "-c", command, home)
			if code != 0 || !json.Valid([]byte(out)) || !strings.Contains(stderr, "Warning: Codex hooks:") {
				t.Fatalf("expected successful JSON registration with warning: %d %s %s", code, out, stderr)
			}
			listed, stderr, code := runAgentDeckEnv(t, home, "", env, "list", "--json")
			if code != 0 || !strings.Contains(listed, "retained") {
				t.Fatalf("registration lost: %d %s %s", code, listed, stderr)
			}
			if _, err := os.Stat(filepath.Join(codexHome, "config.toml")); !os.IsNotExist(err) {
				t.Fatalf("unexpected config write: %v", err)
			}
		})
	}
}
