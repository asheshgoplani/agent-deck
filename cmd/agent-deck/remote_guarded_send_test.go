package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGuardRemoteSSH puts an `ssh` shim first on PATH. Its `session send
// --help` lists --require-input-prompt only when guarded is true; any other
// `session send` is logged and answered with a JSON success.
func fakeGuardRemoteSSH(t *testing.T, guarded bool) string {
	t.Helper()
	shim := t.TempDir()
	log := filepath.Join(shim, "calls.log")
	help := `printf 'Usage: agent-deck session send <id> <message> [options]\n  -json\n  -no-wait\n' >&2`
	if guarded {
		help = `printf 'Usage: agent-deck session send <id> <message> [options]\n  -json\n  -require-input-prompt\n' >&2`
	}
	script := `#!/bin/sh
for cmd; do :; done
printf '%s\n' "$cmd" >> ` + log + `
case "$cmd" in
  *"'session' 'send' '--help'"*)
    ` + help + `
    exit 0 ;;
  *"'session' 'send'"*)
    cat >/dev/null
    printf '{"success":true,"delivery":"submitted"}\n'
    exit 0 ;;
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

func remoteGuardHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.edge]\nhost = 'edge-host'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// An older remote that does not advertise the flag never receives the send.
func TestRemoteGuardedSendRefusedByOlderRemote(t *testing.T) {
	home := remoteGuardHome(t)
	callLog := fakeGuardRemoteSSH(t, false)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "edge", "session", "send", "task", "hello", "--json", "--require-input-prompt")
	if code != 1 || !strings.Contains(stderr, `--require-input-prompt unsupported on this remote "edge"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	calls, _ := os.ReadFile(callLog)
	if strings.Count(string(calls), "'session' 'send'") != 1 || !strings.Contains(string(calls), "'--help'") {
		t.Fatalf("an older remote must only see the help probe, got:\n%s", calls)
	}
}

// A remote that advertises the flag gets the send with the flag intact.
func TestRemoteGuardedSendForwardedToGuardedRemote(t *testing.T) {
	home := remoteGuardHome(t)
	callLog := fakeGuardRemoteSSH(t, true)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "edge", "session", "send", "task", "hello", "--json", "--require-input-prompt")
	if code != 0 || !strings.Contains(stdout, `"delivery":"submitted"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	calls, _ := os.ReadFile(callLog)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	last := lines[len(lines)-1]
	if len(lines) != 2 || !strings.Contains(last, "'--require-input-prompt'") || strings.Contains(last, "'--help'") {
		t.Fatalf("want the help probe then the guarded send, got:\n%s", calls)
	}
}

// Without the flag nothing changes: no help probe, one forwarded send.
func TestRemoteUnguardedSendSkipsProbe(t *testing.T) {
	home := remoteGuardHome(t)
	callLog := fakeGuardRemoteSSH(t, false)
	_, stderr, code := runAgentDeck(t, home, "remote", "edge", "session", "send", "task", "hello", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	calls, _ := os.ReadFile(callLog)
	if strings.Contains(string(calls), "'--help'") || strings.Count(string(calls), "'session' 'send'") != 1 {
		t.Fatalf("unguarded send must not probe, got:\n%s", calls)
	}
}
