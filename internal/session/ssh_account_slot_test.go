package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSSHLoginAccountCapturedAtCreation(t *testing.T) {
	for _, tool := range []string{"shell", "claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			t.Setenv("AGENTDECK_ACCOUNT", "person_a")
			inst := NewInstanceWithTool("first", t.TempDir(), tool)
			t.Setenv("AGENTDECK_ACCOUNT", "person_b")
			next := NewInstance("second", t.TempDir())
			if inst.Account != "person_a" || next.Account != "person_b" {
				t.Fatalf("saved slots = %q, %q", inst.Account, next.Account)
			}
			inst.Account = "override"
			if inst.Account != "override" {
				t.Fatal("explicit override lost")
			}
		})
	}
}

func TestSSHUnknownAccountRefusesStartBeforeSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	config := filepath.Join(home, "config", "agent-deck")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte("[profiles.person_a.claude]\nconfig_dir = '~/.claude-a'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"claude", "codex", "shell"} {
		inst := NewInstanceWithTool("invalid", home, tool)
		inst.Account = "missing"
		if err := inst.Start(); err == nil {
			t.Errorf("%s started with unknown account", tool)
			inst.Kill()
		}
	}
}

func TestSSHNamedAccountActualChildStartRestart(t *testing.T) {
	skipIfNoTmuxBinary(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, filepath.Join(home, name))
	}
	configDir := filepath.Join(home, "XDG_CONFIG_HOME", "agent-deck")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(home, "account-a"), filepath.Join(home, "account-b")
	envFile := filepath.Join(home, "probe.env")
	if err := os.WriteFile(envFile, []byte("export PATH="+strconv.Quote(filepath.Join(home, "bin")+":"+os.Getenv("PATH"))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[shell]\nenv_files = ['%s']\n[profiles.person_a.claude]\nconfig_dir = '%s'\n[profiles.person_a.codex]\nconfig_dir = '%s'\n[profiles.person_b.claude]\nconfig_dir = '%s'\n[profiles.person_b.codex]\nconfig_dir = '%s'\n", envFile, first, first, second, second)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(home, "child-env")
	probe := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then printf '2.0.0\\n'; exit 0; fi\nprintf '%s|%s|%s|%s\\n' \"$AGENTDECK_ACCOUNT\" \"$CODEX_HOME\" \"$CLAUDE_CONFIG_DIR\" \"$CLAUDE_SECURESTORAGE_CONFIG_DIR\" >> '" + record + "'\nexec sleep 600\n"
	for _, tool := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(probe), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "wrong-codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "wrong-claude"))
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", filepath.Join(home, "wrong-storage"))
	for _, tool := range []string{"claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			t.Setenv("AGENTDECK_ACCOUNT", "person_a")
			inst := NewInstanceWithTool("probe-"+tool, home, tool)
			inst.Command = tool
			t.Cleanup(func() { inst.Kill() })
			before, _ := os.ReadFile(record)
			if err := inst.Start(); err != nil {
				t.Fatal(err)
			}
			expect := func(previous int) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					data, _ := os.ReadFile(record)
					if len(data) > previous {
						line := strings.TrimSpace(string(data[previous:]))
						fields := strings.Split(line, "|")
						if len(fields) != 4 || fields[0] != "person_a" {
							t.Fatalf("child slot: %q", line)
						}
						if tool == "codex" && fields[1] != first {
							t.Fatalf("child CODEX_HOME: %q", line)
						}
						if tool == "claude" && (fields[2] != first || fields[3] != first) {
							t.Fatalf("child Claude dirs: %q", line)
						}
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				output, _ := inst.tmuxSession.CapturePane()
				t.Fatalf("no actual child probe record; pane: %s", output)
			}
			expect(len(before))
			before, _ = os.ReadFile(record)
			t.Setenv("AGENTDECK_ACCOUNT", "person_b")
			if err := inst.Restart(); err != nil {
				t.Fatal(err)
			}
			expect(len(before))
		})
	}
}
