package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// fakeUpdater records unattended runs and answers with a canned result.
type fakeUpdater struct {
	exes   []string
	output string
	err    error
}

func (f *fakeUpdater) run(_ context.Context, exe string) (string, error) {
	f.exes = append(f.exes, exe)
	return f.output, f.err
}

// stubUpdateSettings replaces the config seam for one test.
func stubUpdateSettings(t *testing.T, s session.UpdateSettings) {
	t.Helper()
	prev := loadUpdateSettings
	loadUpdateSettings = func() session.UpdateSettings { return s }
	t.Cleanup(func() { loadUpdateSettings = prev })
}

// newAutoInstallTestHome is a clean home with an installable update, the
// default settings (auto_install on) and a fake updater.
func newAutoInstallTestHome(t *testing.T) (*Home, *fakeUpdater) {
	t.Helper()
	t.Setenv(update.SkipUpdateCheckEnv, "")
	stubUpdateSettings(t, session.UpdateSettings{})
	f := &fakeUpdater{output: "Updated to v1.16.1\n"}
	prev := runUnattendedUpdate
	runUnattendedUpdate = f.run
	t.Cleanup(func() { runUnattendedUpdate = prev })
	return newInstallTestHome(t), f
}

// TestAutoInstall_TriggersOncePerVersion pins the core guard: one update
// check starts one background run, a second check while it runs starts
// nothing, and after it finished the same version is not tried again.
func TestAutoInstall_TriggersOncePerVersion(t *testing.T) {
	h, f := newAutoInstallTestHome(t)
	cmd := h.maybeAutoInstall(h.updateInfo)
	if cmd == nil {
		t.Fatal("expected the unattended install to start")
	}
	if h.autoInstallInFlight != "1.16.1" {
		t.Fatalf("inFlight = %q, want 1.16.1", h.autoInstallInFlight)
	}
	if again := h.maybeAutoInstall(h.updateInfo); again != nil {
		t.Fatal("a second check while the install runs must not start another")
	}
	msg, ok := cmd().(unattendedInstallFinishedMsg)
	if !ok {
		t.Fatalf("cmd delivered %T, want unattendedInstallFinishedMsg", msg)
	}
	if len(f.exes) != 1 || f.exes[0] != "/bin/agent-deck" {
		t.Fatalf("updater ran with %v, want the fingerprinted exe once", f.exes)
	}
	if msg.version != "1.16.1" || msg.err != nil || !strings.Contains(msg.output, "Updated") {
		t.Fatalf("msg = %+v", msg)
	}
	if recheck := h.handleUnattendedInstallFinished(msg); recheck == nil {
		t.Fatal("finished handler must re-poll the binary and the update cache")
	}
	if h.autoInstallInFlight != "" || h.err != nil {
		t.Fatalf("after success: inFlight=%q err=%v", h.autoInstallInFlight, h.err)
	}
	if again := h.maybeAutoInstall(h.updateInfo); again != nil {
		t.Fatal("the same version must not be installed twice in an hour")
	}
	// A newer release is a new attempt.
	h.updateInfo.LatestVersion = "1.16.2"
	if cmd := h.maybeAutoInstall(h.updateInfo); cmd == nil {
		t.Fatal("a newer version must start a fresh install")
	}
}

