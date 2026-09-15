package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type creationCatalogRunner struct {
	calls int
	err   error
}

func (r *creationCatalogRunner) FetchCreationCatalog(context.Context) (*session.RemoteCreationCatalog, error) {
	r.calls++
	return &session.RemoteCreationCatalog{Version: 1, Commands: map[string][]session.RemoteCreationField{"add": {{Name: "json"}}, "launch": {{Name: "message-file", TakesValue: true}}}}, r.err
}
func TestRemoteCreationPublicPreflight(t *testing.T) {
	for _, args := range [][]string{{"add", "--json"}, {"launch", "--message-file", "-"}} {
		r := &creationCatalogRunner{}
		if err := preflightRemoteCreation(context.Background(), r, args); err != nil || r.calls != 1 {
			t.Fatalf("%v: calls=%d err=%v", args, r.calls, err)
		}
	}
	for _, args := range [][]string{{"add", "--typo"}, {"launch", "--message-file", "/controller/task"}} {
		r := &creationCatalogRunner{}
		if err := preflightRemoteCreation(context.Background(), r, args); err == nil {
			t.Fatalf("unsafe args allowed: %v", args)
		}
	}
	r := &creationCatalogRunner{err: errors.New("old binary")}
	if err := preflightRemoteCreation(context.Background(), r, []string{"add", "--json"}); err == nil {
		t.Fatal("old binary allowed")
	}
	for _, args := range [][]string{{"add", "--help"}, {"launch", "-h"}, {"add", "--capabilities", "--json"}, {"session", "show", "x"}} {
		r := &creationCatalogRunner{err: errors.New("must not fetch")}
		if err := preflightRemoteCreation(context.Background(), r, args); err != nil || r.calls != 0 {
			t.Fatalf("%v: calls=%d err=%v", args, r.calls, err)
		}
	}
}

func TestRemoteCreationMessageFileAfterEveryBoolean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.txt")
	if err := os.WriteFile(path, []byte("literal query"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, field := range creationCommandFields("launch") {
		if field.TakesValue {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			args, input, closeInput, err := remoteMessageInput([]string{"launch", "--" + field.Name, "--message-file", path})
			if err != nil {
				t.Fatal(err)
			}
			defer closeInput()
			data, err := io.ReadAll(input)
			if err != nil || string(data) != "literal query" {
				t.Fatalf("message lost: %s %v", data, err)
			}
			if strings.Contains(strings.Join(args, " "), path) {
				t.Fatalf("controller filename leaked: %v", args)
			}
		})
	}
}

type legacyCreationCatalogRunner struct{}

func (legacyCreationCatalogRunner) FetchCreationCatalog(context.Context) (*session.RemoteCreationCatalog, error) {
	return session.LegacyRemoteCreationCatalog(), nil
}

func TestOldRemotePublicCreationPreflight(t *testing.T) {
	runner := legacyCreationCatalogRunner{}
	for _, args := range [][]string{
		{"add", "--title", "legacy", "-c", "claude", "-g", "work", "-w", "fix", "-b", "--sandbox", "--additional-path", "/srv/other", "/srv/repo"},
		{"launch", "-m", "literal\nmessage", "--json", "/srv/repo"},
		{"launch", "--message-file", "-", "/srv/repo"},
	} {
		if err := preflightRemoteCreation(context.Background(), runner, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, option := range []string{"startup-query", "effort", "yolo", "parent", "account", "mcp", "extra-arg", "model", "skip-permissions"} {
		err := preflightRemoteCreation(context.Background(), runner, []string{"add", "--" + option, "value"})
		want := "unsupported remote creation field --" + option + "; update the remote"
		if err == nil || err.Error() != want {
			t.Errorf("--%s: %v; want %q", option, err, want)
		}
	}
}

func TestOldRemoteCLIRefusalIsOneLine(t *testing.T) {
	bin := channelsCLIBinary(t)
	home, shim := t.TempDir(), t.TempDir()
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[remotes.legacy]\nhost = 'fake-legacy'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$*" in
 *--capabilities*) printf 'flag provided but not defined: -capabilities\nUsage: PRIVATE REMOTE STDERR\n' >&2; exit 2 ;;
 *) printf '{"id":"legacy-created"}\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []bool{false, true} {
		args := []string{"remote", "legacy", "add", "--json", "-t", "legacy", "/srv/project"}
		if unsupported {
			args = []string{"remote", "legacy", "add", "--effort", "high", "/srv/project"}
		}
		cmd := exec.Command(bin, args...)
		cmd.Dir = home
		for _, kv := range os.Environ() {
			key := strings.SplitN(kv, "=", 2)[0]
			if key == "HOME" || key == "PATH" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "AGENTDECK_") || strings.HasPrefix(key, "TMUX") {
				continue
			}
			cmd.Env = append(cmd.Env, kv)
		}
		cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+shim+":"+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if unsupported {
			want := "unsupported remote creation field --effort; update the remote"
			line := strings.TrimSpace(string(output))
			if err == nil || !strings.Contains(line, want) || strings.ContainsAny(line, "\r\n") {
				t.Fatalf("refusal: %q %v", output, err)
			}
		} else if err != nil || !strings.Contains(string(output), "legacy-created") || strings.Contains(string(output), "PRIVATE") {
			t.Fatalf("legacy add: %q %v", output, err)
		}
	}
}
