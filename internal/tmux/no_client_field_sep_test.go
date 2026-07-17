package tmux

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This file is the deterministic repro + regression guard for the 2026-06-18
// wake-notification bug.
//
// Root cause (pinned empirically): the launchd notify-daemon and the
// conductor-heartbeat run with a minimal environment — HOME and PATH only, no
// UTF-8 locale. tmux invoked under a non-UTF-8 locale (LANG=C or unset)
// sanitizes non-printable bytes in `-F` format output, rewriting TAB (0x09) to
// "_". (Setting LANG/LC_CTYPE to a UTF-8 locale makes the TAB survive; so does
// inheriting $TMUX, which borrows an attached client's UTF-8 context — that is
// why the $TMUX plist stopgap masked the bug.) The status probes delimited
// their fields with TAB, so under the daemon every line collapsed to one field,
// parseListWindowsOutput skipped it, the session cache came back empty,
// Session.Exists() reported false, and UpdateStatus stamped StatusError on every
// live session. That error failed the idle/waiting gate on both the wake-nudge
// and the heartbeat, so an idle conductor stopped being woken when a child
// finished.
//
// The fix replaces the TAB delimiter with tmuxFieldSep ("|"), a printable ASCII
// byte that survives the C-locale sanitization regardless of locale, $TMUX, or
// client presence. These tests assert PASS (session resolves) vs FAIL (the old
// TAB format is mangled and would resolve nothing) under the daemon's exact env.

// daemonLikeEnv returns the minimal environment the launchd notify-daemon runs
// under: HOME and PATH only, no UTF-8 locale and no $TMUX. This is what makes
// tmux sanitize TAB to "_" in format output, so the spawned probe reproduces
// the production condition regardless of how the test itself was launched.
func daemonLikeEnv() []string {
	return []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
}

// makeIsolatedServerNamed starts a detached tmux server on a private -L socket
// with a session named sessionName, and returns the socket. The server has no
// attached client (detached), exactly like the conductor sessions the daemon
// probes. Cleaned up via kill-server.
func makeIsolatedServerNamed(t *testing.T, sessionName string) (socket string) {
	t.Helper()
	socket = fmt.Sprintf("ad%x", sha256.Sum256([]byte(t.Name())))[:14]
	// All tmux calls for this server MUST share daemonLikeEnv: it omits
	// TMUX_TMPDIR (which TestMain isolates), so create / kill / list all resolve
	// `-L <socket>` to the same server under the default socket dir. Mixing envs
	// points kill-server and new-session at different servers — a "duplicate
	// session" flake when a prior run leaked a server.
	kill := func() {
		c := exec.Command("tmux", "-L", socket, "kill-server")
		c.Env = daemonLikeEnv()
		_ = c.Run()
	}
	// The socket name is deterministic per test, so a server leaked by a prior
	// aborted run would collide; clear it before creating a fresh one.
	kill()
	cmd := exec.Command("tmux", "-L", socket, "new-session", "-d", "-x", "200", "-y", "50", "-s", sessionName, "bash")
	cmd.Env = daemonLikeEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create tmux session: %v: %s", err, out)
	}
	t.Cleanup(kill)
	return socket
}

func listWindows(t *testing.T, socket, format string) string {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "list-windows", "-a", "-F", format)
	cmd.Env = daemonLikeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	return string(out)
}

func listPanes(t *testing.T, socket, format string) string {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "list-panes", "-a", "-F", format)
	cmd.Env = daemonLikeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-panes: %v: %s", err, out)
	}
	return string(out)
}

// TestNoClientMangling_RootCause documents the failure mode: under the daemon's
// non-UTF-8 locale, tmux rewrites the TAB field separators in -F output to "_",
// so the legacy tab-delimited probe resolves NO sessions. This is the bug; if
// this test ever stops reproducing (tmux changes its sanitization), the fix's
// rationale should be revisited.
func TestNoClientMangling_RootCause(t *testing.T) {
	requireTmux(t)
	const sess = "agentdeck_conductor-rootcause_aabbccdd"
	socket := makeIsolatedServerNamed(t, sess)

	legacyFormat := "#{session_name}\t#{window_activity}\t#{window_index}\t#{window_name}"
	out := listWindows(t, socket, legacyFormat)

	if strings.Contains(out, "\t") {
		t.Fatalf("expected TAB to be mangled by the no-client path, but output still has tabs: %q", out)
	}
	// The mangled line has no real delimiter, so the parser finds nothing.
	sessions, _ := parseListWindowsOutput(out)
	if _, ok := sessions[sess]; ok {
		t.Fatalf("root-cause repro broke: parser unexpectedly resolved %q from mangled output %q", sess, out)
	}
}

