package update

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// TestNextRecheck pins the cadence a long-running process re-asks for
// updates: every RecheckInterval normally (a cache read; the network only
// when the cache is older than the check interval), RecheckBackoff after a
// failed attempt, and right away when it never asked.
func TestNextRecheck(t *testing.T) {
	now := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	if got := NextRecheck(time.Time{}, false); !got.IsZero() {
		t.Fatalf("never checked: NextRecheck = %v, want zero (due now)", got)
	}
	if got, want := NextRecheck(now, false), now.Add(RecheckInterval); !got.Equal(want) {
		t.Fatalf("after a good check: NextRecheck = %v, want %v", got, want)
	}
	if got, want := NextRecheck(now, true), now.Add(RecheckBackoff); !got.Equal(want) {
		t.Fatalf("after a failed check: NextRecheck = %v, want %v", got, want)
	}
	if RecheckBackoff <= RecheckInterval {
		t.Fatalf("backoff %v must be longer than the normal interval %v", RecheckBackoff, RecheckInterval)
	}
	if RecheckInterval > 5*time.Minute {
		t.Fatalf("RecheckInterval = %v; an open process must notice a release within the check interval, so re-asking must be much finer than the hourly cache", RecheckInterval)
	}
}

// TestNextRecheckAfterFailures pins the exponential-backoff-with-jitter
// schedule a headless poller uses after repeated failures: doubling per
// consecutive failure, capped at RecheckBackoffMax, and never checking
// before the un-jittered floor for that failure count.
func TestNextRecheckAfterFailures(t *testing.T) {
	now := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)

	if got := NextRecheckAfterFailures(time.Time{}, 3); !got.IsZero() {
		t.Fatalf("never checked: NextRecheckAfterFailures = %v, want zero (due now)", got)
	}
	if got, want := NextRecheckAfterFailures(now, 0), now.Add(RecheckInterval); !got.Equal(want) {
		t.Fatalf("no failures: NextRecheckAfterFailures = %v, want %v (same as NextRecheck(last, false))", got, want)
	}

	// Pin jitter to zero so the floor of each step is exact.
	origJitter := backoffJitter
	backoffJitter = func(int64) int64 { return 0 }
	t.Cleanup(func() { backoffJitter = origJitter })

	if got, want := NextRecheckAfterFailures(now, 1), now.Add(RecheckBackoff); !got.Equal(want) {
		t.Fatalf("1st failure: NextRecheckAfterFailures = %v, want %v", got, want)
	}
	if got, want := NextRecheckAfterFailures(now, 2), now.Add(2*RecheckBackoff); !got.Equal(want) {
		t.Fatalf("2nd failure: NextRecheckAfterFailures = %v, want %v (doubled)", got, want)
	}
	// Enough consecutive failures must saturate at the cap, not keep doubling
	// into an ever-larger delay.
	if got, want := NextRecheckAfterFailures(now, 20), now.Add(RecheckBackoffMax); !got.Equal(want) {
		t.Fatalf("many failures: NextRecheckAfterFailures = %v, want the capped %v", got, want)
	}

	// Jitter only ever adds delay (never checks earlier than the floor) and
	// stays bounded (never balloons past the floor plus its ceiling).
	backoffJitter = func(n int64) int64 { return n - 1 }
	floor := now.Add(RecheckBackoff)
	ceiling := floor.Add(RecheckBackoff / 4)
	got := NextRecheckAfterFailures(now, 1)
	if got.Before(floor) || got.After(ceiling) {
		t.Fatalf("jittered NextRecheckAfterFailures = %v, want within [%v, %v]", got, floor, ceiling)
	}
}

// fakeInstaller drives an Installer with canned check results and counts
// the unattended runs it started.
type fakeInstaller struct {
	mu       sync.Mutex
	checks   int
	installs []string
	info     *UpdateInfo
	err      error
	enabled  bool
	instErr  error
	tick     chan time.Time
	inst     *Installer
}

func newFakeInstaller(t *testing.T, info *UpdateInfo) *fakeInstaller {
	t.Helper()
	f := &fakeInstaller{info: info, enabled: true, tick: make(chan time.Time)}
	f.inst = &Installer{
		Exe:            "/bin/agent-deck",
		RunningVersion: "1.16.7",
		Trigger:        "web",
		Enabled:        func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.enabled },
		Check: func() (*UpdateInfo, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.checks++
			return f.info, f.err
		},
		Install: func(_ context.Context, exe, trigger string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.installs = append(f.installs, exe+" "+trigger)
			return "ok", f.instErr
		},
		Pending: func() bool { return false },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		ticks:   f.tick,
		handled: make(chan struct{}),
	}
	return f
}

// step delivers one tick and waits for the installer to finish handling it.
func (f *fakeInstaller) step(t *testing.T, at time.Time) {
	t.Helper()
	select {
	case f.tick <- at:
	case <-time.After(5 * time.Second):
		t.Fatal("installer did not take the tick")
	}
	select {
	case <-f.inst.handled:
	case <-time.After(5 * time.Second):
		t.Fatal("installer did not handle the tick")
	}
}

func (f *fakeInstaller) counts() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, append([]string(nil), f.installs...)
}

