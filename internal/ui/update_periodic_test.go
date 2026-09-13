package ui

import (
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// stubUpdateCheck replaces the update check seam for one test and counts
// the calls.
func stubUpdateCheck(t *testing.T, info *update.UpdateInfo, err error) *int {
	t.Helper()
	calls := new(int)
	prev := checkUpdate
	checkUpdate = func(string, bool) (*update.UpdateInfo, error) {
		*calls++
		return info, err
	}
	t.Cleanup(func() { checkUpdate = prev })
	return calls
}

// TestPeriodicUpdateCheck_RunsWithoutKnownUpdate is the regression test
// for the TUI never noticing a release that lands while it is open: the
// tick loop only re-checked while a banner was already showing
// (`h.updateInfo != nil && h.updateInfo.Available`), so a deck opened
// before a release never asked again and auto_install never had a result
// to act on. A tick with no known update and a stale last check must
// re-ask.
func TestPeriodicUpdateCheck_RunsWithoutKnownUpdate(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	h := newRestartTestHome(t)
	h.updateInfo = nil
	h.lastUpdateCheck = time.Now().Add(-2 * update.RecheckInterval)

	before := h.lastUpdateCheck
	if cmd := h.periodicUpdateCheck(time.Now()); cmd == nil {
		t.Fatal("tick with no known update and a stale last check must re-check")
	}
	if !h.lastUpdateCheck.After(before) {
		t.Fatal("re-check must stamp lastUpdateCheck so the next tick waits its turn")
	}
	if cmd := h.periodicUpdateCheck(time.Now()); cmd != nil {
		t.Fatal("a second tick right after must not re-check again")
	}
}

// TestPeriodicUpdateCheck_Cadence pins the schedule: due once per
// update.RecheckInterval (a cache read; the hourly network fetch is the
// cache's business), held back for update.RecheckBackoff after a failed
// check, never while a check is in flight, and off with check_enabled =
// false.
func TestPeriodicUpdateCheck_Cadence(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	h := newRestartTestHome(t)
	now := time.Now()

	h.lastUpdateCheck = now.Add(-update.RecheckInterval / 2)
	if cmd := h.periodicUpdateCheck(now); cmd != nil {
		t.Fatal("half an interval after a check: not due yet")
	}
	h.lastUpdateCheck = now.Add(-update.RecheckInterval)
	h.lastUpdateCheckFailed = true
	if cmd := h.periodicUpdateCheck(now); cmd != nil {
		t.Fatal("after a failed check the backoff must hold")
	}
	h.lastUpdateCheck = now.Add(-update.RecheckBackoff)
	if cmd := h.periodicUpdateCheck(now); cmd == nil {
		t.Fatal("after the backoff the check is due again")
	}

	h.lastUpdateCheck = now.Add(-2 * update.RecheckInterval)
	h.lastUpdateCheckFailed = false
	h.updateCheckInFlight = true
	if cmd := h.periodicUpdateCheck(now); cmd != nil {
		t.Fatal("must not start a second check while one is in flight")
	}
	h.updateCheckInFlight = false

	stubUpdateSettings(t, session.UpdateSettings{CheckEnabled: boolPtr(false)})
	if cmd := h.periodicUpdateCheck(now); cmd != nil {
		t.Fatal("check_enabled = false must switch the periodic check off")
	}
}

// TestUpdateCheckMsg_RecordsOutcome pins what the check result leaves
// behind for the scheduler: a failed check marks the backoff, a good one
// clears it, and both clear the in-flight flag.
func TestUpdateCheckMsg_RecordsOutcome(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	h := newRestartTestHome(t)
	h.updateCheckInFlight = true
	h.Update(updateCheckMsg{info: &update.UpdateInfo{CurrentVersion: "1.16.0"}, err: errors.New("rate limited")})
	if h.updateCheckInFlight || !h.lastUpdateCheckFailed {
		t.Fatalf("after a failed check: inFlight=%v failed=%v", h.updateCheckInFlight, h.lastUpdateCheckFailed)
	}
	h.updateCheckInFlight = true
	h.Update(updateCheckMsg{info: &update.UpdateInfo{CurrentVersion: "1.16.0", LatestVersion: "1.16.0"}})
	if h.updateCheckInFlight || h.lastUpdateCheckFailed {
		t.Fatalf("after a good check: inFlight=%v failed=%v", h.updateCheckInFlight, h.lastUpdateCheckFailed)
	}
}

// TestPeriodicTick_InstallsAndReExecsWithoutKeypress walks the whole
// unattended chain an open, idle deck must complete on its own: the
// periodic check finds a release, auto_install runs the updater, the
// binary watch sees the new file, and auto_restart arms the in-place
// re-exec with the same executable, all without a key press and without
// a footer error.
func TestPeriodicTick_InstallsAndReExecsWithoutKeypress(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	t.Setenv(update.SkipUpdateCheckEnv, "")
	checks := stubUpdateCheck(t, &update.UpdateInfo{Available: true, CurrentVersion: "1.16.0", LatestVersion: "1.16.1"}, nil)
	f := &fakeUpdater{output: "✓ Updated to v1.16.1 (unattended)\n"}
	prev := runUnattendedUpdate
	runUnattendedUpdate = f.run
	t.Cleanup(func() { runUnattendedUpdate = prev })

	h := newRestartTestHome(t) // running 1.16.0 from /bin/agent-deck, no update known
	h.lastUpdateCheck = time.Now().Add(-2 * update.RecheckInterval)

	// 1. Tick: the periodic check is due and asks.
	checkCmd := h.periodicUpdateCheck(time.Now())
	if checkCmd == nil {
		t.Fatal("periodic check must run")
	}
	msg, ok := checkCmd().(updateCheckMsg)
	if !ok || *checks != 1 || msg.info == nil || !msg.info.Available {
		t.Fatalf("check delivered %T (calls=%d), want an available updateCheckMsg", msg, *checks)
	}

	// 2. The result starts the unattended install on its own.
	_, installCmd := h.Update(msg)
	if installCmd == nil || h.autoInstallInFlight != "1.16.1" {
		t.Fatalf("install cmd=%v inFlight=%q, want the updater started for 1.16.1", installCmd, h.autoInstallInFlight)
	}
	finished, ok := installCmd().(unattendedInstallFinishedMsg)
	if !ok || len(f.exes) != 1 || f.exes[0] != "/bin/agent-deck" {
		t.Fatalf("updater ran with %v, want the fingerprinted exe once", f.exes)
	}
	if _, cmd := h.Update(finished); cmd == nil || h.autoInstallInFlight != "" || h.err != nil {
		t.Fatalf("after install: cmd=%v inFlight=%q err=%v", cmd, h.autoInstallInFlight, h.err)
	}

	// 3. The binary watch sees the replaced file and probes it.
	replaced := fpAt(2, 2)
	if !h.binaryWatch.observe(replaced) {
		t.Fatal("changed fingerprint must request a probe")
	}
	_, restartCmd := h.Update(binaryVersionProbedMsg{fingerprint: replaced, version: "1.16.1"})

	// 4. auto_restart arms the in-place re-exec of the same executable.
	if restartCmd == nil || !h.restartRequested || !h.isQuitting {
		t.Fatalf("restart cmd=%v requested=%v quitting=%v, want the re-exec armed", restartCmd, h.restartRequested, h.isQuitting)
	}
	if exe, ok := h.RestartTarget(); !ok || exe != "/bin/agent-deck" {
		t.Fatalf("RestartTarget = %q, %v; want the updated executable", exe, ok)
	}
	if h.err != nil {
		t.Fatalf("the unattended chain must not leave a footer error, got %v", h.err)
	}
}