// TestNoClientFieldSep_FixResolvesSession is the PASS case: with the printable
// tmuxFieldSep delimiter the same no-client probe resolves the live session, so
// Exists() would return true and UpdateStatus would NOT misread it as error.
func TestNoClientFieldSep_FixResolvesSession(t *testing.T) {
	requireTmux(t)
	const sess = "agentdeck_conductor-fixcase_aabbccdd"
	socket := makeIsolatedServerNamed(t, sess)

	format := tmuxFmt("#{session_name}", "#{window_activity}", "#{window_index}", "#{window_name}")
	out := listWindows(t, socket, format)

	if !strings.Contains(out, tmuxFieldSep) {
		t.Fatalf("expected the printable field separator %q to survive the no-client path; got %q", tmuxFieldSep, out)
	}
	sessions, windows := parseListWindowsOutput(out)
	if _, ok := sessions[sess]; !ok {
		t.Fatalf("fix regressed: parser did not resolve %q from %q (sessions=%v)", sess, out, sessions)
	}
	if len(windows[sess]) == 0 {
		t.Errorf("expected at least one window for %q; got none", sess)
	}
}

// TestNoClientFieldSep_PaneInfoResolves is the PASS case for the pane-info
// probe (feeds running/dead detection and the tool spinner) under the same
// no-client condition.
func TestNoClientFieldSep_PaneInfoResolves(t *testing.T) {
	requireTmux(t)
	const sess = "agentdeck_conductor-panecase_aabbccdd"
	socket := makeIsolatedServerNamed(t, sess)

	format := tmuxFmt("#{session_name}", "#{pane_current_command}", "#{pane_dead}", "#{window_index}", "#{pane_index}", "#{pane_title}")
	out := listPanes(t, socket, format)

	panes, _ := parseListPanesOutput(out)
	info, ok := panes[sess]
	if !ok {
		t.Fatalf("fix regressed: pane parser did not resolve %q from %q", sess, out)
	}
	if info.Dead {
		t.Errorf("freshly created pane reported Dead=true: %+v", info)
	}
}

