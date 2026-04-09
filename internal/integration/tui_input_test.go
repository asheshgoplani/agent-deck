package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestTUIInput_ArrowKeysNavigate launches the real agent-deck binary in a tmux
// session and verifies that arrow key escape sequences are interpreted (cursor
// moves) rather than displayed as raw text.
//
// This is a regression test for #539 where tea.WithInput(CSIuReader) stripped
// the *os.File interface from stdin, preventing Bubble Tea from setting raw
// terminal mode. The result was arrow keys appearing as "^[[A" text.
func TestTUIInput_ArrowKeysNavigate(t *testing.T) {
	skipIfNoTmuxServer(t)

	// Build the binary from current source so we test the working tree.
	binPath := t.TempDir() + "/agent-deck-test"
	build := exec.Command("go", "build", "-o", binPath, "./cmd/agent-deck/")
	build.Dir = findRepoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build agent-deck: %v\n%s", err, out)
	}

	sess := "inttest-tui-input-" + sanitizeName(t.Name())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
	})

	// Launch agent-deck in a tmux session with test profile (no real sessions).
	err := exec.Command("tmux", "new-session", "-d", "-s", sess, "-x", "120", "-y", "40",
		binPath, "-p", "_test_tui_input").Run()
	if err != nil {
		t.Fatalf("failed to create tmux session: %v", err)
	}

	// Wait for the TUI to render.
	waitForPane(t, sess, "Agent Deck", 5*time.Second)

	// Send arrow keys.
	_ = exec.Command("tmux", "send-keys", "-t", sess, "Down").Run()
	time.Sleep(300 * time.Millisecond)
	_ = exec.Command("tmux", "send-keys", "-t", sess, "Down").Run()
	time.Sleep(300 * time.Millisecond)
	_ = exec.Command("tmux", "send-keys", "-t", sess, "Up").Run()
	time.Sleep(300 * time.Millisecond)

	// Capture the pane and check for raw escape sequences.
	content := capturePane(t, sess)

	// Raw escape sequences look like ^[[A, ^[[B, ESC[A, \x1b[A etc.
	// If terminal is in raw mode (correct), these are consumed by Bubble Tea.
	// If terminal is in cooked mode (broken), they appear as visible text.
	rawPatterns := []string{"^[[A", "^[[B", "^[[C", "^[[D", "\x1b[A", "\x1b[B"}
	for _, pat := range rawPatterns {
		if strings.Contains(content, pat) {
			t.Errorf("raw escape sequence %q found in pane output — arrow keys are not being interpreted.\n"+
				"This usually means Bubble Tea cannot set raw terminal mode on stdin.\n"+
				"Pane content:\n%s", pat, content)
		}
	}
}

// TestTUIInput_JKNavigation verifies that j/k keys work for navigation
// (not displayed as literal characters in unexpected places).
func TestTUIInput_JKNavigation(t *testing.T) {
	skipIfNoTmuxServer(t)

	binPath := t.TempDir() + "/agent-deck-test"
	build := exec.Command("go", "build", "-o", binPath, "./cmd/agent-deck/")
	build.Dir = findRepoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build agent-deck: %v\n%s", err, out)
	}

	sess := "inttest-tui-jk-" + sanitizeName(t.Name())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
	})

	err := exec.Command("tmux", "new-session", "-d", "-s", sess, "-x", "120", "-y", "40",
		binPath, "-p", "_test_tui_jk").Run()
	if err != nil {
		t.Fatalf("failed to create tmux session: %v", err)
	}

	waitForPane(t, sess, "Agent Deck", 5*time.Second)

	// Send j and k (vim-style navigation).
	_ = exec.Command("tmux", "send-keys", "-t", sess, "j").Run()
	time.Sleep(300 * time.Millisecond)
	_ = exec.Command("tmux", "send-keys", "-t", sess, "k").Run()
	time.Sleep(300 * time.Millisecond)

	// In a working TUI, j/k are consumed as navigation commands.
	// If the TUI isn't in raw mode, they'd appear as typed text.
	// Check the status bar area (bottom lines) for stray characters.
	content := capturePane(t, sess)
	lines := strings.Split(content, "\n")

	// Check last 3 lines for stray j/k characters that indicate
	// keys are being echoed instead of interpreted.
	for i := len(lines) - 3; i < len(lines) && i >= 0; i++ {
		line := strings.TrimSpace(lines[i])
		// A line that is just "j", "k", "jk", etc. means keys are echoed.
		if line == "j" || line == "k" || line == "jk" || line == "kj" {
			t.Errorf("stray key characters %q found on bottom line %d — keys are being echoed, not interpreted.\n"+
				"Pane content:\n%s", line, i, content)
		}
	}
}

// waitForPane polls tmux pane content until it contains the expected string.
func waitForPane(t *testing.T, sess, contains string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			content := capturePane(t, sess)
			t.Fatalf("timed out waiting for pane to contain %q.\nPane content:\n%s", contains, content)
		case <-ticker.C:
			content := capturePane(t, sess)
			if strings.Contains(content, contains) {
				return
			}
		}
	}
}

// capturePane returns the current tmux pane content.
func capturePane(t *testing.T, sess string) string {
	t.Helper()
	out, err := exec.Command("tmux", "capture-pane", "-t", sess, "-p", "-S", "-50").Output()
	if err != nil {
		t.Fatalf("failed to capture pane: %v", err)
	}
	return string(out)
}

// findRepoRoot walks up from the working directory to find the go.mod file.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}
