package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Separate CLI processes must initialize the same policy as a TUI. Actual tmux
// options/rendered values are asserted; no prebuilt argv or tmux stub is used.
func TestTitleFormatIndependentCLI(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)
	if testing.Short() {
		t.Skip("subprocess integration")
	}
	bin := filepath.Join(t.TempDir(), "agent-deck")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	for _, tc := range []struct{ name, config, want string }{
		{"custom", "title_format = \"{group}/{name}\"", "work/nested/renamed"},
		{"no prefix", "include_cwd_prefix = false", "renamed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("XDG_CACHE_HOME", "")
			session.ReloadUserConfig()
			t.Cleanup(session.ClearUserConfigCache)
			socket := fmt.Sprintf("title-cli-%d", time.Now().UnixNano())
			cfg := filepath.Join(home, ".agent-deck", "config.toml")
			if err := os.MkdirAll(filepath.Dir(cfg), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte("[tmux]\nsocket_name = \""+socket+"\"\n[display]\n"+tc.config+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			session.ReloadUserConfig()
			inst := session.NewInstanceWithGroupAndTool("original", home, "work/nested", "shell")
			sess := inst.GetTmuxSession()
			run := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("tmux %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("new-session", "-d", "-s", sess.Name, "sleep 300")
			t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", sess.Name).Run() })
			store, err := session.NewStorageWithProfile("title-test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
				t.Fatal(err)
			}
			env := sandboxedCLIEnv(home)
			env = append(env, "TMUX_TMPDIR="+os.Getenv("TMUX_TMPDIR"))
			// This exact CLI implementation is also the endpoint of remote SSH dispatch.
			cmd := exec.Command(bin, "-p", "title-test", "rename", inst.ID, "renamed", "--json")
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rename: %v: %s", err, out)
			}
			if got := run("display-message", "-p", "-t", sess.Name, "#{E:set-titles-string}"); got != tc.want {
				t.Fatalf("rendered=%q, want %q", got, tc.want)
			}
			if tc.name == "custom" {
				// Execute the actual remote CLI endpoint under a separate HOME. Only
				// the SSH transport is replaced; list/rename/storage/tmux all remain real.
				controller := t.TempDir()
				controlConfig := filepath.Join(controller, ".agent-deck", "config.toml")
				if err := os.MkdirAll(filepath.Dir(controlConfig), 0700); err != nil {
					t.Fatal(err)
				}
				config := fmt.Sprintf("[display]\ntitle_format = \"LOCAL/{name}\"\n[remotes.fixture]\nhost = \"fixture\"\nagent_deck_path = %q\nprofile = \"title-test\"\n", bin)
				if err := os.WriteFile(controlConfig, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
				shimDir := t.TempDir()
				script := "#!/bin/sh\nfor arg do command=$arg; done\nexport HOME=\"$TITLE_REMOTE_HOME\" XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME=\nexec /bin/sh -c \"$command\"\n"
				if err := os.WriteFile(filepath.Join(shimDir, "ssh"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				remoteCmd := exec.Command(bin, "remote", "rename", "fixture", inst.ID, "remote-name")
				remoteCmd.Env = append(sandboxedCLIEnv(controller), "TMUX_TMPDIR="+os.Getenv("TMUX_TMPDIR"), "TITLE_REMOTE_HOME="+home, "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				if out, err := remoteCmd.CombinedOutput(); err != nil {
					t.Fatalf("remote rename: %v: %s", err, out)
				}
				if got := run("display-message", "-p", "-t", sess.Name, "#{E:set-titles-string}"); got != "work/nested/remote-name" {
					t.Fatalf("remote endpoint used wrong config: %q", got)
				}
			}

		})
	}
}
