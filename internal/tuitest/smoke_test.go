package tuitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// skipIfNoTmuxServer skips the test if tmux binary is missing or server isn't running.
func skipIfNoTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if err := exec.Command("tmux", "list-sessions").Run(); err != nil {
		t.Skip("tmux server not running")
	}
}

// buildBinary builds the agent-deck binary into a temp directory with GOTOOLCHAIN=go1.24.0.
// Returns the path to the built binary.
func buildBinary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "agent-deck")

	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/agent-deck")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=go1.24.0")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binPath
}

// repoRoot returns the project root directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from this test file to find go.mod
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

// tmuxCapture captures the current pane content from a tmux session.
func tmuxCapture(t *testing.T, sessionName string) string {
	t.Helper()
	out, err := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p").Output()
	if err != nil {
		t.Fatalf("tmux capture-pane: %v", err)
	}
	return string(out)
}

// tmuxCaptureWithANSI captures pane content with ANSI escape codes (for freeze screenshots).
func tmuxCaptureWithANSI(t *testing.T, sessionName string) string {
	t.Helper()
	out, err := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-e").Output()
	if err != nil {
		t.Fatalf("tmux capture-pane -e: %v", err)
	}
	return string(out)
}

// captureScreenshot saves a freeze PNG screenshot of the tmux session.
// Returns the path to the PNG file.
func captureScreenshot(t *testing.T, sessionName, name, outputDir string) string {
	t.Helper()

	pngPath := filepath.Join(outputDir, name+".png")
	ansi := tmuxCaptureWithANSI(t, sessionName)

	cmd := exec.Command("freeze", "--language", "ansi", "--output", pngPath)
	cmd.Stdin = strings.NewReader(ansi)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("freeze failed (non-fatal): %v\n%s", err, out)
		return ""
	}
	t.Logf("screenshot captured: %s", pngPath)
	return pngPath
}

// sendKey sends a key to a tmux session.
func sendKey(t *testing.T, sessionName string, key string) {
	t.Helper()
	if err := exec.Command("tmux", "send-keys", "-t", sessionName, key).Run(); err != nil {
		t.Fatalf("tmux send-keys %q: %v", key, err)
	}
}

// dismissUpdatePrompt checks if the TUI shows an update prompt and dismisses it with 'n'.
// The _test profile may have auto_update enabled, causing "Update now? [Y/n]:" to block.
func dismissUpdatePrompt(t *testing.T, sessionName string) {
	t.Helper()
	time.Sleep(2 * time.Second)
	content := tmuxCapture(t, sessionName)
	if strings.Contains(content, "Update now?") || strings.Contains(content, "Update available") {
		t.Log("update prompt detected, dismissing with 'n'")
		sendKey(t, sessionName, "n")
		sendKey(t, sessionName, "Enter")
		time.Sleep(500 * time.Millisecond)
	}
}

// waitForContent polls tmux capture until the content contains the expected substring
// (case-insensitive) or timeout.
func waitForContent(t *testing.T, sessionName, substr string, timeout time.Duration) string {
	t.Helper()
	lowerSubstr := strings.ToLower(substr)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content := tmuxCapture(t, sessionName)
		if strings.Contains(strings.ToLower(content), lowerSubstr) {
			return content
		}
		time.Sleep(200 * time.Millisecond)
	}
	content := tmuxCapture(t, sessionName)
	t.Fatalf("timed out waiting for %q in tmux output (got: %s)", substr, truncate(content, 500))
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// killSession kills a tmux session, ignoring errors if already dead.
func killSession(t *testing.T, sessionName string) {
	t.Helper()
	_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
}

// TestSmoke_TUIRenders builds the binary, launches it in tmux, and verifies the TUI renders.
func TestSmoke_TUIRenders(t *testing.T) {
	skipIfNoTmuxServer(t)

	binary := buildBinary(t)
	session := "tuitest_render_" + t.Name()
	defer killSession(t, session)

	// Launch agent-deck in a tmux session with test profile
	cmd := exec.Command("tmux", "new-session", "-d", "-s", session, "-x", "120", "-y", "40", binary)
	cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE=_test")
	if err := cmd.Run(); err != nil {
		t.Fatalf("tmux new-session: %v", err)
	}

	// Dismiss update prompt if it appears (auto_update may be enabled in _test profile)
	dismissUpdatePrompt(t, session)

	// Wait for TUI to render: look for "SESSIONS" in the output
	content := waitForContent(t, session, "sessions", 10*time.Second)

	// Verify key TUI elements are present (case-insensitive)
	if !strings.Contains(strings.ToLower(content), "conductor") {
		t.Error("expected 'conductor' group in TUI output")
	}

	// Capture screenshot if freeze is available
	if _, err := exec.LookPath("freeze"); err == nil {
		screenshotDir := filepath.Join(t.TempDir(), "screenshots")
		os.MkdirAll(screenshotDir, 0755)
		captureScreenshot(t, session, "01_main_screen", screenshotDir)
	}
}

