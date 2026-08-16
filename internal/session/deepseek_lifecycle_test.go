package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// End-to-end lifecycle proof for the DeepSeek Harness integration.
//
// This is the "after" half of the reproduce/fix/reprove pair for the feature:
// a real tmux pane, a real agent-deck Instance, and one process per phase,
// covering launch -> status transitions -> send delivery -> prompt round-trip
// -> restart-with-context.
//
// It runs against testdata/fake-dsh rather than @deepseek-ai/dsh because the
// real launcher refuses to answer anything without a DEEPSEEK_API_KEY, which CI
// does not have (`dsh --profile headless "say hi"` exits 1 with
// MISSING_CREDENTIAL). The fake reproduces the parts of the CLI contract this
// integration depends on, each captured from the real 0.1.0-rc.6 binary — see
// the header of testdata/fake-dsh for exactly what is emulated and what is not.
// The pieces that CAN be checked against the real binary (flag grammar, the
// DSH_HOME layout, the ready banner, exit codes, the credential error) are
// pinned in deepseek_test.go and internal/tmux/deepseek_test.go.

// fakeDshPath returns the absolute path of the emulator, skipping the test when
// it is missing or not executable.
func fakeDshPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "fake-dsh"))
	if err != nil {
		t.Skipf("resolve fake-dsh: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("fake-dsh not present: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("fake-dsh is not executable (%s); the checkout lost its mode bit", path)
	}
	return path
}

