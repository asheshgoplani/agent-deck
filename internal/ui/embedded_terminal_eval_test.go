//go:build eval_smoke

package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deckterminal "github.com/asheshgoplani/agent-deck/internal/terminal"
	"github.com/charmbracelet/x/ansi"
)

func TestEval_EmbeddedTerminalIsARealTmuxClient(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("agent-deck-embedded-eval-%d", os.Getpid())
	sessionName := "full-fidelity"
	tmux := func(args ...string) (string, error) {
		cmdArgs := append([]string{"-L", socket}, args...)
		out, err := exec.Command("tmux", cmdArgs...).CombinedOutput()
		return string(out), err
	}
	_, _ = tmux("kill-server")
	t.Cleanup(func() { _, _ = tmux("kill-server") })
	if out, err := tmux("new-session", "-d", "-x", "40", "-y", "8", "-s", sessionName, "sh"); err != nil {
		t.Fatalf("create isolated tmux session: %v: %s", err, out)
	}
	if out, err := tmux("new-window", "-d", "-t", sessionName+":1", "-n", "selected", "sh"); err != nil {
		t.Fatalf("create selected tmux window: %v: %s", err, out)
	}
	_, _ = tmux("set-option", "-g", "status", "off")

	terminal, err := startEmbeddedTerminal(context.Background(), deckterminal.AttachRequest{
		Name:       sessionName + ":1",
		SocketName: socket,
	}, embeddedTerminalSize{Cols: 40, Rows: 8})
	if err != nil {
		t.Fatalf("start embedded attach: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	eventually(t, 3*time.Second, func() bool {
		out, err := tmux("list-clients", "-F", "#{client_name}:#{client_width}x#{client_height}")
		return err == nil && strings.Contains(out, "40x8")
	}, "embedded PTY never appeared as a 40x8 tmux client")

	beforeSpace := terminal.Cursor()
	if _, err := terminal.Write([]byte(" ")); err != nil {
		t.Fatalf("write raw Space: %v", err)
	}
	eventually(t, 3*time.Second, func() bool {
		afterSpace := terminal.Cursor()
		return afterSpace.X != beforeSpace.X || afterSpace.Y != beforeSpace.Y
	}, "Space reached tmux but did not advance the emulated terminal cursor")
	_, _ = terminal.Write([]byte{0x7f}) // remove the test space from the shell input

	resultPath := filepath.Join(t.TempDir(), "enter-result")
	command := "printf embedded-enter-ok > " + shellSingleQuote(resultPath) + "\r"
	if _, err := terminal.Write([]byte(command)); err != nil {
		t.Fatalf("write raw command and Enter: %v", err)
	}
	eventually(t, 3*time.Second, func() bool {
		data, err := os.ReadFile(resultPath)
		return err == nil && string(data) == "embedded-enter-ok"
	}, "raw Enter did not execute the command in the tmux pane")

	// tmux and full-screen terminal apps commonly bracket each repaint with
	// hide/show cursor controls. Cursor() must expose only committed emulator
	// frames, never the temporary hidden state in the middle of a busy repaint.
	busyCommand := "i=0; while [ \"$i\" -lt 80 ]; do printf '\\033[?25l\\r%04d\\033[?25h' \"$i\"; i=$((i+1)); sleep 0.005; done; printf '\\r\\nCURSOR-STABLE-DONE\\r\\n'\r"
	if _, err := terminal.Write([]byte(busyCommand)); err != nil {
		t.Fatalf("write busy cursor command: %v", err)
	}
	hiddenSnapshots := 0
	snapshots := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(ansi.Strip(terminal.Render()), "CURSOR-STABLE-DONE") {
		alive, waitErr := terminal.Wait()
		if !alive || waitErr != nil {
			t.Fatalf("embedded terminal ended during busy cursor test: alive=%v err=%v", alive, waitErr)
		}
		snapshots++
		if !terminal.Cursor().Visible {
			hiddenSnapshots++
		}
	}
	if !strings.Contains(ansi.Strip(terminal.Render()), "CURSOR-STABLE-DONE") {
		t.Fatal("busy cursor command did not complete")
	}
	if snapshots < 2 {
		t.Fatalf("busy cursor test sampled only %d frames", snapshots)
	}
	if hiddenSnapshots != 0 {
		t.Fatalf("busy tmux repaint exposed %d transient hidden-cursor snapshots across %d frames", hiddenSnapshots, snapshots)
	}

	if _, err := terminal.Write([]byte("printf '\\033[?1049h\\033[2J\\033[HALT-SCREEN'\r")); err != nil {
		t.Fatalf("enter alternate screen: %v", err)
	}
	eventually(t, 3*time.Second, func() bool {
		return strings.Contains(ansi.Strip(terminal.Render()), "ALT-SCREEN")
	}, "tmux alternate-screen output did not survive the PTY and Charm VT pipeline")

	if err := terminal.Resize(embeddedTerminalSize{Cols: 52, Rows: 9}); err != nil {
		t.Fatalf("resize embedded terminal: %v", err)
	}
	eventually(t, 3*time.Second, func() bool {
		out, err := tmux("list-clients", "-F", "#{client_width}x#{client_height}")
		return err == nil && strings.Contains(out, "52x9")
	}, "tmux client did not track embedded pane resize")

	if err := terminal.Close(); err != nil {
		t.Fatalf("close embedded client: %v", err)
	}
	if terminal.cmd.ProcessState == nil {
		t.Fatal("closing the embedded client did not reap its attach process")
	}
	select {
	case <-terminal.replyDone:
	default:
		t.Fatal("closing the embedded client left its terminal reply pump running")
	}
	eventually(t, 3*time.Second, func() bool {
		_, err := tmux("has-session", "-t", sessionName)
		return err == nil
	}, "closing the embedded client killed the persistent tmux session")
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool, failure string) {
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

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
