//go:build eval_smoke

package session_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_EmbeddedPTY(t *testing.T) {
	sb := harness.NewSandbox(t)
	socketName := "ad-embedded-" + randHex(t, 4)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socketName, "kill-server").Run() })
	configDir := filepath.Join(sb.Home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	config := "theme = \"dark\"\n\n[tmux]\nsocket_name = \"" + socketName + "\"\n\n[ui]\nembedded_terminal = true\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write embedded terminal config: %v", err)
	}
	workDir := filepath.Join(sb.Home, "embedded-project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	runBin(t, sb, "add", "-c", "bash", "-t", "embedded-eval", workDir)
	runBin(t, sb, "session", "start", "embedded-eval")

	p := sb.Spawn("--select", "embedded-eval")
	defer p.Close()
	p.Resize(120, 30)
	// The explicit config skips the first-run wizard; dismiss the normal hooks
	// prompt before exercising the deck.
	p.ExpectOutput("Claude Code Hooks", 8*time.Second)
	p.Send("\x1b")
	p.ExpectOutput("embedded-eval", 8*time.Second)
	p.Send("\r")
	p.ExpectOutput("Ctrl+Q detach", 5*time.Second)

	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		out, err := tmuxTryEmbedded(socketName, "list-clients", "-F", "#{client_tty}")
		return err == nil && strings.TrimSpace(out) != ""
	}, "Enter never created a real tmux attach client")
	initialWidth, ok := embeddedTTYClientWidth(socketName)
	if !ok || initialWidth >= 118 {
		t.Fatalf("embedded client width before sidebar collapse = %d, want less than full 118", initialWidth)
	}
	p.Send("\x1b[98;7u") // Ctrl+Alt+B in Ghostty/Kitty CSI-u form
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		width, found := embeddedTTYClientWidth(socketName)
		return found && width == 118
	}, "Ctrl+Alt+B did not expand the embedded PTY across the hidden sidebar")
	p.Send("\x1b[98;7u")
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		width, found := embeddedTTYClientWidth(socketName)
		return found && width == initialWidth
	}, "second Ctrl+Alt+B did not restore the sidebar and prior PTY width")

	resultPath := filepath.Join(sb.Home, "embedded-enter-result")
	p.Send("printf tui-embedded-ok > " + shellQuoteEval(resultPath) + "\r")
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(resultPath)
		return err == nil && string(data) == "tui-embedded-ok"
	}, "raw Enter through the TUI did not execute in the session")

	escapePath := filepath.Join(sb.Home, "embedded-escape-result")
	escapeDonePath := filepath.Join(sb.Home, "embedded-escape-done")
	p.Send("stty -echo -icanon min 1; printf ESC_READY; dd bs=1 count=1 of=" +
		shellQuoteEval(escapePath) + " 2>/dev/null; stty sane; printf done > " +
		shellQuoteEval(escapeDonePath) + "\r")
	p.ExpectOutput("ESC_READY", 5*time.Second)
	p.Send("\x1b")
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(escapePath)
		return err == nil && string(data) == "\x1b"
	}, "raw Escape through the TUI did not reach the session")
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(escapeDonePath)
		return err == nil && string(data) == "done"
	}, "pane did not restore normal terminal input after the Escape probe")

	p.Send("printf '\\033[38;2;1;2;3mDETACHED-HIFI\\033[0m\\n'\r")
	p.ExpectOutput("DETACHED-HIFI", 5*time.Second)
	detachedOutputStart := len(p.Output())
	p.Send("\x11")
	detached := false
	waitDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(waitDeadline) {
		out, err := tmuxTryEmbedded(socketName, "list-clients", "-F", "#{client_tty}")
		if err != nil || strings.TrimSpace(out) == "" {
			detached = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !detached {
		clients, _ := tmuxTryEmbedded(socketName, "list-clients", "-F", "#{client_name}:#{client_tty}:#{client_session}")
		t.Fatalf("Ctrl+Q did not close the embedded tmux client; clients=%q\n%s", clients, p.Dump())
	}
	if _, err := tmuxTryEmbedded(socketName, "has-session"); err != nil {
		t.Fatal("Ctrl+Q closed the persistent tmux session instead of only its client")
	}
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		output := p.Output()
		if detachedOutputStart > len(output) {
			return false
		}
		redraw := output[detachedOutputStart:]
		return strings.Contains(redraw, "DETACHED-HIFI") && strings.Contains(redraw, "\x1b[38;2;1;2;3m")
	}, "detached dashboard did not redraw the terminal snapshot with its truecolor styling")
}

func TestEval_ClassicTUI(t *testing.T) {
	sb := harness.NewSandbox(t)
	socketName := "ad-classic-" + randHex(t, 4)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socketName, "kill-server").Run() })
	configDir := filepath.Join(sb.Home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	config := "theme = \"dark\"\n\n[tmux]\nsocket_name = \"" + socketName + "\"\n\n[ui]\nembedded_terminal = false\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write classic UI config: %v", err)
	}

	workDir := filepath.Join(sb.Home, "classic-project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	runBin(t, sb, "add", "-c", "bash", "-t", "classic-eval", workDir)
	runBin(t, sb, "session", "start", "classic-eval")

	p := sb.SpawnWithEnv([]string{"TERM=xterm-256color"}, "--select", "classic-eval")
	defer p.Close()
	p.Resize(120, 30)
	p.ExpectOutput("Claude Code Hooks", 8*time.Second)
	p.Send("\x1b")
	p.ExpectOutput("SESSIONS", 8*time.Second)
	p.ExpectOutput("classic-eval", 8*time.Second)
	p.Send("\r")

	classicAttached := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		width, found := embeddedTTYClientWidth(socketName)
		if found && width >= 118 {
			classicAttached = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !classicAttached {
		clients, _ := tmuxTryEmbedded(socketName, "list-clients", "-F", "#{client_tty}\t#{client_width}\t#{client_session}")
		t.Fatalf("classic Enter did not replace the dashboard with a full-width tmux client; clients=%q\n%s", clients, p.Dump())
	}

	resultPath := filepath.Join(sb.Home, "classic-enter-result")
	p.Send("printf tui-classic-ok > " + shellQuoteEval(resultPath) + "\r")
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(resultPath)
		return err == nil && string(data) == "tui-classic-ok"
	}, "classic full-screen attach did not accept terminal input")

	p.Send("\x11")
	waitForEmbeddedEval(t, 5*time.Second, func() bool {
		out, err := tmuxTryEmbedded(socketName, "list-clients", "-F", "#{client_tty}")
		return err != nil || strings.TrimSpace(out) == ""
	}, "Ctrl+Q did not detach the classic full-screen tmux client")
}

func waitForEmbeddedEval(t *testing.T, timeout time.Duration, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(failure)
}

func shellQuoteEval(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func tmuxTryEmbedded(socketName string, args ...string) (string, error) {
	full := append([]string{"-L", socketName}, args...)
	out, err := exec.Command("tmux", full...).CombinedOutput()
	return string(out), err
}

func embeddedTTYClientWidth(socketName string) (int, bool) {
	out, err := tmuxTryEmbedded(socketName, "list-clients", "-F", "#{client_tty}\t#{client_width}")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		width, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err == nil {
			return width, true
		}
	}
	return 0, false
}