// waitForPaneFull polls the pane's WHOLE buffer until want appears.
//
// Use this for anything a one-shot printed. Preview() returns only the last
// three lines, and once a headless run exits, tmux's remain-on-exit banner
// ("Pane is dead (status 0, …)") occupies the tail — so asserting through
// Preview() is a race against that banner, not a check on the output.
func waitForPaneFull(t *testing.T, inst *Instance, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		content, err := inst.PreviewFull()
		if err == nil {
			last = content
			if strings.Contains(content, want) {
				return content
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pane buffer never showed %q within %s; buffer:\n%s", want, timeout, last)
	return ""
}

// waitForPane polls the pane until want appears, and fails with the captured
// content when it does not. Polling (rather than one sleep) keeps the test both
// fast on a quick box and stable on a loaded one.
func waitForPane(t *testing.T, inst *Instance, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		content, err := inst.Preview()
		if err == nil {
			last = content
			if strings.Contains(content, want) {
				return content
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Preview() is the last few lines only; on failure dump the whole buffer,
	// which is where a respawned one-shot's output actually lives.
	full, fullErr := inst.PreviewFull()
	if fullErr != nil {
		full = "(PreviewFull: " + fullErr.Error() + ")"
	}
	t.Fatalf("pane never showed %q within %s;\nlast preview:\n%s\nfull buffer:\n%s", want, timeout, last, full)
	return ""
}

func TestDeepSeekLifecycle_LaunchSendRestart(t *testing.T) {
	// New test: uses TestMain's bootstrapped server on the isolated socket, so
	// the binary check is the right gate (skipIfNoTmuxServer is the legacy one).
	skipIfNoTmuxBinary(t)

	fake := fakeDshPath(t)
	home := t.TempDir()
	workspace := t.TempDir()
	dshHome := filepath.Join(home, ".dsh")

	// A credentials document stands in for DEEPSEEK_API_KEY: the emulator honors
	// the same two credential sources the real launcher documents, and a file
	// survives into the pane's environment without agent-deck exporting a secret
	// on a command line.
	if err := os.MkdirAll(dshHome, 0o755); err != nil {
		t.Fatalf("mkdir DSH_HOME: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dshHome, ".credentials.yaml"), []byte("deepseek: test\n"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Command:   fake,
		ConfigDir: dshHome,
		// An installed interactive profile — the shape `dsh --profile tui
		// --resume <session>` in the launcher's own help. resume_flag is set
		// because THIS profile's app accepts one; it stays unset for the shipped
		// web/headless profiles.
		Profile:    "tui",
		ResumeFlag: "--resume",
	}})

	inst := NewInstanceWithTool("deepseek-lifecycle-e2e", workspace, "deepseek")
	t.Cleanup(func() { _ = inst.Kill() })

	// --- phase 1: launch ------------------------------------------------------
	if err := inst.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	banner := waitForPane(t, inst, "DeepSeek Harness", 15*time.Second)
	if !strings.Contains(banner, "(profile tui)") {
		t.Errorf("pane did not boot the configured profile:\n%s", banner)
	}

	// The launcher wrote its workspace index, so agent-deck can find the
	// conversation this pane is holding.
	waitForSessionIndex(t, dshHome, workspace, 15*time.Second)
	sessionID := DiscoverDeepSeekSessionID(dshHome, workspace)
	if sessionID == "" {
		t.Fatalf("no dsh session discovered under %s for %s", dshHome, workspace)
	}
	if !strings.Contains(banner, sessionID) {
		t.Errorf("discovered session %q is not the one the pane reported:\n%s", sessionID, banner)
	}

	// --- phase 2: send delivery + status transitions --------------------------
	if err := inst.tmuxSession.SendKeysAndEnter("summarize the diff"); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Busy: the pane carries the marker the deepseek preset treats as working,
	// and UpdateStatus reports running rather than idle.
	waitForPane(t, inst, "esc to interrupt", 10*time.Second)
	if !waitForDeepSeekStatus(t, inst, StatusRunning, 10*time.Second) {
		t.Errorf("status never became %q while the pane was busy (got %q)", StatusRunning, inst.Status)
	}

	// --- phase 3: prompt round-trip ------------------------------------------
	waitForPane(t, inst, "answered: summarize the diff", 20*time.Second)

	// Back to not-busy once the answer lands. The session must leave "running"
	// on its own — a session stuck busy is the failure this asserts against
	// (the busy marker has to be a redrawn footer, not scrollback).
	if !waitForDeepSeekStatusOtherThan(t, inst, StatusRunning, 15*time.Second) {
		t.Errorf("status stayed %q after the answer landed", inst.Status)
	}

	// --- phase 4: restart with context ---------------------------------------
	if !inst.CanRestart() {
		t.Fatal("CanRestart() = false for a live deepseek session")
	}
	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart(): %v", err)
	}
	if inst.DeepSeekSessionID != sessionID {
		t.Errorf("restart discovered session %q, want the pane's own %q", inst.DeepSeekSessionID, sessionID)
	}
	resumed := waitForPane(t, inst, "resumed session", 20*time.Second)
	if !strings.Contains(resumed, sessionID) {
		t.Errorf("restart resumed a different session than %q:\n%s", sessionID, resumed)
	}
	// A resumed boot must NOT mint a new conversation.
	if strings.Contains(resumed, "new session") {
		t.Errorf("restart started a fresh session instead of resuming:\n%s", resumed)
	}

	// --- phase 5: the resumed pane still answers ------------------------------
	if err := inst.tmuxSession.SendKeysAndEnter("still there?"); err != nil {
		t.Fatalf("send after restart: %v", err)
	}
	waitForPane(t, inst, "answered: still there?", 20*time.Second)
}

// TestDeepSeekLifecycle_HeadlessOneShot proves the other shipped shape: the
// prompt rides the command line, the process answers once and exits, and
// agent-deck reports the prompt as already delivered so nothing is typed twice.
func TestDeepSeekLifecycle_HeadlessOneShot(t *testing.T) {
	// New test: uses TestMain's bootstrapped server on the isolated socket, so
	// the binary check is the right gate (skipIfNoTmuxServer is the legacy one).
	skipIfNoTmuxBinary(t)

	fake := fakeDshPath(t)
	home := t.TempDir()
	workspace := t.TempDir()
	dshHome := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(dshHome, 0o755); err != nil {
		t.Fatalf("mkdir DSH_HOME: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dshHome, ".credentials.yaml"), []byte("deepseek: test\n"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Command:   fake,
		ConfigDir: dshHome,
		Profile:   "headless",
	}})

	inst := NewInstanceWithTool("deepseek-headless-e2e", workspace, "deepseek")
	t.Cleanup(func() { _ = inst.Kill() })

	command, embedded := inst.buildDeepSeekCommandWithPrompt(inst.Command, "run the tests")
	if !embedded {
		t.Fatalf("headless prompt was not embedded in %q", command)
	}

	if err := inst.StartWithMessage("run the tests"); err != nil {
		t.Fatalf("StartWithMessage(): %v", err)
	}
	waitForPaneFull(t, inst, "answered: run the tests", 20*time.Second)

	// One answer only: the embedded prompt must not ALSO be typed into the pane
	// after start, which would run the task twice.
	content, err := inst.PreviewFull()
	if err != nil {
		t.Fatalf("PreviewFull(): %v", err)
	}
	if strings.Count(content, "answered: run the tests") != 1 {
		t.Errorf("the task ran more than once (prompt both embedded and typed):\n%s", content)
	}

	// The one-shot recorded its conversation where agent-deck looks for it.
	waitForSessionIndex(t, dshHome, workspace, 15*time.Second)
	if id := DiscoverDeepSeekSessionID(dshHome, workspace); id == "" {
		t.Error("the one-shot run left no discoverable session")
	}
}

// TestDeepSeekLifecycle_WebReadyBanner proves the third shipped shape: a
// long-lived server pane whose single ready line is the readiness signal, and
// which the status detector must read as waiting rather than busy.
func TestDeepSeekLifecycle_WebReadyBanner(t *testing.T) {
	// New test: uses TestMain's bootstrapped server on the isolated socket, so
	// the binary check is the right gate (skipIfNoTmuxServer is the legacy one).
	skipIfNoTmuxBinary(t)

	fake := fakeDshPath(t)
	workspace := t.TempDir()
	dshHome := filepath.Join(t.TempDir(), ".dsh")

	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Command:   fake,
		ConfigDir: dshHome,
		Profile:   "web",
		Host:      "127.0.0.1",
		Port:      intPtr(31337),
	}})

	inst := NewInstanceWithTool("deepseek-web-e2e", workspace, "deepseek")
	t.Cleanup(func() { _ = inst.Kill() })

	if err := inst.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	waitForPane(t, inst, "dsh web: http://127.0.0.1:31337", 20*time.Second)

	// A served-and-idle server must not read as busy, or the session would show
	// a spinner for as long as it is up.
	if !waitForDeepSeekStatusOtherThan(t, inst, StatusRunning, 10*time.Second) {
		t.Errorf("an idle dsh web server reports %q", inst.Status)
	}

	// And restart brings it back on the same port against the same DSH_HOME.
	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart(): %v", err)
	}
	waitForPane(t, inst, "dsh web: http://127.0.0.1:31337", 20*time.Second)
}

