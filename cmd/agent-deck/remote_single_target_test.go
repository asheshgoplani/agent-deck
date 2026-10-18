package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSingleTargetRemote registers remote "lab" behind an ssh shim that logs
// each remote command string and runs it with sh. The fake remote core
// answers `session show --json` the way agent-deck does (an object, or a
// NOT_FOUND error with exit 2); with oldShow it rejects `session show`
// like a build that lacks it, so the controller must fall back.
func fakeSingleTargetRemote(t *testing.T, home string, oldShow bool) (callLog string) {
	t.Helper()
	dir := t.TempDir()
	callLog = filepath.Join(dir, "calls.log")
	ssh := `#!/bin/sh
for cmd; do :; done
printf '%s' "$cmd" | tr '\n' ' ' >> '` + callLog + `'
printf '\n' >> '` + callLog + `'
exec sh -c "$cmd"
`
	show := `case "$4" in
    r1|"remote one") printf '{"id":"r1abcdef","title":"remote one","status":"idle"}\n' ;;
    *) printf '{"success":false,"error":"session not found: %s","code":"NOT_FOUND"}\n' "$4"; exit 2 ;;
  esac ;;`
	if oldShow {
		show = `echo "unknown session command: show" >&2; exit 1 ;;`
	}
	core := `#!/bin/sh
case "$1 $2" in
  "session show") ` + show + `
  "session send"|"session output") printf '{"success":true}\n' ;;
  "list --json"*) printf '[{"id":"r1abcdef","title":"remote one","tool":"claude","status":"idle"}]\n' ;;
  "rename r1abcdef") echo renamed ;;
  *) echo "unexpected remote command: $*" >&2; exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	corePath := filepath.Join(dir, "remote-agent-deck")
	if err := os.WriteFile(corePath, []byte(core), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.lab]\nhost = 'lab-host'\nagent_deck_path = '"+corePath+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return callLog
}

func callsListing(calls []string) []string {
	var listed []string
	for _, c := range calls {
		if strings.Contains(c, "'list'") {
			listed = append(listed, c)
		}
	}
	return listed
}

// A forwarded single-session command is one ssh round trip that runs only
// that command on the remote: no `list --json --stats` (or any list) first.
// Regression guard for the remote list pile-up (2026-10).
func TestRemoteSendOutputRunNoRemoteList(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	callLog := fakeSingleTargetRemote(t, home, false)
	for _, args := range [][]string{
		{"remote", "lab", "session", "send", "r1", "hello", "--json", "--no-wait"},
		{"remote", "lab", "session", "output", "r1", "--json"},
		{"remote", "exec", "lab", "session", "output", "r1", "--json"},
	} {
		before := len(sshCalls(t, callLog))
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("%q: exit %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		calls := sshCalls(t, callLog)[before:]
		if len(calls) != 1 {
			t.Fatalf("%q: want one ssh round trip, got %d: %q", args, len(calls), calls)
		}
		if listed := callsListing(calls); len(listed) != 0 {
			t.Fatalf("%q: ran a remote list: %q", args, listed)
		}
	}
}

// `remote rename` resolves its one target with the remote's own
// `session show --json`, not a full `list --json --stats`.
func TestRemoteRenameResolvesWithoutListing(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	callLog := fakeSingleTargetRemote(t, home, false)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "rename", "lab", "remote one", "new title")
	if code != 0 || !strings.Contains(stdout, "Renamed 'remote one'") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	calls := sshCalls(t, callLog)
	if listed := callsListing(calls); len(listed) != 0 {
		t.Fatalf("rename listed the whole remote: %q", listed)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "'session' 'show' '--json' 'remote one'") || !strings.Contains(calls[1], "'rename' 'r1abcdef' 'new title'") {
		t.Fatalf("want show then rename, got %q", calls)
	}
}

// A remote that answers "not found" is final: no list fallback.
func TestRemoteRenameNotFoundDoesNotList(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	callLog := fakeSingleTargetRemote(t, home, false)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "rename", "lab", "nope", "x")
	if code == 0 || !strings.Contains(stdout+stderr, "not found") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if listed := callsListing(sshCalls(t, callLog)); len(listed) != 0 {
		t.Fatalf("not-found answer still listed the remote: %q", listed)
	}
}

// A remote without a usable `session show` still resolves, through a plain
// `list --json` that never carries --stats.
func TestRemoteRenameFallsBackToPlainListOnOlderRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	callLog := fakeSingleTargetRemote(t, home, true)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "rename", "lab", "r1", "new title")
	if code != 0 || !strings.Contains(stdout, "Renamed 'remote one'") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	listed := callsListing(sshCalls(t, callLog))
	if len(listed) != 1 || strings.Contains(listed[0], "--stats") {
		t.Fatalf("want one plain list --json fallback, got %q", listed)
	}
}
