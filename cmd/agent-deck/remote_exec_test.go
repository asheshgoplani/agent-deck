package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the actual CLI boundary over an authenticated SSH connection.
// The server runs each remote command under a separate HOME.
func TestRemoteCommandParity(t *testing.T) {
	bin := channelsCLIBinary(t)
	controller, remote, shim := t.TempDir(), t.TempDir(), t.TempDir()
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
	startParitySSH(t, remote, shim)
	run := func(home, stdin string, args ...string) (string, string, int) {
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
		cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+shim+":"+os.Getenv("PATH"), "PARITY_REMOTE_HOME="+remote)
		cmd.Stdin = strings.NewReader(stdin)
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
		return out.String(), stderr.String(), code
	}
	socket := fmt.Sprintf("parity-%d", time.Now().UnixNano())
	write(filepath.Join(remote, ".config", "agent-deck", "config.toml"), "[tmux]\nsocket_name = '"+socket+"'\n[profiles.person_a.claude]\nconfig_dir = '"+filepath.Join(remote, "claude-a")+"'\n[mcps.parity]\ncommand = 'true'\n", 0600)

	title := "parity 'quoted' $value"
	if out, stderr, code := run(controller, "", "remote", "lab", "add", remote, "-t", title, "--json"); code != 0 {
		t.Fatalf("remote add: %d %s %s", code, out, stderr)
	}
	for _, args := range [][]string{
		{"list", "--json"}, {"status", "--json"}, {"session", "show", title, "--json"},
		{"session", "output", "missing", "--json"}, {"session", "start", "missing", "--json"},
		{"session", "stop", "missing", "--json"}, {"session", "restart", "missing", "--json"},
		{"worktree", "info", "missing", "--json"}, {"mcp", "attach", "missing", "none", "--json"},
		{"skill", "attach", "missing", "none"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			local, localErr, localCode := run(remote, "", args...)
			forwarded, remoteErr, remoteCode := run(controller, "", append([]string{"remote", "lab"}, args...)...)
			var l, r any
			if json.Unmarshal([]byte(local), &l) == nil && json.Unmarshal([]byte(forwarded), &r) == nil {
				if !reflect.DeepEqual(l, r) {
					t.Errorf("JSON differs:\nlocal %s\nremote %s", local, forwarded)
				}
			} else if local != forwarded {
				t.Errorf("stdout differs: %q != %q", local, forwarded)
			}
			if localCode != remoteCode || localErr != remoteErr {
				t.Errorf("exit/stderr differs: local %d %q remote %d %q", localCode, localErr, remoteCode, remoteErr)
			}
		})
	}
	// The remote add persisted on the server only, never on the controller.
	if out, _, _ := run(controller, "", "list", "--json"); strings.Contains(out, title) {
		t.Fatalf("remote add wrote controller registry: %s", out)
	}
	for _, args := range [][]string{{"version"}, {"session", "remove", title}, {"remote", "list"}, {"--help"}} {
		out, stderr, code := run(controller, "", append([]string{"remote", "lab"}, args...)...)
		if code == 0 || !strings.Contains(out+stderr, "unsupported remote command") {
			t.Errorf("unsupported %v: %d %q %q", args, code, out, stderr)
		}
	}
	mustRun := func(args ...string) string {
		t.Helper()
		out, stderr, code := run(controller, "", append([]string{"remote", "lab"}, args...)...)
		if code != 0 {
			t.Fatalf("remote %v: %d %s %s", args, code, out, stderr)
		}
		return out
	}
	// Successful lifecycle operations act only on the server's isolated tmux socket.
	t.Cleanup(func() { run(remote, "", "session", "stop", title) })
	for _, command := range []string{"start", "restart", "stop"} {
		args := []string{"session", command, title, "--json"}
		if command == "restart" {
			args = append(args, "--force")
		}
		mustRun(args...)
		show, _, _ := run(remote, "", "session", "show", title, "--json")
		var state map[string]any
		if err := json.Unmarshal([]byte(show), &state); err != nil {
			t.Fatal(err)
		}
		tmuxName, _ := state["tmux_session"].(string)
		probeCmd := exec.Command("tmux", "-L", socket, "has-session", "-t", tmuxName)
		probeCmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + remote}
		probe := probeCmd.Run()
		if (probe == nil) != (command != "stop") {
			t.Fatalf("remote %s tmux state mismatch: %v, show %s", command, probe, show)
		}
	}
	launched := "remote launch"
	t.Cleanup(func() { run(remote, "", "session", "stop", launched) })
	mustRun("launch", remote, "--title", launched, "--cmd", "shell", "--no-wait", "--account", "person_a", "--json")
	mustRun("session", "stop", launched, "--json")
	for _, argv := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", remote}, argv...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	mustRun("add", remote, "--title", "worktree parity", "--worktree", "parity-branch", "--new-branch", "--location", "subdirectory", "--account", "person_a", "--json")
	if out, err := exec.Command("git", "-C", remote, "show-ref", "--verify", "refs/heads/feature/parity-branch").CombinedOutput(); err != nil {
		t.Fatalf("remote branch side effect: %v %s", err, out)
	}
	for _, argv := range [][]string{{"worktree", "list", "--json"}, {"worktree", "info", "worktree parity", "--json"}, {"worktree", "cleanup", "--json"}} {
		local, _, localCode := run(remote, "", argv...)
		forwarded := mustRun(argv...)
		if localCode != 0 || local != forwarded {
			t.Errorf("worktree parity %v: local %d %s remote %s", argv, localCode, local, forwarded)
		}
	}
	// MCP and skill attachment update files in the remote project, without a live AI process.
	project := filepath.Join(remote, "skill-project")
	write(filepath.Join(project, "README.md"), "fixture\n", 0600)
	mustRun("add", project, "--title", "attachments", "--cmd", "claude", "--json")
	mustRun("mcp", "attach", "attachments", "parity", "--json")
	if data, err := os.ReadFile(filepath.Join(project, ".mcp.json")); err != nil || !strings.Contains(string(data), "parity") {
		t.Fatalf("remote MCP side effect: %v %s", err, data)
	}
	write(filepath.Join(remote, ".config", "agent-deck", "skills", "pool", "parity", "SKILL.md"), "---\nname: parity\ndescription: Test fixture\n---\nTest skill.\n", 0600)
	mustRun("skill", "attach", "attachments", "parity", "--json")
	if _, err := os.Stat(filepath.Join(project, ".claude", "skills", "parity", "SKILL.md")); err != nil {
		t.Fatalf("remote skill side effect: %v", err)
	}

	// A server stub isolates transport semantics from send readiness and tool APIs.
	write(filepath.Join(shim, "server"), "#!/bin/sh\nprintf '%s\\n' \"$@\"\ncat\nprintf 'remote diagnostic' >&2\nexit 43\n", 0700)
	write(filepath.Join(controller, ".config", "agent-deck", "config.toml"), fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n", filepath.Join(shim, "server")), 0600)
	payload := strings.Repeat("line ' $value\n", 350)
	messageFile := filepath.Join(t.TempDir(), "message.txt")
	write(messageFile, payload, 0600)
	for _, source := range []string{messageFile, "-"} {
		for _, flag := range [][]string{{"--message-file", source}, {"--message-file=" + source}, {"-message-file", source}, {"-message-file=" + source}} {
			args := append([]string{"remote", "lab", "send", title}, flag...)
			out, stderr, code := run(controller, payload, args...)
			if code != 43 || stderr != "remote diagnostic" || !strings.HasSuffix(out, payload) || !strings.Contains(out, "--message-file\n-\n") {
				t.Errorf("message forwarding: code %d stderr %q output %q", code, stderr, out)
			}
		}
	}
	for _, args := range [][]string{
		{"launch", "--message", "--message-file=/must-not-read"},
		{"launch", "--title", "--message-file=/must-not-read"},
		{"launch", "--extra-arg", "--message-file=/must-not-read"},
		{"add", "--title", "--message-file=/must-not-read"},
		{"session", "send", title, "--message-file=", "literal"},
		{"session", "send", "--", title, "--message-file=/must-not-read"},
		{"launch", "--allow-repo-scripts"},
	} {
		out, stderr, code := run(controller, "", append([]string{"remote", "lab"}, args...)...)
		if code != 43 || stderr != "remote diagnostic" || out != strings.Join(args, "\n")+"\n" {
			t.Errorf("literal argv %v: %d %q %q", args, code, out, stderr)
		}
	}
	out, stderr, code := run(controller, "", "remote", "lab", "send", title, "--message-file", "/missing-superseded", "--message-file", messageFile)
	if code != 43 || stderr != "remote diagnostic" || !strings.HasSuffix(out, payload) {
		t.Errorf("last file wins: %d %q %q", code, out, stderr)
	}
	// A colliding configured name must never be interpreted as a local mutation.
	collisionConfig := fmt.Sprintf("[remotes.remove]\nhost = 'test-host'\nagent_deck_path = '%s'\n[remotes.list]\nhost = 'test-host'\n", filepath.Join(shim, "server"))
	configPath := filepath.Join(controller, ".config", "agent-deck", "config.toml")
	write(configPath, collisionConfig, 0600)
	if out, stderr, code := run(controller, "", "remote", "remove", "list"); code != 2 || !strings.Contains(out+stderr, "Ambiguous") {
		t.Errorf("collision: %d %q %q", code, out, stderr)
	}
	if data, err := os.ReadFile(configPath); err != nil || string(data) != collisionConfig {
		t.Fatalf("ambiguous execution mutated local config: %v", err)
	}
	if out, stderr, code := run(controller, "", "remote", "exec", "remove", "list"); code != 43 || out != "list\n" || stderr != "remote diagnostic" {
		t.Errorf("explicit exec: %d %q %q", code, out, stderr)
	}

}