// waitForSessionIndex blocks until the launcher has flushed its workspace index
// for this workspace.
func waitForSessionIndex(t *testing.T, home, workspace string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(DeepSeekSessionIDs(home, workspace)) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no workspace index under %s for %s within %s", home, workspace, timeout)
}

// waitForDeepSeekStatus polls UpdateStatus until the instance reports want.
func waitForDeepSeekStatus(t *testing.T, inst *Instance, want Status, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if err := inst.UpdateStatus(); err == nil && inst.Status == want {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// waitForDeepSeekStatusOtherThan polls UpdateStatus until the instance reports anything other
// than unwanted.
func waitForDeepSeekStatusOtherThan(t *testing.T, inst *Instance, unwanted Status, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if err := inst.UpdateStatus(); err == nil && inst.Status != unwanted {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// --- review findings on PR #1942 -------------------------------------------
//
// Three P1s and one P2 from the adversarial review, each reproduced here before
// being fixed. The shared theme of the P1s: the DeepSeek profiles are not
// interchangeable, and treating them as one shape either loses a user's prompt
// or launches an invocation dsh rejects.

// TestDeepSeekLifecycle_WebProfileRefusesInitialPrompt covers P1a — the worst
// class in this repo: silent message loss.
//
// The web profile is an HTTP server. It has no terminal prompt, so a message
// "delivered" by typing into its pane goes to the server process's stdin and is
// gone. Before the fix, `agent-deck launch -c deepseek -m ...` on the DEFAULT
// profile reported success and discarded the request. Refusing loudly is the
// only honest option here: agent-deck cannot submit through dsh's browser API
// without reimplementing its trust fence.
func TestDeepSeekLifecycle_WebProfileRefusesInitialPrompt(t *testing.T) {
	// New test: uses TestMain's bootstrapped server on the isolated socket, so
	// the binary check is the right gate (skipIfNoTmuxServer is the legacy one).
	skipIfNoTmuxBinary(t)

	fake := fakeDshPath(t)
	workspace := t.TempDir()
	dshHome := filepath.Join(t.TempDir(), ".dsh")

	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Command:   fake,
		ConfigDir: dshHome,
		Profile:   "web", // the default
	}})

	inst := NewInstanceWithTool("deepseek-web-refuse-e2e", workspace, "deepseek")
	t.Cleanup(func() { _ = inst.Kill() })

	err := inst.StartWithMessage("please refactor the parser")
	if err == nil {
		t.Fatal("StartWithMessage on the web profile returned nil — the prompt was accepted and then silently discarded")
	}
	// The error has to name the cause and the way out, or the user just sees a
	// failure they cannot act on.
	for _, want := range []string{"web", "profile"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Refusal must happen BEFORE the spawn: leaving a running server behind
	// after reporting failure is its own trap.
	if inst.Exists() {
		t.Error("a session was spawned despite the refusal")
	}
}

// TestDeepSeekLifecycle_HeadlessStartWithoutTaskRefuses covers P1b.
//
// `dsh --profile headless` with no positional task is a usage error — the app
// rejects it before any prompt could be delivered. Every plain Start() built
// exactly that: an ordinary TUI launch, and `launch -m --no-wait`, which
// deliberately calls Start() before its asynchronous pane send.
func TestDeepSeekLifecycle_HeadlessStartWithoutTaskRefuses(t *testing.T) {
	skipIfNoTmuxBinary(t)

	fake := fakeDshPath(t)
	workspace := t.TempDir()
	dshHome := filepath.Join(t.TempDir(), ".dsh")

	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Command:   fake,
		ConfigDir: dshHome,
		Profile:   "headless",
	}})

	inst := NewInstanceWithTool("deepseek-headless-notask-e2e", workspace, "deepseek")
	t.Cleanup(func() { _ = inst.Kill() })

	err := inst.Start()
	if err == nil {
		t.Fatal("Start() on the headless profile with no task returned nil — dsh exits with a usage error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "task") {
		t.Errorf("error %q does not say a task is required", err)
	}
	if inst.Exists() {
		t.Error("a doomed pane was spawned instead of refusing up front")
	}
}

