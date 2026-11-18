package tuitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requireTmux skips only where there is no tmux binary to run. It must not
// ask for a running server: TestMain gives this package a private tmux socket,
// so no server exists until the first test starts one, and a "server not
// running" skip turned every real-binary test here into a permanent skip.
// In CI and in the test container a missing tmux is a failure, not a skip.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") != "" || os.Getenv("AGENTDECK_TEST_CONTAINER") == "1" {
			t.Fatalf("tmux is required for the real-binary TUI tests: %v", err)
		}
		t.Skip("tmux not available")
	}
}

// deckEnv is the environment a real agent-deck runs with in these tests: a
// HOME of its own with a config file (so the deck opens on the session list,
// not the setup wizard), no update check, no usage-data question, and the
// opt-in that lets the deck start inside the test's own tmux pane.
type deckEnv struct {
	home string
	vars []string
}

func newDeckEnv(t *testing.T) *deckEnv {
	t.Helper()
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[updates]\ncheck_enabled = false\n"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return &deckEnv{home: home, vars: []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"AGENTDECK_PROFILE=_test",
		"AGENTDECK_SKIP_UPDATE_CHECK=1",
		"DO_NOT_TRACK=1",
		"AGENT_DECK_ALLOW_OUTER_TMUX=1",
		"TERM=xterm-256color",
	}}
}

// cli runs an agent-deck CLI command against this deck's state.
func (e *deckEnv) cli(t *testing.T, binary string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), e.vars...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-deck %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// seed adds two shell sessions in a group "work". Shell sessions are never
// started here and raise no agent-specific start-up question.
func (e *deckEnv) seed(t *testing.T, binary string) {
	t.Helper()
	for _, title := range []string{"smoke-one", "smoke-two"} {
		dir := filepath.Join(e.home, "proj", title)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir project dir: %v", err)
		}
		e.cli(t, binary, "add", dir, "-t", title, "-g", "work", "-c", "shell")
	}
}

// startDeck opens the real TUI in a detached 120x40 tmux session. The
// variables are passed on the command line: a tmux server keeps the
// environment of the client that started it, so a later test's HOME would
// otherwise not reach its pane.
func (e *deckEnv) startDeck(t *testing.T, sessionName, binary string) {
	t.Helper()
	args := []string{"new-session", "-d", "-s", sessionName, "-x", "120", "-y", "40", "env"}
	args = append(args, e.vars...)
	args = append(args, binary)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	t.Cleanup(func() { killSession(t, sessionName) })
}

// waitForSessionList waits until the deck shows the seeded session list.
func waitForSessionList(t *testing.T, sessionName string) string {
	t.Helper()
	waitForContent(t, sessionName, "smoke-one", 20*time.Second)
	return waitForContent(t, sessionName, "smoke-two", 5*time.Second)
}

// pinnedToolchain returns the toolchain pinned by go.mod's go directive
// (for example "go1.26.11") and its release line (for example "go1.26"), so the
// smoke tests follow the pin instead of repeating it.
func pinnedToolchain(t *testing.T) (exact, line string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		fields := strings.Fields(l)
		if len(fields) == 2 && fields[0] == "go" {
			exact = "go" + fields[1]
			parts := strings.SplitN(fields[1], ".", 3)
			if len(parts) < 2 {
				t.Fatalf("unexpected go directive %q in go.mod", l)
			}
			return exact, "go" + parts[0] + "." + parts[1]
		}
	}
	t.Fatal("go.mod has no go directive")
	return "", ""
}

// buildBinary builds the agent-deck binary into a temp directory with the
// toolchain pinned by go.mod and the given build tags. Returns the path to the
// built binary.
func buildBinary(t *testing.T, tags ...string) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "agent-deck")
	exact, line := pinnedToolchain(t)

	cmd := exec.Command("go", "build", "-tags", strings.Join(tags, ","), "-o", binPath, "./cmd/agent-deck")
	cmd.Dir = repoRoot(t)
	cmd.Env = os.Environ()
	// Pin the go.mod toolchain only when the local go is on another release
	// line: forcing an exact patch release makes go download it, which fails
	// in an offline test container that already ships a newer patch.
	if !strings.HasPrefix(runtime.Version(), line) {
		cmd.Env = append(cmd.Env, "GOTOOLCHAIN="+exact)
	}

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

