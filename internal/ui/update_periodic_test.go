package ui

import (
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
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
	const exe = "/bin/agent-deck"
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
	if !ok || len(f.exes) != 1 || f.exes[0] != exe {
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
	if target, ok := h.RestartTarget(); !ok || target != exe {
		t.Fatalf("RestartTarget = %q, %v; want the updated executable", target, ok)
	}
	if h.err != nil {
		t.Fatalf("the unattended chain must not leave a footer error, got %v", h.err)
	}
}

// TestUpdateCheck_SingleFlight pins that every check the TUI starts on its
// own (periodic tick, after an unattended install, after a key-driven
// install) goes through one gate: none starts while another is in flight,
// and each one stamps lastUpdateCheck so the periodic schedule counts
// from it.
func TestUpdateCheck_SingleFlight(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	stubUpdateCheck(t, &update.UpdateInfo{CurrentVersion: "1.16.0", LatestVersion: "1.16.0"}, nil)
	h := newRestartTestHome(t)

	h.lastUpdateCheck = time.Now().Add(-2 * update.RecheckInterval)
	if cmd := h.periodicUpdateCheck(time.Now()); cmd == nil || !h.updateCheckInFlight {
		t.Fatal("periodic check must start and mark itself in flight")
	}
	// The install-finished handlers must not start a second check now.
	before := h.lastUpdateCheck
	h.Update(unattendedInstallFinishedMsg{version: "1.16.1"})
	h.handleUpdateInstallFinished(updateInstallFinishedMsg{})
	if !h.lastUpdateCheck.Equal(before) {
		t.Fatal("a check in flight must not be restarted or re-stamped by the install handlers")
	}

	// With nothing in flight, the install handlers do check, through the gate.
	h.Update(updateCheckMsg{info: &update.UpdateInfo{CurrentVersion: "1.16.0", LatestVersion: "1.16.0"}})
	h.lastUpdateCheck = time.Now().Add(-2 * update.RecheckInterval)
	h.Update(unattendedInstallFinishedMsg{version: "1.16.1"})
	if !h.updateCheckInFlight || !h.lastUpdateCheck.After(before) {
		t.Fatalf("install-finished check must go through the gate: inFlight=%v", h.updateCheckInFlight)
	}
	if cmd := h.periodicUpdateCheck(time.Now().Add(2 * update.RecheckInterval)); cmd != nil {
		t.Fatal("periodic check must wait while the install-finished check is in flight")
	}
}

// TestPeriodicUpdateCheck_HonoursSuppression pins issue #2251 for the
// periodic check itself: a process that a test, CI job or script drives
// (update.TUIAutoUpdateSuppressed: go test, CI, skip marker, no terminal)
// does not start re-checking on its own.
func TestPeriodicUpdateCheck_HonoursSuppression(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	h := newRestartTestHome(t)
	h.lastUpdateCheck = time.Now().Add(-2 * update.RecheckInterval)
	for _, reason := range []string{"running under go test", "CI=true", "AGENTDECK_TEST_ARGV_LOG set", "stdin is not a terminal"} {
		h.autoUpdateSuppressedReason = reason
		if cmd := h.periodicUpdateCheck(time.Now()); cmd != nil || h.updateCheckInFlight {
			t.Fatalf("%s: periodic check must not run", reason)
		}
	}
	h.autoUpdateSuppressedReason = ""
	if cmd := h.periodicUpdateCheck(time.Now()); cmd == nil {
		t.Fatal("unsuppressed: periodic check runs")
	}
}

// runTickBatch executes the command a tick returned and delivers every
// message it produces of type T back into h (the Bubble Tea runtime would
// do the same); other messages are dropped. Sub-commands that are still
// running after the deadline (the next tick's timer) are left behind.
func runTickBatch[T tea.Msg](t *testing.T, h *Home, cmd tea.Cmd) (found T, ok bool) {
	t.Helper()
	if cmd == nil {
		return found, false
	}
	var cmds []tea.Cmd
	switch m := cmd().(type) {
	case tea.BatchMsg:
		cmds = m
	default:
		if v, is := m.(T); is {
			return v, true
		}
		return found, false
	}
	results := make(chan tea.Msg, len(cmds))
	for _, c := range cmds {
		if c == nil {
			continue
		}
		go func(c tea.Cmd) { results <- c() }(c)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m := <-results:
			if v, is := m.(T); is {
				return v, true
			}
		case <-deadline:
			return found, false
		}
	}
}

