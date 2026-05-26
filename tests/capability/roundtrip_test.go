//go:build capability_e2e

package capability

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupEchobot installs the deterministic echo agent into the sandbox and
// registers it as a custom tool in config.toml, so `launch -c echobot` and
// `session start` resolve it. The script is copied out of testdata into the
// scratch HOME with the executable bit set, so the test never depends on the
// repo file's permissions.
//
// It returns nothing; the tool name is the literal "echobot".
func setupEchobot(t *testing.T, c *capSandbox) {
	t.Helper()

	src, err := filepath.Abs(filepath.Join("testdata", "echobot.sh"))
	if err != nil {
		t.Fatalf("resolve echobot.sh: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read echobot.sh: %v", err)
	}
	scriptPath := filepath.Join(c.Home, "echobot.sh")
	if err := os.WriteFile(scriptPath, data, 0o755); err != nil {
		t.Fatalf("write echobot.sh into sandbox: %v", err)
	}

	cfgDir := filepath.Join(c.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	// prompt_patterns makes PromptDetector treat "ECHOBOT READY" as the ready
	// prompt so the readiness gate opens. busy_patterns is set to a token that
	// never appears so the detector never reads the pane as busy.
	cfg := fmt.Sprintf(`[tools.echobot]
command = %q
icon = "E"
prompt_patterns = ["ECHOBOT READY"]
busy_patterns = ["WORKING"]
`, scriptPath)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// echoExchange distills a raw echo-agent pane down to the conversation that
// proves the round trip: only the lines that carry our unique token, which are
// the prompt line showing the message we sent and the agent's "ECHO:<token>"
// reply. Everything else in the raw pane is chrome (the shell MOTD, the
// "ECHOBOT READY >" banner, the braille "working" spinner frames) and would
// only obscure the proof. Keeping exactly the token lines makes the display
// snapshot deterministic and meaningful.
func echoExchange(pane, token string) string {
	var keep []string
	for _, ln := range strings.Split(pane, "\n") {
		if strings.Contains(ln, token) {
			keep = append(keep, strings.TrimRight(ln, " \t"))
		}
	}
	return strings.Join(keep, "\n")
}

// waitForPaneContains polls `session output --pane` until the substring shows
// up or the deadline passes, returning the final capture and whether it matched.
func (c *capSandbox) waitForPaneContains(t *testing.T, ref, want string, timeout time.Duration) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := c.try("session", "output", ref, "--pane")
		if err == nil {
			last = out
			if strings.Contains(out, want) {
				return last, true
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return last, false
}

// TestCapability_Agent_EchoRoundTrip is the backbone capability test: a message
// reaches a live agent and a reply comes back. It drives the deterministic
// echo agent through the exact production path a real claude send takes:
// waitForAgentReady polls capture-pane and runs PromptDetector against the
// custom prompt pattern, sendWithRetry issues the literal send-keys + Enter,
// and `session output --pane` reads the result back. Only the brain on the far
// end is made deterministic.
func TestCapability_Agent_EchoRoundTrip(t *testing.T) {
	c := newCapSandbox(t)
	setupEchobot(t, c)

	c.run(t, "add", "-c", "echobot", "-t", "cap-echo", c.WorkDir)
	c.run(t, "session", "start", "cap-echo")
	defer c.stopQuietly("cap-echo")

	if name := c.waitForTmuxSession(t, 8*time.Second); name == "" {
		t.Fatalf("echobot session never started.\nrows: %+v", c.list(t))
	}

	// --no-wait uses the send path that does not fire the Ctrl+C "full resend"
	// recovery (issue #479 / #876). That recovery is tuned for real agents,
	// which sit visibly "active" after receiving input; our deterministic
	// stand-in returns to its prompt too quickly for that heuristic, and the
	// Ctrl+C would kill it. The send still goes through the real send-keys +
	// Enter, the readiness preflight, and the delivery verifier (which confirms
	// the token reached the pane), so the round trip is exercised end to end.
	token := "PING-" + strings.ReplaceAll(t.Name(), "/", "-")
	c.run(t, "session", "send", "cap-echo", token, "--no-wait")

	want := "ECHO:" + token
	pane, ok := c.waitForPaneContains(t, "cap-echo", want, 20*time.Second)
	if !ok {
		t.Fatalf("pane never showed %q within timeout.\nThe send -> readiness -> capture round trip is broken.\nlast pane:\n%s", want, pane)
	}

	// Display proof, the backbone snapshot: the token we sent AND the agent's
	// echoed reply, distilled from the live pane so a reader can literally see
	// the conversation that the assertion above confirmed, without the shell
	// banner or spinner noise.
	snapshot(t, "send-output-echo", echoExchange(pane, token))
}

// TestCapability_Lifecycle_Launch proves the atomic add+start+send path: a
// single `launch -m` command creates the session, starts the pane, waits for
// readiness, and delivers the message, with the echoed token visible in the
// pane afterward.
func TestCapability_Lifecycle_Launch(t *testing.T) {
	c := newCapSandbox(t)
	setupEchobot(t, c)

	token := "PINGLAUNCH-cap-e2e-token"
	c.run(t, "launch", c.WorkDir, "-c", "echobot", "-t", "cap-launch", "-m", token)
	defer c.stopQuietly("cap-launch")

	row, ok := c.findByTitle(t, "cap-launch")
	if !ok {
		t.Fatalf("launch did not create a registry row.\nrows: %+v", c.list(t))
	}
	if row.Tool != "echobot" {
		t.Errorf("tool = %q, want echobot", row.Tool)
	}

	want := "ECHO:" + token
	pane, ok := c.waitForPaneContains(t, "cap-launch", want, 20*time.Second)
	if !ok {
		t.Fatalf("launch -m did not result in %q in the pane.\nlast pane:\n%s", want, pane)
	}

	// Display proof: the one-step launch distilled to the message we asked
	// launch -m to deliver and the agent's echoed reply, no shell banner.
	snapshot(t, "launch", echoExchange(pane, token))
}