// TestAutoInstall_FailureShowsFooterAndBacksOff pins the failure path: one
// footer line built from the updater's first output line, and no retry of
// that version until an hour has passed.
func TestAutoInstall_FailureShowsFooterAndBacksOff(t *testing.T) {
	h, f := newAutoInstallTestHome(t)
	f.output = "download failed: 503 Service Unavailable\nsecond line\n"
	f.err = errors.New("exit status 1")
	cmd := h.maybeAutoInstall(h.updateInfo)
	if cmd == nil {
		t.Fatal("expected the install to start")
	}
	if recheck := h.handleUnattendedInstallFinished(cmd().(unattendedInstallFinishedMsg)); recheck == nil {
		t.Fatal("finished handler must re-poll even after a failure")
	}
	want := "auto-update to v1.16.1 failed: download failed: 503 Service Unavailable; run agent-deck update"
	if h.err == nil || h.err.Error() != want {
		t.Fatalf("footer = %v, want %q", h.err, want)
	}
	if again := h.maybeAutoInstall(h.updateInfo); again != nil {
		t.Fatal("a failed version must not be retried within the hour")
	}
	h.autoInstallAttempts["1.16.1"] = time.Now().Add(-2 * autoInstallRetryAfter)
	again := h.maybeAutoInstall(h.updateInfo)
	if again == nil {
		t.Fatal("after the back-off the version may be tried again")
	}
	again()
	if len(f.exes) != 2 {
		t.Fatalf("updater runs = %d, want 2", len(f.exes))
	}
	// Without output the error itself is the footer detail.
	h2, f2 := newAutoInstallTestHome(t)
	f2.output, f2.err = "", errors.New("signal: killed")
	h2.handleUnattendedInstallFinished(h2.maybeAutoInstall(h2.updateInfo)().(unattendedInstallFinishedMsg))
	if h2.err == nil || !strings.Contains(h2.err.Error(), "failed: signal: killed;") {
		t.Fatalf("footer without output = %v", h2.err)
	}
}

// TestAutoInstall_SkipConditions pins every reason the periodic check
// leaves the updater alone.
func TestAutoInstall_SkipConditions(t *testing.T) {
	cases := map[string]func(t *testing.T, h *Home){
		"auto_install off": func(t *testing.T, h *Home) {
			stubUpdateSettings(t, session.UpdateSettings{AutoInstall: boolPtr(false)})
		},
		"homebrew-managed": func(_ *testing.T, h *Home) { h.homebrewManaged = true },
		"still publishing": func(_ *testing.T, h *Home) { h.updateInfo.PublishingVersion = "1.16.2" },
		"skip env set":     func(t *testing.T, _ *Home) { t.Setenv(update.SkipUpdateCheckEnv, "1") },
		"nothing available": func(_ *testing.T, h *Home) {
			h.updateInfo.Available = false
		},
		"already on disk": func(_ *testing.T, h *Home) {
			h.binaryWatch.observe(fpAt(2, 2))
			h.binaryWatch.recordProbe(fpAt(2, 2), "1.16.1", nil)
		},
	}
	for name, arm := range cases {
		t.Run(name, func(t *testing.T) {
			h, f := newAutoInstallTestHome(t)
			arm(t, h)
			if cmd := h.maybeAutoInstall(h.updateInfo); cmd != nil {
				t.Fatalf("%s: expected no install command", name)
			}
			if h.autoInstallInFlight != "" || len(f.exes) != 0 || h.err != nil {
				t.Fatalf("%s: inFlight=%q runs=%v err=%v", name, h.autoInstallInFlight, f.exes, h.err)
			}
		})
	}
	// A nil result (network down) is not a reason to log or act.
	h, _ := newAutoInstallTestHome(t)
	if cmd := h.maybeAutoInstall(nil); cmd != nil {
		t.Fatal("nil update info must be inert")
	}
}

// TestAutoInstall_ManualKeyStillWorks pins that the unattended path does
// not take the interactive install key away: with auto_install off the
// key still hands Bubble Tea the exec command.
func TestAutoInstall_ManualKeyStillWorks(t *testing.T) {
	h, _ := newAutoInstallTestHome(t)
	stubUpdateSettings(t, session.UpdateSettings{AutoInstall: boolPtr(false)})
	if _, cmd := h.tryInstallUpdate(); cmd == nil {
		t.Fatal("install key must still run the interactive updater")
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 8}
	for _, chunk := range []string{"abcdef", "ghij", "kl"} {
		if n, err := tb.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got := tb.String(); got != "efghijkl" {
		t.Fatalf("tail = %q, want the last 8 bytes", got)
	}
	if got := firstLine("\n  \n  first \nsecond", "fallback"); got != "first" {
		t.Fatalf("firstLine = %q", got)
	}
	if got := firstLine("  \n", "fallback"); got != "fallback" {
		t.Fatalf("firstLine on blank text = %q", got)
	}
}