// TestInstaller_InstallsOncePerVersion pins the headless auto-install loop
// (issue: a `web --no-tui` daemon only ever watched the binary; nothing in
// the process installed a release that landed while it ran): a tick that
// sees an installable release runs `<exe> update --unattended --trigger
// <trigger>` once, the same version is not retried within
// InstallRetryAfter, and a newer version is.
func TestInstaller_InstallsOncePerVersion(t *testing.T) {
	f := newFakeInstaller(t, &UpdateInfo{Available: true, CurrentVersion: "1.16.7", LatestVersion: "1.16.8"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { f.inst.Run(ctx); close(done) }()

	now := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	f.step(t, now)
	if checks, installs := f.counts(); checks != 1 || len(installs) != 1 || installs[0] != "/bin/agent-deck web" {
		t.Fatalf("after first tick: checks=%d installs=%v, want one check and one run of the fingerprinted exe with trigger web", checks, installs)
	}
	// Same version, one minute later: checked again (cheap), not installed again.
	f.step(t, now.Add(RecheckInterval))
	if checks, installs := f.counts(); checks != 2 || len(installs) != 1 {
		t.Fatalf("same version within the retry window: checks=%d installs=%v", checks, installs)
	}
	// A newer version is a fresh attempt.
	f.mu.Lock()
	f.info = &UpdateInfo{Available: true, CurrentVersion: "1.16.7", LatestVersion: "1.16.9"}
	f.mu.Unlock()
	f.step(t, now.Add(2*RecheckInterval))
	if _, installs := f.counts(); len(installs) != 2 {
		t.Fatalf("newer version: installs=%v, want a second run", installs)
	}
	// After the retry window the failed/attempted version may be tried again.
	f.step(t, now.Add(2*RecheckInterval+InstallRetryAfter))
	if _, installs := f.counts(); len(installs) != 3 {
		t.Fatalf("after the retry window: installs=%v, want a third run", installs)
	}
	cancel()
	<-done
}

// TestInstaller_SkipConditions pins every reason a tick leaves the
// updater alone: nothing available, a release still publishing, the
// setting off, and a failed check (which also backs off the next ask).
func TestInstaller_SkipConditions(t *testing.T) {
	cases := map[string]func(f *fakeInstaller){
		"no update available": func(f *fakeInstaller) {
			f.info = &UpdateInfo{Available: false, CurrentVersion: "1.16.7", LatestVersion: "1.16.7"}
		},
		"release still publishing": func(f *fakeInstaller) {
			f.info = &UpdateInfo{Available: true, CurrentVersion: "1.16.7", LatestVersion: "1.16.7", PublishingVersion: "1.16.8"}
		},
		"auto_install off": func(f *fakeInstaller) { f.enabled = false },
		"check failed":     func(f *fakeInstaller) { f.info, f.err = nil, errors.New("rate limited") },
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			// Pin jitter to zero so the backoff boundary below is exact;
			// NextRecheckAfterFailures otherwise adds up to 25% jitter.
			origJitter := backoffJitter
			backoffJitter = func(int64) int64 { return 0 }
			t.Cleanup(func() { backoffJitter = origJitter })

			f := newFakeInstaller(t, &UpdateInfo{Available: true, CurrentVersion: "1.16.7", LatestVersion: "1.16.8"})
			arrange(f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go f.inst.Run(ctx)
			now := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
			f.step(t, now)
			if _, installs := f.counts(); len(installs) != 0 {
				t.Fatalf("%s: installs=%v, want none", name, installs)
			}
			if name == "check failed" {
				// Within the backoff the next tick does not even ask.
				f.step(t, now.Add(RecheckInterval))
				if checks, _ := f.counts(); checks != 1 {
					t.Fatalf("failed check: checks=%d, want the backoff to hold the next ask", checks)
				}
				f.step(t, now.Add(RecheckBackoff))
				if checks, _ := f.counts(); checks != 2 {
					t.Fatalf("after the backoff: checks=%d, want a fresh ask", checks)
				}
			}
		})
	}
}

// TestInstaller_NoExeIsInert pins that an Installer without an executable
// path returns at once instead of ticking forever.
func TestInstaller_NoExeIsInert(t *testing.T) {
	done := make(chan struct{})
	go func() { (&Installer{}).Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Installer with no Exe must return immediately")
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &TailBuffer{Max: 8}
	for _, chunk := range []string{"abcdef", "ghij", "kl"} {
		if n, err := tb.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got := tb.String(); got != "efghijkl" {
		t.Fatalf("tail = %q, want the last 8 bytes", got)
	}
}

// TestInstaller_DrainsPendingLaunchAgentsWhenCurrent pins the daemon's
// retry of a pending launch agent (a bootstrap that failed after bootout,
// or an agent deferred by a run inside it): with nothing to install and
// the marker set, a tick runs the unattended updater (which drains what it
// can from inside the daemon's own service), once per InstallRetryAfter.
func TestInstaller_DrainsPendingLaunchAgentsWhenCurrent(t *testing.T) {
	f := newFakeInstaller(t, &UpdateInfo{Available: false, CurrentVersion: "1.16.7", LatestVersion: "1.16.7"})
	pending := true
	f.inst.Pending = func() bool { f.mu.Lock(); defer f.mu.Unlock(); return pending }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.inst.Run(ctx)

	now := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	f.step(t, now)
	if _, installs := f.counts(); len(installs) != 1 || installs[0] != "/bin/agent-deck web" {
		t.Fatalf("pending agent: installs=%v, want one unattended run", installs)
	}
	f.step(t, now.Add(RecheckInterval))
	if _, installs := f.counts(); len(installs) != 1 {
		t.Fatalf("inside the retry window: installs=%v", installs)
	}
	f.step(t, now.Add(InstallRetryAfter))
	if _, installs := f.counts(); len(installs) != 2 {
		t.Fatalf("after the retry window: installs=%v, want a second run", installs)
	}
	f.mu.Lock()
	pending = false
	f.mu.Unlock()
	f.step(t, now.Add(2*InstallRetryAfter))
	if _, installs := f.counts(); len(installs) != 2 {
		t.Fatalf("marker cleared: installs=%v, want no further run", installs)
	}
	cancel()
}
