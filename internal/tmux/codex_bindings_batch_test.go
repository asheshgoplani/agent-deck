package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAgentDeckCodexSessionIDsOnSocket(t *testing.T) {
	const socket = "codex-bindings-batch-test"
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	run("new-session", "-d", "-s", "agentdeck_first")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	run("new-session", "-d", "-s", "agentdeck_second")
	run("new-session", "-d", "-s", "unmanaged")
	run("set-environment", "-t", "agentdeck_first", "CODEX_SESSION_ID", "first-id")
	run("set-environment", "-t", "unmanaged", "CODEX_SESSION_ID", "ignored-id")

	ids, err := ListAgentDeckCodexSessionIDsOnSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids["agentdeck_first"] != "first-id" {
		t.Fatalf("batch bindings = %v", ids)
	}
}

// The server's global environment is copied from the client that started it.
// A format resolves an unbound session's CODEX_SESSION_ID from that global
// environment (verified on tmux 3.3a, 3.6a and 3.7b); only session-level
// values are bindings.
func TestListAgentDeckCodexSessionIDsOnSocketIgnoresGlobalEnvironment(t *testing.T) {
	const socket = "codex-bindings-global-test"
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("tmux", append([]string{"-L", socket}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	leaked := append(os.Environ(), "CODEX_SESSION_ID=global-leak")
	// First client starts the server: its environment becomes the global one.
	run(leaked, "new-session", "-d", "-s", "agentdeck_unbound")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	run(os.Environ(), "new-session", "-d", "-s", "agentdeck_bound")
	run(os.Environ(), "new-session", "-d", "-s", "agentdeck_same")
	run(os.Environ(), "set-environment", "-t", "agentdeck_bound", "CODEX_SESSION_ID", "bound-id")
	run(os.Environ(), "set-environment", "-t", "agentdeck_same", "CODEX_SESSION_ID", "global-leak")

	global, err := globalEnvironmentValue(socket, "CODEX_SESSION_ID")
	if err != nil || global != "global-leak" {
		t.Fatalf("global environment = %q, %v; the fixture did not leak the variable", global, err)
	}
	ids, err := ListAgentDeckCodexSessionIDsOnSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := ids["agentdeck_unbound"]; leaked {
		t.Fatalf("unbound session took the global value: %v", ids)
	}
	if ids["agentdeck_bound"] != "bound-id" || ids["agentdeck_same"] != "global-leak" || len(ids) != 2 {
		t.Fatalf("batch bindings = %v", ids)
	}
}

// Same contract against a scripted tmux, so the resolution path is exercised
// even where the installed tmux would not leak: the shim answers list-sessions
// with the global value for every unbound row (the 3.6a/3.7b format
// behaviour), and only the per-session read tells them apart.
func TestListAgentDeckCodexSessionIDsOnSocketShimGlobalFallback(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + log + `"
if [ "$1" = -u ]; then shift; fi
if [ "$1" = -L ]; then shift 2; fi
case "$1" in
list-sessions)
 printf 'agentdeck_unbound\tglobal-leak\n'
 printf 'agentdeck_bound\tbound-id\n'
 printf 'agentdeck_same\tglobal-leak\n'
 printf 'other\tglobal-leak\n' ;;
show-environment)
 if [ "$2" = -g ]; then printf 'CODEX_SESSION_ID=global-leak\n'; exit 0; fi
 # A key-less per-session read lists that session's environment, exit 0.
 case "$3" in
 agentdeck_bound) printf 'CODEX_SESSION_ID=bound-id\n' ;;
 agentdeck_same) printf 'CODEX_SESSION_ID=global-leak\n' ;;
 *) exit 0 ;;
 esac ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ids, err := ListAgentDeckCodexSessionIDsOnSocket("shim")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids["agentdeck_bound"] != "bound-id" || ids["agentdeck_same"] != "global-leak" {
		t.Fatalf("batch bindings = %v", ids)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	// One list, one global read, one per-session read per ambiguous managed
	// row (2): the unmanaged "other" row is never resolved.
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 4 {
		t.Fatalf("tmux calls = %d, want 4:\n%s", got, data)
	}
}
