package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRemoteCompositionManagementHelpIsLocal(t *testing.T) {
	for _, command := range []string{"", "add", "list", "drain", "exec"} {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(command+"/"+flag, func(t *testing.T) {
				home := t.TempDir()
				config := filepath.Join(home, ".config", "agent-deck")
				if err := os.MkdirAll(config, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte("[invalid config"), 0600); err != nil {
					t.Fatal(err)
				}
				before := snapshotTree(t, home)
				args := []string{"remote"}
				if command != "" {
					args = append(args, command)
				}
				out, stderr, code := runAgentDeck(t, home, append(args, flag)...)
				if code != 0 || !strings.Contains(out+stderr, "Usage: agent-deck remote "+command) {
					t.Fatalf("local help: exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if after := snapshotTree(t, home); !reflect.DeepEqual(before, after) {
					t.Fatalf("help changed controller HOME: before=%v after=%v", before, after)
				}
			})
		}
	}
}

func TestRemoteCompositionForwardsCommandHelp(t *testing.T) {
	controller, remote, shim := t.TempDir(), t.TempDir(), t.TempDir()
	startParitySSH(t, remote, shim)
	server := filepath.Join(shim, "record-argv")
	if err := os.WriteFile(server, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'server stderr' >&2\nexit 43\n"), 0700); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(controller, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n[remotes.remove]\nhost = 'test-host'\nagent_deck_path = '%s'\n", server, server)
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
		code int
	}{
		{"named_long", []string{"lab", "session", "show", "--help"}, "session\nshow\n--help\n", 43},
		{"named_short", []string{"lab", "list", "-h"}, "list\n-h\n", 43},
		{"explicit", []string{"exec", "lab", "session", "show", "--help"}, "session\nshow\n--help\n", 43},
		{"bare_help_value", []string{"lab", "show", "help"}, "session\nshow\nhelp\n", 43},
		{"collision_escape", []string{"exec", "remove", "list"}, "list\n", 43},
		{"collision_rejected", []string{"remove", "list"}, "", 2},
		{"no_server_command", []string{"lab", "--help"}, "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, channelsCLIBinary(t), append([]string{"remote"}, tc.args...)...)
			for _, kv := range cliEnvForIssue1031(controller) {
				if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "XDG_") {
					cmd.Env = append(cmd.Env, kv)
				}
			}
			cmd.Env = append(cmd.Env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
				"XDG_CONFIG_HOME="+filepath.Join(controller, ".config"), "XDG_DATA_HOME="+filepath.Join(controller, ".local", "share"), "XDG_CACHE_HOME="+filepath.Join(controller, ".cache"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != tc.code || stdout.String() != tc.want {
				t.Fatalf("remote result: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if tc.code == 43 && stderr.String() != "server stderr" {
				t.Fatalf("lost remote stderr: %q", stderr.String())
			}
			if tc.code == 2 && !strings.Contains(stderr.String(), map[string]string{"collision_rejected": "Ambiguous remote name", "no_server_command": "unsupported remote command"}[tc.name]) {
				t.Fatalf("wrong rejection: %q", stderr.String())
			}
			got, err := os.ReadFile(configPath)
			if err != nil || string(got) != config {
				t.Fatalf("controller config changed: %v", err)
			}
		})
	}
}
