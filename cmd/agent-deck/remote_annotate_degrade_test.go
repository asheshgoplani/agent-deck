package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoteAnnotateUnsupported locks the detection: only a non-zero
// `session annotate` reply carrying the older remote's "unknown session
// command: annotate" line triggers the graceful message.
func TestRemoteAnnotateUnsupported(t *testing.T) {
	old := "Error: unknown session command: annotate\nUsage: agent-deck session <command> [options]\n..."
	if !remoteAnnotateUnsupported([]string{"session", "annotate", "abc", "--outcome", "worked"}, 1, old) {
		t.Fatal("older remote reply must be detected")
	}
	if remoteAnnotateUnsupported([]string{"session", "annotate", "abc"}, 1, "Error: session not found") {
		t.Fatal("other remote errors must pass through")
	}
	if remoteAnnotateUnsupported([]string{"session", "show", "abc"}, 1, old) {
		t.Fatal("only session annotate must trigger the graceful message")
	}
	if remoteAnnotateUnsupported([]string{"session", "annotate", "abc"}, 0, old) {
		t.Fatal("exit code 0 must never trigger the graceful message")
	}
	if !isSessionAnnotateArgs([]string{"session", "annotate", "abc"}) || isSessionAnnotateArgs([]string{"annotate"}) || isSessionAnnotateArgs([]string{"session", "metrics"}) {
		t.Fatal("isSessionAnnotateArgs must match exactly session annotate")
	}
}

func TestRemoteAnnotateUnsupportedMessageAndJSON(t *testing.T) {
	msg := remoteAnnotateUnsupportedMessage("lab", "1.16.10")
	if strings.Count(msg, "\n") != 0 || !strings.Contains(msg, `remote "lab" runs v1.16.10 without session annotate`) || !strings.Contains(msg, "agent-deck remote update lab") {
		t.Fatalf("message = %q", msg)
	}
	if msg := remoteAnnotateUnsupportedMessage("lab", ""); !strings.Contains(msg, "unknown agent-deck version") {
		t.Fatalf("unknown version must read as unknown: %q", msg)
	}
	var got struct {
		Error         string `json:"error"`
		Remote        string `json:"remote"`
		RemoteVersion string `json:"remote_version"`
	}
	if err := json.Unmarshal(remoteAnnotateUnsupportedJSON("lab", "1.16.10"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Remote != "lab" || got.RemoteVersion != "1.16.10" || got.Error != msg {
		t.Fatalf("json = %+v", got)
	}
	if err := json.Unmarshal(remoteAnnotateUnsupportedJSON("lab", ""), &got); err != nil || got.RemoteVersion != remoteVersionUnknown || !strings.Contains(got.Error, "unknown agent-deck version") {
		t.Fatalf("unknown version json = %+v (%v)", got, err)
	}
}

// fakeOldRemoteSSH puts an `ssh` shim first on PATH that answers like an
// older remote: `agent-deck version` reports 1.16.10 and `session annotate`
// is an unknown session command (usage on stdout, error on stderr, exit 1).
func fakeOldRemoteSSH(t *testing.T) string {
	t.Helper()
	shim := t.TempDir()
	log := filepath.Join(shim, "calls.log")
	script := `#!/bin/sh
for cmd; do :; done
printf '%s\n' "$cmd" >> ` + log + `
case "$cmd" in
  *" version")
    printf 'Agent Deck v1.16.10\n'
    exit 0 ;;
  *"'session' 'annotate'"*)
    printf 'Usage: agent-deck session <command> [options]\n\nCommands:\n  show <id>   Show session\n'
    printf 'Error: unknown session command: annotate\n' >&2
    exit 1 ;;
esac
printf 'unexpected remote command: %s\n' "$cmd" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// TestRemoteSessionAnnotate_OlderRemoteDegrades drives the built binary
// against a fake older remote: the caller gets one clear line naming the
// remote's version and the update command, exit 1, and under --json the
// {error, remote, remote_version} object instead of the remote's usage text.
func TestRemoteSessionAnnotate_OlderRemoteDegrades(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.lab]\nhost = 'old-host'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	callLog := fakeOldRemoteSSH(t)

	stdout, stderr, code := runAgentDeck(t, home, "remote", "lab", "session", "annotate", "task", "--outcome", "worked")
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout must stay empty (no remote usage text): %q", stdout)
	}
	want := `remote "lab" runs v1.16.10 without session annotate; update it with 'agent-deck remote update lab'`
	if strings.TrimSpace(stderr) != "Error: "+want {
		t.Fatalf("stderr = %q, want one line %q", stderr, "Error: "+want)
	}

	stdout, stderr, code = runAgentDeck(t, home, "remote", "lab", "session", "annotate", "task", "--outcome", "worked", "--json")
	if code != 1 {
		t.Fatalf("--json exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "unknown session command") || strings.Contains(stdout, "Usage:") {
		t.Fatalf("raw remote output leaked\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	var got struct {
		Error         string `json:"error"`
		Remote        string `json:"remote"`
		RemoteVersion string `json:"remote_version"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("--json stdout is not JSON: %v\n%s", err, stdout)
	}
	if got.Error != want || got.Remote != "lab" || got.RemoteVersion != "1.16.10" {
		t.Fatalf("--json = %+v", got)
	}

	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "annotate") || !strings.Contains(string(calls), " version") {
		t.Fatalf("expected the annotate call and one version probe, got:\n%s", calls)
	}
}