// TestDaemonPath_ExistsResolvesUnderCLocale is the end-to-end daemon-path
// assertion. It drives the exact chain the launchd notify-daemon and the
// heartbeat's `session show` run — RefreshSessionCache() populates the global
// session cache from a real `tmux list-windows -a -F …` subprocess, and
// Session.Exists() reads that cache — but under a forced C locale and no $TMUX,
// the condition that made the daemon misread every session as StatusError. With
// the tmuxFieldSep fix the cache resolves the live session and Exists() returns
// true; before the fix the mangled TAB output left the cache empty and Exists()
// returned false (→ StatusError → the idle/waiting wake gate failed).
//
// Note: the daemon and CLI never install a PipeManager (only the TUI does, via
// internal/ui/home.go), so RefreshSessionCache deterministically takes the
// subprocess branch this test exercises — not the control-mode pipe path.
func TestDaemonPath_ExistsResolvesUnderCLocale(t *testing.T) {
	requireTmux(t)
	const sess = "agentdeck_conductor-daemonpath_aabbccdd"

	// Reproduce the launchd daemon's environment in-process: a non-UTF-8 locale
	// (the trigger for tmux's control-char sanitization) and no inherited $TMUX.
	// Set BEFORE creating the server so both the create and RefreshSessionCache's
	// in-process probe share one env — in particular the TMUX_TMPDIR that
	// TestMain isolates, so `-L <socket>` resolves to the same server in both.
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")
	t.Setenv("LC_CTYPE", "C")
	t.Setenv("TMUX", "")

	socket := fmt.Sprintf("ad%x", sha256.Sum256([]byte(t.Name())))[:14]
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	// No cmd.Env override: inherit the (now C-locale, isolated-TMUX_TMPDIR)
	// process env so the server lands where RefreshSessionCache will look.
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-x", "200", "-y", "50", "-s", sess, "bash").CombinedOutput(); err != nil {
		t.Fatalf("create tmux session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	// Point the package-level default socket at our isolated test server so
	// RefreshSessionCache probes it and Session.Exists() (SocketName == default)
	// trusts the resulting cache.
	prev := DefaultSocketName()
	SetDefaultSocketName(socket)
	t.Cleanup(func() {
		SetDefaultSocketName(prev)
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
	})

	RefreshSessionCache()

	if exists, valid := sessionExistsFromCache(sess); !valid || !exists {
		t.Fatalf("daemon path regressed: sessionExistsFromCache(%q) = (exists=%v, valid=%v) under C locale; "+
			"the status cache did not resolve the live session", sess, exists, valid)
	}

	s := &Session{Name: sess, SocketName: socket}
	if !s.Exists() {
		t.Fatalf("daemon path regressed: Session.Exists() returned false for live session %q under C locale "+
			"(this is the exact StatusError misread that suppressed the idle wake-nudge)", sess)
	}
}

// TestPipePath_FieldSepResolvesSessionAndPane closes the control-mode pipe
// coverage gap. The pipe producers (RefreshAllActivities, RefreshAllPaneInfo)
// share parseListWindowsOutput / parseListPanesOutput with the subprocess path,
// so a producer↔parser delimiter or field-order drift would silently empty the
// result and the daemon/TUI would fall back or misread. The existing
// TestPipeManager_RefreshAllActivities silent-skips in CI/isolated envs
// (skipIfNoTmuxServer needs a real external session); this uses the strict
// helper so it RUNS wherever tmux exists, and asserts the session resolves
// through BOTH pipe refreshers.
func TestPipePath_FieldSepResolvesSessionAndPane(t *testing.T) {
	name := createTestSessionStrict(t, "fieldsep")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm := NewPipeManager(ctx, nil)
	defer pm.Close()
	if err := pm.Connect(name, ""); err != nil {
		t.Fatalf("pipe connect: %v", err)
	}

	activities, _, err := pm.RefreshAllActivities()
	if err != nil {
		t.Fatalf("RefreshAllActivities: %v", err)
	}
	if _, ok := activities[name]; !ok {
		t.Fatalf("pipe RefreshAllActivities did not resolve %q — producer/parser delimiter drift; got %v", name, activities)
	}

	panes, _, err := pm.RefreshAllPaneInfo()
	if err != nil {
		t.Fatalf("RefreshAllPaneInfo: %v", err)
	}
	if _, ok := panes[name]; !ok {
		t.Fatalf("pipe RefreshAllPaneInfo did not resolve %q — producer/parser delimiter or field-order drift; got %v", name, panes)
	}
}

// TestParseListWindowsOutput_FieldSepInWindowName proves collision-safety: a
// tmuxFieldSep inside the trailing free-text field (window_name) is preserved
// intact because it is parsed last with SplitN.
func TestParseListWindowsOutput_FieldSepInWindowName(t *testing.T) {
	const sess = "agentdeck_conductor-x_deadbeef"
	weirdName := "my|weird|window"
	line := tmuxFmt(sess, "1781842421", "0", weirdName)

	sessions, windows := parseListWindowsOutput(line)
	if _, ok := sessions[sess]; !ok {
		t.Fatalf("session %q not parsed from %q", sess, line)
	}
	if len(windows[sess]) != 1 || windows[sess][0].Name != weirdName {
		t.Fatalf("window_name with embedded %q not preserved: got %+v", tmuxFieldSep, windows[sess])
	}
}

// TestParseListPanesOutput_FieldSepInPaneTitle proves the same collision-safety
// for the pane probe: a tmuxFieldSep inside pane_title (the only free-text
// field, placed last) does not corrupt the leading command/dead fields.
func TestParseListPanesOutput_FieldSepInPaneTitle(t *testing.T) {
	const sess = "agentdeck_conductor-y_cafebabe"
	weirdTitle := "Claude | working | foo"
	// session | command | dead | window_index | pane_index | title
	line := tmuxFmt(sess, "node", "0", "0", "1", weirdTitle)

	panes, _ := parseListPanesOutput(line)
	info, ok := panes[sess]
	if !ok {
		t.Fatalf("session %q not parsed from %q", sess, line)
	}
	if info.CurrentCommand != "node" {
		t.Errorf("CurrentCommand corrupted by delimiter in title: got %q want %q", info.CurrentCommand, "node")
	}
	if info.Dead {
		t.Errorf("Dead corrupted by delimiter in title: got true")
	}
	if info.Title != weirdTitle {
		t.Errorf("pane_title with embedded %q not preserved: got %q want %q", tmuxFieldSep, info.Title, weirdTitle)
	}
}