// tickHome is a restart-test home prepared so a real tickMsg does no
// unrelated background work (mirrors clockFilterTick).
func tickHome(t *testing.T) *Home {
	t.Helper()
	h := newRestartTestHome(t)
	now := time.Now()
	h.lastLogCheck, h.lastCachePrune = now, now
	h.agentsLoaded, h.agentsLastRefresh = true, now
	h.uiStateSaveTicks = 0
	h.groupTree = session.NewGroupTree(nil)
	h.rebuildFlatItems()
	return h
}

// TestTick_InstallsAndReExecsWithoutKeypress drives the real tick handler
// end to end: a tickMsg schedules the check, its result starts the
// unattended install, the next tick's stat sees the replaced file and
// probes it, and the probe result arms the in-place re-exec of the same
// executable. No key, no footer error. Unlike the unit tests above it goes
// through Update(tickMsg), so removing the tick wiring fails it.
func TestTick_InstallsAndReExecsWithoutKeypress(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	t.Setenv(update.SkipUpdateCheckEnv, "")
	checks := stubUpdateCheck(t, &update.UpdateInfo{Available: true, CurrentVersion: "1.16.0", LatestVersion: "1.16.1"}, nil)
	f := &fakeUpdater{output: "✓ Updated to v1.16.1 (unattended)\n"}
	prevRun := runUnattendedUpdate
	runUnattendedUpdate = f.run
	t.Cleanup(func() { runUnattendedUpdate = prevRun })
	probes := 0
	prevProbe := probeBinaryVersion
	probeBinaryVersion = func(string) (string, error) { probes++; return "1.16.1", nil }
	t.Cleanup(func() { probeBinaryVersion = prevProbe })

	h := tickHome(t)
	h.lastUpdateCheck = time.Now().Add(-2 * update.RecheckInterval)

	// Tick 1: the periodic check is scheduled and answered.
	_, cmd := h.Update(tickMsg(time.Now()))
	checkMsg, ok := runTickBatch[updateCheckMsg](t, h, cmd)
	if !ok || *checks != 1 || !h.updateCheckInFlight {
		t.Fatalf("tick did not schedule the update check (calls=%d inFlight=%v)", *checks, h.updateCheckInFlight)
	}
	// Its result starts the unattended install.
	_, cmd = h.Update(checkMsg)
	finished, ok := runTickBatch[unattendedInstallFinishedMsg](t, h, cmd)
	if !ok || len(f.exes) != 1 || f.exes[0] != "/bin/agent-deck" {
		t.Fatalf("check result did not run the updater (exes=%v)", f.exes)
	}
	h.Update(finished)
	if h.err != nil || h.autoInstallInFlight != "" {
		t.Fatalf("after install: err=%v inFlight=%q", h.err, h.autoInstallInFlight)
	}

	// The installer replaced the file: tick 2's stat sees a new fingerprint.
	stubStatBinary(t, fpAt(2, 2), nil)
	_, cmd = h.Update(tickMsg(time.Now()))
	probed, ok := runTickBatch[binaryVersionProbedMsg](t, h, cmd)
	if !ok || probes != 1 || probed.version != "1.16.1" {
		t.Fatalf("tick did not probe the replaced binary (probes=%d)", probes)
	}
	// The probe result arms the re-exec at once.
	h.Update(probed)
	if !h.restartRequested || !h.isQuitting {
		t.Fatalf("requested=%v quitting=%v, want the re-exec armed by the probe result", h.restartRequested, h.isQuitting)
	}
	if exe, ok := h.RestartTarget(); !ok || exe != "/bin/agent-deck" {
		t.Fatalf("RestartTarget = %q, %v", exe, ok)
	}
	if h.err != nil {
		t.Fatalf("unattended chain left a footer error: %v", h.err)
	}
}