// TestSmoke_NewSessionDialog verifies the new session dialog opens when 'n' is pressed.
func TestSmoke_NewSessionDialog(t *testing.T) {
	skipIfNoTmuxServer(t)

	binary := buildBinary(t)
	session := "tuitest_dialog_" + t.Name()
	defer killSession(t, session)

	cmd := exec.Command("tmux", "new-session", "-d", "-s", session, "-x", "120", "-y", "40", binary)
	cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE=_test")
	if err := cmd.Run(); err != nil {
		t.Fatalf("tmux new-session: %v", err)
	}

	dismissUpdatePrompt(t, session)

	// Wait for main TUI to render
	waitForContent(t, session, "sessions", 10*time.Second)

	// Press 'n' to open new session dialog
	sendKey(t, session, "n")

	// Wait for dialog to appear: should contain "Name" field label
	content := waitForContent(t, session, "name", 5*time.Second)

	// Verify dialog elements (case-insensitive)
	lower := strings.ToLower(content)
	hasSessionDialog := strings.Contains(lower, "new session") ||
		strings.Contains(lower, "name") ||
		strings.Contains(lower, "path")
	if !hasSessionDialog {
		t.Error("new session dialog did not appear after pressing 'n'")
	}

	// Capture screenshot if freeze is available
	if _, err := exec.LookPath("freeze"); err == nil {
		screenshotDir := filepath.Join(t.TempDir(), "screenshots")
		os.MkdirAll(screenshotDir, 0755)
		captureScreenshot(t, session, "02_new_session_dialog", screenshotDir)
	}
}

// TestSmoke_QuitExitsCleanly verifies pressing 'q' exits the TUI.
func TestSmoke_QuitExitsCleanly(t *testing.T) {
	skipIfNoTmuxServer(t)

	binary := buildBinary(t)
	session := "tuitest_quit_" + t.Name()
	defer killSession(t, session)

	cmd := exec.Command("tmux", "new-session", "-d", "-s", session, "-x", "120", "-y", "40", binary)
	cmd.Env = append(os.Environ(), "AGENTDECK_PROFILE=_test")
	if err := cmd.Run(); err != nil {
		t.Fatalf("tmux new-session: %v", err)
	}

	dismissUpdatePrompt(t, session)

	// Wait for TUI to render
	waitForContent(t, session, "sessions", 10*time.Second)

	// Press 'q' to quit. If MCP pool is running, a dialog appears with options:
	//   k = Keep running (quit TUI, keep pool)
	//   s = Shut down (quit TUI, stop pool)
	// Send 'k' to dismiss and quit without stopping the pool.
	sendKey(t, session, "q")
	time.Sleep(500 * time.Millisecond)
	sendKey(t, session, "k")

	// Wait for tmux session to disappear (TUI exited)
	deadline := time.Now().Add(5 * time.Second)
	exited := false
	for time.Now().Before(deadline) {
		err := exec.Command("tmux", "has-session", "-t", session).Run()
		if err != nil {
			exited = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !exited {
		t.Error("TUI did not exit after pressing 'q' then 'k' (tmux session still exists)")
	}
}

// TestSmoke_BuildVersion verifies the built binary reports the correct Go toolchain version.
func TestSmoke_BuildVersion(t *testing.T) {
	binary := buildBinary(t)

	out, err := exec.Command("go", "version", "-m", binary).Output()
	if err != nil {
		t.Fatalf("go version -m: %v", err)
	}

	versionInfo := string(out)

	// Verify built with go1.24.x (the pinned toolchain)
	if !strings.Contains(versionInfo, "go1.24") {
		t.Errorf("binary not built with go1.24.x toolchain:\n%s", versionInfo)
	}

	// Warn (don't fail) if vcs.modified=true since tests run in a working tree
	if strings.Contains(versionInfo, "vcs.modified=true") {
		t.Log("WARNING: binary built from dirty worktree (vcs.modified=true). Release builds must have vcs.modified=false.")
	}
}
