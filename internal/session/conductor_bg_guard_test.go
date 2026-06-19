package session

import (
	"path/filepath"
	"strings"
	"testing"
)

// The Background-domain eviction guard: `conductor setup` run from inside a
// conductor pane (launchd "Background" domain) must NOT unload/load the live
// notify-daemon — that cross-domain unload evicts it fleet-wide. These tests
// drive installTransitionNotifierLaunchd through its seams (no real launchctl,
// HOME pointed at a temp dir) and assert which launchctl verbs are issued.

func withGuardSeams(t *testing.T, managerName string, running bool) *[]string {
	t.Helper()
	// Isolate the plist path so the proceed-path WriteFile lands in a temp dir,
	// never the real ~/Library/LaunchAgents.
	t.Setenv("HOME", t.TempDir())

	var calls []string
	origMgr := transitionNotifierManagerName
	origRun := transitionNotifierIsRunning
	origExec := runLaunchctl
	transitionNotifierManagerName = func() string { return managerName }
	transitionNotifierIsRunning = func() bool { return running }
	runLaunchctl = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() {
		transitionNotifierManagerName = origMgr
		transitionNotifierIsRunning = origRun
		runLaunchctl = origExec
	})
	return &calls
}

func containsPrefix(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// SAFE-AFTER: Background domain + daemon running → guard skips, ZERO launchctl
// verbs issued (no unload = no eviction), and the existing plist is not rewritten.
func TestInstallTransitionNotifierLaunchd_BackgroundRunning_SkipsUnload(t *testing.T) {
	calls := withGuardSeams(t, "Background", true)

	path, err := installTransitionNotifierLaunchd()
	if err != nil {
		t.Fatalf("guarded skip must not error; got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("guard must issue NO launchctl verbs in Background w/ running daemon "+
			"(an unload would evict it cross-domain); got %v", *calls)
	}
	if path == "" {
		t.Fatalf("guard should still return the plist path for the caller's message")
	}
}

// EVICTS-BEFORE (the bug, modeled): without the guard condition (Aqua login
// domain), the install proceeds and DOES issue unload + load — the exact verbs
// that, in the Background domain, evict the live daemon. Pins that the guard is
// the only thing standing between Background and that unload.
func TestInstallTransitionNotifierLaunchd_LoginDomain_ProceedsWithUnloadLoad(t *testing.T) {
	calls := withGuardSeams(t, "Aqua", false)

	if _, err := installTransitionNotifierLaunchd(); err != nil {
		t.Fatalf("login-domain install must proceed cleanly; got %v", err)
	}
	if !containsPrefix(*calls, "unload ") {
		t.Fatalf("login-domain install must issue the unload (the evicting verb); got %v", *calls)
	}
	if !containsPrefix(*calls, "load ") {
		t.Fatalf("login-domain install must issue the load; got %v", *calls)
	}
}

// Background but daemon NOT running: nothing to protect → proceed (first install
// from a conductor pane when no daemon exists must still work).
func TestInstallTransitionNotifierLaunchd_BackgroundNotRunning_Proceeds(t *testing.T) {
	calls := withGuardSeams(t, "Background", false)

	if _, err := installTransitionNotifierLaunchd(); err != nil {
		t.Fatalf("background-but-not-running install must proceed; got %v", err)
	}
	if !containsPrefix(*calls, "unload ") || !containsPrefix(*calls, "load ") {
		t.Fatalf("with no live daemon to protect, install must proceed (unload+load); got %v", *calls)
	}
}

// The plist path the guard returns must be the real notifier path (sanity that
// the early return didn't fabricate a path).
func TestInstallTransitionNotifierLaunchd_SkipReturnsNotifierPath(t *testing.T) {
	withGuardSeams(t, "Background", true)
	path, err := installTransitionNotifierLaunchd()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want, _ := TransitionNotifierLaunchdPlistPath()
	if filepath.Base(path) != filepath.Base(want) {
		t.Fatalf("skip returned %q, want notifier plist %q", path, want)
	}
}
