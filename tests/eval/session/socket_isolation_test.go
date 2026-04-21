//go:build eval_smoke

// Behavioral eval for tmux socket isolation (issue #687, v1.7.50). This
// test is the peer of TestEval_Session_InjectStatusLine_RealTmux: it drives
// the agent-deck binary end-to-end through `add` + `session start` under a
// configured `[tmux].socket_name`, then verifies against a REAL tmux server
// that the session actually landed on the isolated `-L <name>` socket and
// NOT on the user's default server.
//
// Why this eval exists: unit tests already prove `tmuxArgs()` produces
// `-L <name>` and that `Instance.TmuxSocketName` round-trips through SQLite.
// But a regression in the call-site plumbing — e.g. a future refactor that
// accidentally drops `tmuxCmd` for a raw `exec.Command("tmux", ...)` —
// would pass every unit test and still break socket isolation in
// production. This case reads what tmux itself reports to catch that.

package session_test

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_Session_SocketIsolation_RealTmux: the v1.7.50 feature guard.
//
// The sandbox deliberately does NOT install the tmux shim — the shim
// splices `-S <socket-path>` into every tmux call, which conflicts with
// the `-L <name>` selector agent-deck emits when socket_name is set. We
// want to exercise the real `-L` code path end-to-end, so this test uses
// a unique per-invocation socket name and cleans up with
// `tmux -L <name> kill-server` at the end.
func TestEval_Session_SocketIsolation_RealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	sb := harness.NewSandbox(t)

	// Random socket name per test run so parallel CI jobs / re-runs don't
	// collide on the same tmux server. Keep the prefix so an operator who
	// runs `tmux ls` during flake investigation can spot our leftovers.
	socketName := "ad-eval-" + randHex(t, 4)

	// Config opt-in: the single line v1.7.50 users add to get isolation.
	agentDeckDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	cfg := "[tmux]\nsocket_name = \"" + socketName + "\"\n"
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	// Register cleanup BEFORE any tmux activity so a failure mid-test still
	// reaps the isolated server. Target the same -L <name> we expect
	// agent-deck to use.
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socketName, "kill-server").Run()
	})

	workDir := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	// 1. Add a shell session (bash, not claude, so we don't need the real
	//    claude binary on PATH). Display name "iso" keeps substring checks
	//    tight.
	runBin(t, sb, "add", "-c", "bash", "-t", "iso", "-g", "sockeval", workDir)

	// 2. Start the session. If socket isolation is wired correctly, this
	//    creates a tmux session on `-L <socketName>`, NOT on the user's
	//    default tmux server.
	runBin(t, sb, "session", "start", "iso")

	// 3. Poll the ISOLATED socket for our session. Use -L (the feature's
	//    contract) so the query goes to the right server. Up to 5s for the
	//    startup handshake on slow CI.
	deadline := time.Now().Add(5 * time.Second)
	var foundName string
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").CombinedOutput()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.HasPrefix(line, "agentdeck_") {
					foundName = line
					break
				}
			}
		}
		if foundName != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if foundName == "" {
		out, _ := exec.Command("tmux", "-L", socketName, "list-sessions").CombinedOutput()
		t.Fatalf(
			"no agentdeck_* session on isolated socket -L %s after 5s.\n"+
				"tmux -L %s list-sessions: %s\n\n"+
				"Diagnosis: either the CLI never set Session.SocketName "+
				"from [tmux].socket_name, or the tmuxCmd factory regressed "+
				"and session start still targets the default server.",
			socketName, socketName, string(out))
	}

	// 4. Strongest check: the session we just created must NOT also appear
	//    on the user's default tmux server. If this fires, isolation is
	//    silently broken — agent-deck spawned on both sockets or escaped
	//    the -L and landed only on the default. Match by exact foundName
	//    to avoid flagging unrelated `agentdeck_*` sessions that the
	//    developer already has running on their default server (common
	//    when running the harness inside an agent-deck conductor).
	defaultOut, _ := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(defaultOut)), "\n") {
		if line == foundName {
			t.Fatalf(
				"isolation leak: session %q appeared on BOTH the isolated socket -L %s "+
					"AND the user's default tmux server. This defeats the entire "+
					"feature — `[tmux].socket_name` must keep agent-deck OFF the default.",
				foundName, socketName)
		}
	}

	// 5. Clean teardown via the CLI. Exercising `session stop` under an
	//    isolated socket proves the lifecycle teardown path threads the
	//    stored TmuxSocketName correctly — otherwise the stop would silently
	//    no-op (it would look for the session on the default server and
	//    not find it).
	runBin(t, sb, "session", "stop", "iso")

	// Verify stop actually reached the isolated server.
	time.Sleep(200 * time.Millisecond)
	after, _ := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(after)), "\n") {
		if line == foundName {
			t.Fatalf(
				"session %q survived `session stop iso` on isolated socket -L %s.\n"+
					"`session stop` did not target the stored Instance.TmuxSocketName.",
				foundName, socketName)
		}
	}
}

// randHex returns a short random hex string for per-test socket naming.
func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