// TestSmoke_TUIRenders builds the binary, starts it in tmux and checks that
// the session list shows the seeded group and sessions.
func TestSmoke_TUIRenders(t *testing.T) {
	requireTmux(t)

	binary := buildBinary(t)
	env := newDeckEnv(t)
	env.seed(t, binary)
	session := "tuitest_render"
	env.startDeck(t, session, binary)

	content := waitForSessionList(t, session)
	for _, want := range []string{"work", "smoke-one", "smoke-two"} {
		if !strings.Contains(content, want) {
			t.Errorf("session list does not show %q:\n%s", want, content)
		}
	}
	// The deck must be on its list, not parked in a start-up dialog.
	for _, dialog := range []string{"Help improve agent-deck?", "You are inside tmux", "Update now?"} {
		if strings.Contains(content, dialog) {
			t.Errorf("deck opened on %q instead of the session list:\n%s", dialog, content)
		}
	}

	// Capture screenshot if freeze is available
	if _, err := exec.LookPath("freeze"); err == nil {
		screenshotDir := filepath.Join(t.TempDir(), "screenshots")
		_ = os.MkdirAll(screenshotDir, 0755)
		captureScreenshot(t, session, "01_main_screen", screenshotDir)
	}
}

// TestSmoke_FirstRunWelcome starts the binary with no sessions and no config:
// a new user's first screen.
func TestSmoke_FirstRunWelcome(t *testing.T) {
	requireTmux(t)

	binary := buildBinary(t)
	env := newDeckEnv(t)
	if err := os.Remove(filepath.Join(env.home, ".config", "agent-deck", "config.toml")); err != nil {
		t.Fatalf("remove config.toml: %v", err)
	}
	session := "tuitest_welcome"
	env.startDeck(t, session, binary)

	// The first screen is the first-run check ("Agent Deck · quick check").
	waitForContent(t, session, "quick check", 20*time.Second)
}

// TestSmoke_NewSessionDialog verifies the new session dialog opens when 'n' is pressed.
func TestSmoke_NewSessionDialog(t *testing.T) {
	requireTmux(t)

	binary := buildBinary(t)
	env := newDeckEnv(t)
	env.seed(t, binary)
	session := "tuitest_dialog"
	env.startDeck(t, session, binary)
	waitForSessionList(t, session)

	// Press 'n' to open new session dialog
	sendKey(t, session, "n")
	content := waitForContent(t, session, "new session", 10*time.Second)
	for _, want := range []string{"Name", "Path"} {
		if !strings.Contains(content, want) {
			t.Errorf("new session dialog lacks the %q field:\n%s", want, content)
		}
	}

	// Esc closes it and the list is back.
	sendKey(t, session, "Escape")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(strings.ToLower(tmuxCapture(t, session)), "new session") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if after := tmuxCapture(t, session); strings.Contains(strings.ToLower(after), "new session") {
		t.Errorf("Esc did not close the new session dialog:\n%s", after)
	}

	// Capture screenshot if freeze is available
	if _, err := exec.LookPath("freeze"); err == nil {
		screenshotDir := filepath.Join(t.TempDir(), "screenshots")
		_ = os.MkdirAll(screenshotDir, 0755)
		captureScreenshot(t, session, "02_new_session_dialog", screenshotDir)
	}
}

// TestSmoke_QuitExitsCleanly verifies pressing 'q' exits the TUI.
func TestSmoke_QuitExitsCleanly(t *testing.T) {
	requireTmux(t)

	binary := buildBinary(t)
	env := newDeckEnv(t)
	env.seed(t, binary)
	session := "tuitest_quit"
	env.startDeck(t, session, binary)
	waitForSessionList(t, session)

	sendKey(t, session, "q")

	// The pane runs only the deck, so the tmux session ends when the deck exits.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("tmux", "has-session", "-t", session).Run(); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("TUI did not exit after pressing 'q' (tmux session still exists):\n%s", tmuxCapture(t, session))
}

// TestSmoke_BuildVersion verifies the built binary reports the correct Go toolchain version.
func TestSmoke_BuildVersion(t *testing.T) {
	binary := buildBinary(t)

	out, err := exec.Command("go", "version", "-m", binary).Output()
	if err != nil {
		t.Fatalf("go version -m: %v", err)
	}

	versionInfo := string(out)

	// Verify the binary was built on the release line pinned by go.mod.
	_, line := pinnedToolchain(t)
	if !strings.Contains(versionInfo, line) {
		t.Errorf("binary not built with %s.x toolchain:\n%s", line, versionInfo)
	}

	// Warn (don't fail) if vcs.modified=true since tests run in a working tree
	if strings.Contains(versionInfo, "vcs.modified=true") {
		t.Log("WARNING: binary built from dirty worktree (vcs.modified=true). Release builds must have vcs.modified=false.")
	}
}
