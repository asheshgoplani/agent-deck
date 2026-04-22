//go:build eval_smoke

// Behavioral eval for the socket-isolation-at-attach gap (#687 follow-up,
// v1.7.55). This is the peer of TestEval_Session_SocketIsolation_RealTmux:
// that case proves `session start` + `session stop` honour socket isolation,
// this case proves the full interactive lifecycle does — specifically the
// attach and restart paths, which @jcordasco's audit flagged as still using
// raw exec.Command("tmux", ...) in v1.7.50.
//
// Why this eval exists: unit tests now assert pty.go's command-builder
// helpers produce -L <socket>. But a regression in the call-site plumbing
// — e.g. a future refactor that drops s.attachCmd for a hand-assembled
// argv — would pass every Go test and still silently connect `session attach`
// to the default server. The fix shipped in v1.7.55 was exactly that kind
// of regression on top of v1.7.50. This eval catches it.

package session_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_Session_AttachRestart_SocketIsolation_RealTmux is the v1.7.55
// feature guard. It drives the agent-deck binary through the full
// user-observable lifecycle (add → start → attach → detach → restart →
// stop) under a configured `[tmux].socket_name` and verifies, against a
// real tmux server, that every step landed on the isolated -L socket and
// not on the user's default server.
//
// The sandbox deliberately does NOT install the tmux shim — the shim splices
// `-S <socket-path>` into tmux calls, which conflicts with the `-L <name>`
// selector we want to exercise. We use a unique per-invocation socket name
// and clean up with `tmux -L <name> kill-server`.
func TestEval_Session_AttachRestart_SocketIsolation_RealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	sb := harness.NewSandbox(t)

	socketName := "ad-attach-" + randHex(t, 4)

	agentDeckDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	cfg := "[tmux]\nsocket_name = \"" + socketName + "\"\n"
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socketName, "kill-server").Run()
	})

	workDir := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	// 1. Register + start a shell session.
	runBin(t, sb, "add", "-c", "bash", "-t", "att", "-g", "attacheval", workDir)
	runBin(t, sb, "session", "start", "att")

	// 2. Wait for the session to appear on the isolated socket. If socket
	//    isolation at start is broken, this times out immediately — that
	//    regression is already covered by TestEval_Session_SocketIsolation,
	//    but we need a live session before the attach step so fail loudly
	//    here if not.
	sessName := waitForAgentDeckSession(t, socketName, 5*time.Second)
	if sessName == "" {
		out, _ := exec.Command("tmux", "-L", socketName, "list-sessions").CombinedOutput()
		t.Fatalf("no agentdeck_* session on isolated socket -L %s after 5s.\n"+
			"tmux -L %s list-sessions: %s", socketName, socketName, string(out))
	}

	// 3. Interactive attach via PTY. If pty.go still used raw
	//    exec.Command("tmux", "attach-session", ...), this would connect to
	//    the user's default tmux server — which has no such session — and
	//    exit non-zero with "can't find session". The fix routes through
	//    Session.attachCmd → s.tmuxCmdContext so the -L selector threads
	//    through.
	//
	// Spawn uses Sandbox.Env() which sets TERM=dumb — fine for other evals
	// but tmux attach wants real terminal capabilities to register a client.
	// Use SpawnWithEnv if available, otherwise override TERM in place.
	p := sb.SpawnWithEnv([]string{"TERM=xterm-256color"}, "session", "attach", "att")
	defer p.Close()

	// 3a. Positive signal: a client actually attached to the ISOLATED
	//     socket. Poll list-clients on -L <socket> until non-empty.
	attached := waitForClientOnSocket(t, socketName, 8*time.Second)
	if !attached {
		out, _ := exec.Command("tmux", "-L", socketName, "list-clients").CombinedOutput()
		defaultOut, _ := exec.Command("tmux", "list-clients", "-t", sessName).CombinedOutput()
		t.Fatalf(
			"session attach did not produce a client on the isolated socket.\n"+
				"tmux -L %s list-clients: %q\n"+
				"tmux list-clients -t %s (default): %q\n"+
				"agent-deck PTY output:\n%s\n\n"+
				"Diagnosis: Session.Attach built its tmux argv by hand and landed on the default server — the #687 follow-up regression.",
			socketName, strings.TrimSpace(string(out)), sessName, strings.TrimSpace(string(defaultOut)), p.Output())
	}

	// 3b. Negative signal: no agent-deck test session exists on the user's
	//     default tmux server, so no client can be attached to it there.
	//     `tmux list-clients -t <session>` exits 1 with "can't find session"
	//     on the default server — which is exactly what we want. Failure
	//     mode is: the default server DOES have a same-named session and a
	//     client on it. That would mean both start and attach leaked.
	defaultClients, defErr := exec.Command("tmux", "list-clients", "-t", sessName).CombinedOutput()
	if defErr == nil && len(strings.TrimSpace(string(defaultClients))) > 0 {
		t.Fatalf("default server has a client attached to %q — isolation leaked on attach.\n"+
			"tmux list-clients -t %s: %s",
			sessName, sessName, string(defaultClients))
	}

	// 4. Detach by sending Ctrl+Q (the agent-deck default detach key). The
	//    attach command should return cleanly and the child process should
	//    exit 0.
	p.Send("\x11") // Ctrl+Q
	p.ExpectExit(0, 10*time.Second)

	// 5. Session must survive the detach — on the isolated socket.
	if !sessionStillExists(socketName, sessName) {
		t.Fatalf("session %q disappeared from isolated socket -L %s after detach",
			sessName, socketName)
	}

	// 6. Restart exercises the full teardown + recreate on the isolated
	//    socket. Pre-fix, restart's kill step ran on the default server
	//    (silent no-op) and then the recreate spawned a duplicate — visible
	//    as two agentdeck_* entries post-restart.
	runBin(t, sb, "session", "restart", "att")

	// Give restart a moment to finish its recreate.
	time.Sleep(500 * time.Millisecond)

	// 7. Count agentdeck_ sessions on the isolated socket — must be exactly
	//    one (restart must not have duplicated).
	if n := countAgentDeckSessions(t, socketName); n != 1 {
		listOut, _ := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").CombinedOutput()
		t.Fatalf("expected exactly 1 agentdeck_* session on -L %s after restart; got %d.\ntmux -L %s list-sessions: %s",
			socketName, n, socketName, string(listOut))
	}

	// 8. Final stop. Proves the teardown path still threads socket name
	//    even after restart regenerated the Instance.
	runBin(t, sb, "session", "stop", "att")

	time.Sleep(200 * time.Millisecond)
	if countAgentDeckSessions(t, socketName) != 0 {
		out, _ := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").CombinedOutput()
		t.Fatalf("session stop did not reach isolated socket -L %s.\ntmux -L %s list-sessions: %s",
			socketName, socketName, string(out))
	}
}

// waitForAgentDeckSession polls `tmux -L <socket> list-sessions` until a
// session starting with "agentdeck_" appears, or the timeout elapses.
func waitForAgentDeckSession(t *testing.T, socketName string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").CombinedOutput()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.HasPrefix(line, "agentdeck_") {
					return line
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

// waitForClientOnSocket polls `tmux -L <socket> list-clients` until at least
// one client row appears. Returns true on success, false on timeout.
func waitForClientOnSocket(t *testing.T, socketName string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-L", socketName, "list-clients").CombinedOutput()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// sessionStillExists returns true if `name` is listed on -L socketName.
func sessionStillExists(socketName, name string) bool {
	out, err := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").CombinedOutput()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == name {
			return true
		}
	}
	return false
}

// countAgentDeckSessions returns the number of agentdeck_* sessions on the
// isolated socket.
func countAgentDeckSessions(t *testing.T, socketName string) int {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").CombinedOutput()
	if err != nil {
		// no server → 0 sessions
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "agentdeck_") {
			n++
		}
	}
	return n
}