// TestDeepSeekLifecycle_HeadlessRestartReplaysTask covers P1c.
//
// A one-shot's task IS its invocation, so a restart that forgets the task
// rebuilds `dsh --profile headless` and replaces the retained answer with a
// usage error. Restart must replay the same task.
func TestDeepSeekLifecycle_HeadlessRestartReplaysTask(t *testing.T) {
	skipIfNoTmuxBinary(t)

	fake := fakeDshPath(t)
	home := t.TempDir()
	workspace := t.TempDir()
	dshHome := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(dshHome, 0o755); err != nil {
		t.Fatalf("mkdir DSH_HOME: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dshHome, ".credentials.yaml"), []byte("deepseek: test\n"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Command:   fake,
		ConfigDir: dshHome,
		Profile:   "headless",
	}})

	inst := NewInstanceWithTool("deepseek-headless-restart-e2e", workspace, "deepseek")
	t.Cleanup(func() { _ = inst.Kill() })

	if err := inst.StartWithMessage("run the tests"); err != nil {
		t.Fatalf("StartWithMessage(): %v", err)
	}
	waitForPaneFull(t, inst, "answered: run the tests", 20*time.Second)

	// The task is what makes this session restartable at all.
	if inst.DeepSeekTask != "run the tests" {
		t.Fatalf("DeepSeekTask = %q, want the launched task", inst.DeepSeekTask)
	}
	if !inst.CanRestart() {
		t.Fatal("CanRestart() = false for a headless session whose task is known")
	}

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart(): %v", err)
	}
	// Replayed, not a usage error. The answer lives in the buffer; the 3-line
	// preview tail belongs to tmux's dead-pane banner once the one-shot exits.
	content := waitForPaneFull(t, inst, "answered: run the tests", 20*time.Second)
	if strings.Contains(content, "a task is required") || strings.Contains(content, "Usage: dsh") {
		t.Errorf("restart rebuilt a taskless invocation:\n%s", content)
	}
}

// TestDeepSeekLifecycle_HeadlessRestartWithoutTaskIsRefused pins the other half
// of P1c: a headless session with no recorded task (a row written before the
// task was persisted) must report that it cannot restart rather than promise a
// restart that lands on a usage error.
func TestDeepSeekLifecycle_HeadlessRestartWithoutTaskIsRefused(t *testing.T) {
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{Profile: "headless"}})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
	if inst.CanRestart() {
		t.Error("CanRestart() = true for a headless session with no recorded task")
	}

	inst.DeepSeekTask = "run the tests"
	if !inst.CanRestart() {
		t.Error("CanRestart() = false once the task is known")
	}
}
