package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Remote parity walk on g14 (2026-09-18), finding 3: a session created on a
// remote (the TUI's `add` + `session start --json --no-wait` over SSH) must
// carry the same agent-deck environment and identity block a local session
// gets, with the identity file written on the remote host by the remote's
// own agent-deck. The walk saw none of it in a shell session; a claude
// session could not be checked there. Both are checked here through the
// real CLI boundary: the controller talks to an in-process SSH server that
// runs the same binary under a separate HOME, and the tools it spawns dump
// what they were given.
func TestRemoteCreatePathCarriesIdentity(t *testing.T) {
	bin := channelsCLIBinary(t)
	controller, server, shim := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(controller, ".config", "agent-deck", "config.toml"), fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n", bin), 0600)
	socket := fmt.Sprintf("identity-server-%d", time.Now().UnixNano())
	write(filepath.Join(server, ".config", "agent-deck", "config.toml"), "[tmux]\nsocket_name = '"+socket+"'\n", 0600)
	startParitySSH(t, server, shim)
	// The server's `claude`: records its argv and environment, then stays
	// alive so the spawn verifies. It is found through the SSH exec PATH the
	// server's tmux inherits (the shim dir), the way a real remote's tool is.
	claudeDump := filepath.Join(server, "claude-run.txt")
	write(filepath.Join(shim, "claude"), "#!/bin/sh\n{ printf 'ARGV:'; printf ' %s' \"$@\"; printf '\\n'; env; } > "+claudeDump+"\nsleep 60\n", 0755)

	run := func(home string, args ...string) (string, int) {
		t.Helper()
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
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return out.String() + stderr.String(), code
	}
	controllerMust := func(args ...string) string {
		t.Helper()
		out, code := run(controller, append([]string{"remote", "lab"}, args...)...)
		if code != 0 {
			t.Fatalf("remote lab %v: exit %d: %s", args, code, out)
		}
		return out
	}
	waitFor := func(label string, ready func() bool) {
		t.Helper()
		until := time.Now().Add(15 * time.Second)
		for time.Now().Before(until) {
			if ready() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal(label + ": timeout")
	}
	readWhenContains := func(path, needle string) string {
		t.Helper()
		var data []byte
		waitFor(filepath.Base(path), func() bool {
			var err error
			data, err = os.ReadFile(path)
			return err == nil && strings.Contains(string(data), needle)
		})
		return string(data)
	}
	created := func(out string) string {
		t.Helper()
		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || result.ID == "" {
			t.Fatalf("remote add did not return an id: %v %q", err, out)
		}
		return result.ID
	}
	identityFileOnServer := func(env, id, tool string) {
		t.Helper()
		var file string
		for _, line := range strings.Split(env, "\n") {
			if strings.HasPrefix(line, "AGENTDECK_IDENTITY_FILE=") {
				file = strings.TrimPrefix(line, "AGENTDECK_IDENTITY_FILE=")
			}
		}
		if file == "" {
			t.Fatalf("%s session: AGENTDECK_IDENTITY_FILE not exported:\n%s", tool, env)
		}
		if !strings.HasPrefix(file, server+"/") {
			t.Fatalf("%s session: identity file %q is not under the remote's home %q (the remote's own agent-deck must write it)", tool, file, server)
		}
		block, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s session: identity file not written on the remote: %v", tool, err)
		}
		for _, want := range []string{"- session id: " + id, "- tool: " + tool, "agent-deck session current --json"} {
			if !strings.Contains(string(block), want) {
				t.Fatalf("%s session: identity block lacks %q:\n%s", tool, want, block)
			}
		}
	}
	stopAll := func() {
		for _, title := range []string{"ident-claude", "ident-shell"} {
			run(server, "session", "stop", title)
		}
	}
	t.Cleanup(stopAll)

	// claude, through the exact create path the TUI dialog uses.
	claudeID := created(controllerMust("add", server, "--title", "ident-claude", "--cmd", "claude", "--json"))
	controllerMust("session", "start", "--json", "--no-wait", claudeID)
	claudeEnv := readWhenContains(claudeDump, "AGENTDECK_INSTANCE_ID=")
	argv := strings.SplitN(claudeEnv, "\n", 2)[0]
	if !strings.Contains(argv, "--append-system-prompt-file ") {
		t.Fatalf("claude on the remote was not handed the identity block: %s", argv)
	}
	for _, want := range []string{"AGENTDECK_INSTANCE_ID=" + claudeID + "\n", "AGENTDECK_PROFILE=", "AGENTDECK_IDENTITY_FILE="} {
		if !strings.Contains(claudeEnv, want) {
			t.Fatalf("claude on the remote lacks %q:\n%s", want, claudeEnv)
		}
	}
	identityFileOnServer(claudeEnv, claudeID, "claude")

	// shell, the walk's own case: created the same way, then asked what it
	// sees, the way the walk asked.
	shellID := created(controllerMust("add", server, "--title", "ident-shell", "--cmd", "shell", "--json"))
	controllerMust("session", "start", "--json", "--no-wait", shellID)
	shellDump := filepath.Join(server, "shell-env.txt")
	controllerMust("session", "send", shellID, "env > "+shellDump, "--no-wait", "--json")
	shellEnv := readWhenContains(shellDump, "AGENTDECK_INSTANCE_ID=")
	for _, want := range []string{"AGENTDECK_INSTANCE_ID=" + shellID + "\n", "AGENTDECK_TOOL=shell\n", "AGENTDECK_TITLE=ident-shell\n", "AGENTDECK_PROFILE="} {
		if !strings.Contains(shellEnv, want) {
			t.Fatalf("shell session on the remote lacks %q:\n%s", want, shellEnv)
		}
	}
	identityFileOnServer(shellEnv, shellID, "shell")
}
