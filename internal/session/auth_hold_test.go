package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newAuthHoldTestInstance builds a bare instance with a unique id so each test's
// sidecar is independent. TestMain isolates HOME+XDG, so the sidecar lands in a
// temp dir, never the real ~/.agent-deck.
func newAuthHoldTestInstance(t *testing.T, id string) *Instance {
	t.Helper()
	inst := &Instance{ID: id, Title: id, Tool: "claude", Status: StatusError}
	t.Cleanup(func() { clearAuthHoldRecord(inst.ID) })
	return inst
}

// TestAuthHold_RoundTrip asserts the durable sidecar is the cross-process source
// of truth: the TUI observes the failure, a fresh CLI process must see the hold.
func TestAuthHold_RoundTrip(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-roundtrip")

	if inst.AuthHold() != nil {
		t.Fatal("a fresh instance must not be held")
	}

	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401 · Please run /login")

	// A DIFFERENT Instance value for the same id models the fresh CLI process.
	other := &Instance{ID: inst.ID, Title: inst.Title, Status: StatusError}
	rec := other.AuthHold()
	if rec == nil {
		t.Fatal("the hold must be visible to another process reading the same id")
	}
	if rec.Reason != AuthHoldReasonDeath {
		t.Fatalf("reason = %q, want %q", rec.Reason, AuthHoldReasonDeath)
	}
	if !strings.Contains(rec.Evidence, "401") {
		t.Fatalf("evidence must be retained, got %q", rec.Evidence)
	}
}

// TestIsAuthHeld_OnlyWhileUnhealthy asserts a lingering sidecar can never strand
// a session that is demonstrably working. Health is the proof the credential is
// good, and it outranks any record on disk.
func TestIsAuthHeld_OnlyWhileUnhealthy(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-healthy-wins")
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	if held, _ := inst.IsAuthHeld(); !held {
		t.Fatal("an errored session with a hold record must be held")
	}

	for _, healthy := range []Status{StatusRunning, StatusWaiting, StatusIdle, StatusStarting} {
		inst.Status = healthy
		if held, _ := inst.IsAuthHeld(); held {
			t.Fatalf("status %q must not be held", healthy)
		}
	}
}

// TestIsAuthHeld_CoversCleanExitAuthDeath asserts a STOPPED session with a hold
// is still held. An agent that exits on a 401 can exit cleanly (exit 0), which
// the exit-code classifier reads as stopped (■) rather than error — the silent
// decay shape of the fleet death. A deliberate user stop is not affected: Kill
// releases the hold, so only an unexplained exit-0 keeps one.
func TestIsAuthHeld_CoversCleanExitAuthDeath(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-clean-exit")
	inst.Status = StatusStopped
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401 · Please run /login")

	held, remedy := inst.IsAuthHeld()
	if !held {
		t.Fatal("an exit-0 auth death (status stopped) must still be held")
	}
	if !strings.Contains(remedy, "/login") {
		t.Fatalf("remedy must name /login, got %q", remedy)
	}
}

// TestIsAuthHeld_RemedyNamesTheAction is the whole point of the feature: the
// 2026-07-26 outage dragged on because no surface said what to do.
func TestIsAuthHeld_RemedyNamesTheAction(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-remedy")
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	held, remedy := inst.IsAuthHeld()
	if !held {
		t.Fatal("expected held")
	}
	if !strings.Contains(remedy, "/login") {
		t.Fatalf("remedy must name /login, got %q", remedy)
	}
	if !strings.Contains(strings.ToLower(remedy), "press r") {
		t.Fatalf("remedy must name the manual escape hatch, got %q", remedy)
	}
}

// TestClearAuthHold_ReleasesBothLayers asserts release wipes the sidecar AND the
// in-memory mirror, so neither a stale file nor a stale flag keeps a recovered
// session pinned.
func TestClearAuthHold_ReleasesBothLayers(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-clear")
	inst.noteAuthHoldLocked(AuthHoldReasonLive, "Please run /login")
	inst.authHeld = true

	inst.clearAuthHoldLocked()

	if inst.AuthHold() != nil {
		t.Fatal("the sidecar must be removed")
	}
	if inst.authHeld {
		t.Fatal("the in-memory mirror must be cleared")
	}
	if !inst.authFailureSeenAt.IsZero() {
		t.Fatal("the observation timestamp must be cleared")
	}
}

// TestClearAuthHold_SweepsStaleSidecarOnce asserts the first healthy sample in a
// process removes a sidecar written by a PREVIOUS process, and that later samples
// are free. Without the first unconditional sweep, a stale record would make a
// later ordinary crash look like an auth death and hold a restartable session.
func TestClearAuthHold_SweepsStaleSidecarOnce(t *testing.T) {
	writer := newAuthHoldTestInstance(t, "auth-hold-stale-sweep")
	writer.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	// Fresh process view: no in-memory flags, but the sidecar is on disk.
	reader := &Instance{ID: writer.ID, Title: writer.Title, Status: StatusRunning}
	reader.clearAuthHoldLocked()

	if reader.AuthHold() != nil {
		t.Fatal("the first healthy sample must sweep a sidecar left by another process")
	}
	if !reader.authHoldClearedOnce {
		t.Fatal("the one-shot sweep must be recorded so later samples are free")
	}

	// Second call is a no-op and must not create state.
	reader.clearAuthHoldLocked()
	if reader.AuthHold() != nil {
		t.Fatal("clearing an unheld session must never create state")
	}
}

// TestNoteAuthHold_IdempotentForSameReason asserts a wedged session observed once
// a second does not thrash the filesystem: the record is only rewritten when the
// reason actually changes.
func TestNoteAuthHold_IdempotentForSameReason(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-idempotent")
	inst.noteAuthHoldLocked(AuthHoldReasonLive, "first evidence")
	first := inst.AuthHold()
	if first == nil {
		t.Fatal("expected a record")
	}

	inst.noteAuthHoldLocked(AuthHoldReasonLive, "second evidence")
	second := inst.AuthHold()
	if second.Evidence != "first evidence" {
		t.Fatalf("same-reason re-observation must not rewrite the record, evidence = %q", second.Evidence)
	}

	// A reason CHANGE (live banner → the process exited) must be recorded: it is
	// a real transition the death path depends on.
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "dying output")
	promoted := inst.AuthHold()
	if promoted.Reason != AuthHoldReasonDeath {
		t.Fatalf("promotion to death must be recorded, got %q", promoted.Reason)
	}
	if promoted.Evidence != "dying output" {
		t.Fatalf("promotion must carry the new evidence, got %q", promoted.Evidence)
	}
}

// TestAuthDeathObserved_RequiresEvidence asserts an ordinary crash is NOT
// attributed to auth. Over-attributing would hold sessions a restart would fix.
func TestAuthDeathObserved_RequiresEvidence(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-needs-evidence")

	if inst.authDeathObservedLocked() {
		t.Fatal("a death with no auth evidence must not be attributed to auth")
	}
	if inst.AuthHold() != nil {
		t.Fatal("no hold may be armed without evidence")
	}
}

// TestAuthDeathObserved_UsesRecentLiveObservation asserts the freshness window:
// a credential failure seen moments before the death justifies the attribution,
// but a stale one does not.
func TestAuthDeathObserved_UsesRecentLiveObservation(t *testing.T) {
	fresh := newAuthHoldTestInstance(t, "auth-hold-fresh-window")
	fresh.authFailureSeenAt = time.Now()
	if !fresh.authDeathObservedLocked() {
		t.Fatal("a death moments after a live auth banner must be attributed to auth")
	}

	stale := newAuthHoldTestInstance(t, "auth-hold-stale-window")
	stale.authFailureSeenAt = time.Now().Add(-2 * authHoldDeathWindow)
	if stale.authDeathObservedLocked() {
		t.Fatal("a death long after a recovered auth failure must NOT be attributed to auth")
	}
}

// TestRefreshAuthHoldOnDeath_AdoptsAnotherProcessRecord asserts a fresh CLI
// process, which has no in-memory history and no pane to read, still honours a
// hold the TUI wrote — and promotes a live-banner hold to a death hold now that
// the pane is confirmed gone.
func TestRefreshAuthHoldOnDeath_AdoptsAnotherProcessRecord(t *testing.T) {
	writer := newAuthHoldTestInstance(t, "auth-hold-adopt")
	writer.noteAuthHoldLocked(AuthHoldReasonLive, "Please run /login")

	reader := &Instance{ID: writer.ID, Title: writer.Title, Status: StatusError}
	reader.refreshAuthHoldOnDeathLocked()

	if !reader.authHeld {
		t.Fatal("the reader must adopt the existing hold")
	}
	rec := reader.AuthHold()
	if rec == nil || rec.Reason != AuthHoldReasonDeath {
		t.Fatalf("a confirmed dead pane must promote the hold to %q, got %+v", AuthHoldReasonDeath, rec)
	}
}

// TestRefreshAuthHoldOnDeath_ThrottlesSidecarReads asserts the death path does
// not stat the filesystem on every status tick for every dead session.
func TestRefreshAuthHoldOnDeath_ThrottlesSidecarReads(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-throttle")

	inst.refreshAuthHoldOnDeathLocked()
	firstCheck := inst.authHoldCheckedAt
	if firstCheck.IsZero() {
		t.Fatal("the first call must record a check timestamp")
	}

	// A record appears immediately after the first check; the throttle must make
	// the very next call a no-op.
	inst.noteAuthHoldLocked(AuthHoldReasonLive, "Please run /login")
	inst.authFailureSeenAt = time.Time{} // isolate the sidecar path from the window path
	inst.refreshAuthHoldOnDeathLocked()
	if inst.authHeld {
		t.Fatal("the throttle must suppress the immediate re-read")
	}

	// Past the throttle window the record is picked up.
	inst.authHoldCheckedAt = time.Now().Add(-2 * authHoldRecheckInterval)
	inst.refreshAuthHoldOnDeathLocked()
	if !inst.authHeld {
		t.Fatal("after the throttle window the hold must be adopted")
	}
}

// TestAuthHoldSurvivedBoot asserts the "beyond machine recovery" signal self-heal
// keys off: a hold that has already eaten an automatic boot must never be
// auto-actioned again.
func TestAuthHoldSurvivedBoot(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-survived-boot")
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	if inst.AuthHoldSurvivedBoot() {
		t.Fatal("a fresh hold has not survived a boot yet")
	}

	inst.RecordAuthBootFailure()

	if !inst.AuthHoldSurvivedBoot() {
		t.Fatal("after a failed automatic boot the hold must report as survived")
	}
	if rec := inst.AuthHold(); rec == nil || rec.BootAttempts != 1 {
		t.Fatalf("boot attempts must be recorded, got %+v", rec)
	}
}

// TestRecordAuthBootFailure_NoopWithoutHold asserts the counter never creates a
// hold out of nothing (it is called from the sweep for every verified boot).
func TestRecordAuthBootFailure_NoopWithoutHold(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-counter-noop")
	inst.RecordAuthBootFailure()
	if inst.AuthHold() != nil {
		t.Fatal("bumping the counter must not fabricate a hold")
	}
}

// TestAuthHoldRecord_FormatForDisplay asserts the human block explains why
// nothing is retrying — otherwise the silence reads as agent-deck being broken.
func TestAuthHoldRecord_FormatForDisplay(t *testing.T) {
	rec := &AuthHoldRecord{
		InstanceID:   "x",
		Reason:       AuthHoldReasonDeath,
		Evidence:     "API Error: 401 · Please run /login",
		BootAttempts: 2,
	}
	out := rec.FormatForDisplay()
	for _, want := range []string{"authentication", "HELD", "/login", "press R", "API Error: 401"} {
		if !strings.Contains(out, want) {
			t.Fatalf("display block must mention %q, got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "2") {
		t.Fatalf("display block must report the boot attempts, got:\n%s", out)
	}
}

// TestAuthHoldRecord_NilSafe keeps the display helpers total — they are called on
// the result of a lookup that legitimately returns nil.
func TestAuthHoldRecord_NilSafe(t *testing.T) {
	var rec *AuthHoldRecord
	if rec.FormatForDisplay() != "" || rec.Remedy() != "" {
		t.Fatal("nil record helpers must return empty strings")
	}
}

// TestReconcileAuthHold_LiveBannerArmsOnlyOnCredentialFailure asserts the
// restart-recoverable/credential distinction survives at the reconcile layer.
// Without a tmux session there is no per-sample verdict, so nothing may be armed.
func TestReconcileAuthHold_NoTmuxSessionArmsNothing(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-reconcile-no-tmux")
	inst.reconcileAuthHoldLocked("error")
	if inst.authHeld || inst.AuthHold() != nil {
		t.Fatal("an error status with no readable sample must not arm a hold")
	}
}

// TestReconcileAuthHold_HealthyReleases asserts a healthy sample releases the
// hold, which is the ONLY automatic release path.
func TestReconcileAuthHold_HealthyReleases(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-reconcile-healthy")
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")
	inst.authHeld = true
	inst.Status = StatusRunning

	inst.reconcileAuthHoldLocked("active")

	if inst.authHeld || inst.AuthHold() != nil {
		t.Fatal("a healthy sample must release the hold")
	}
}

// TestSubstate_AuthHoldOverridesDeadPane is the honest-status half of the fix: a
// dead pane has no substate to classify, and reporting SubstateNone is exactly
// what made a fleet-wide 401 look like anonymous mass death.
func TestSubstate_AuthHoldOverridesDeadPane(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-substate")
	if got := inst.Substate(); got != SubstateNone {
		t.Fatalf("without a hold and without tmux, substate = %q, want none", got)
	}

	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")
	if got := inst.Substate(); got != SubstateAuth401 {
		t.Fatalf("a held session must report %q, got %q", SubstateAuth401, got)
	}

	inst.authHeld = true
	if got := inst.CachedSubstate(); got != SubstateAuth401 {
		t.Fatalf("the render hot path must report %q, got %q", SubstateAuth401, got)
	}
}

// TestShouldSkipRestart_HoldsAutomationButNotForce asserts the automation/human
// split: `session restart <id>` (watchdogs, cron, conductors) is gated, and
// --force — the human override — always boots.
func TestShouldSkipRestart_HoldsAutomationButNotForce(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-restart-guard")
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	skip, reason := ShouldSkipRestart(inst, time.Now(), false)
	if !skip {
		t.Fatal("an auth-held session must not be auto-restarted")
	}
	if !strings.Contains(reason, "--force") {
		t.Fatalf("the skip reason must name the override, got %q", reason)
	}

	if skip, _ := ShouldSkipRestart(inst, time.Now(), true); skip {
		t.Fatal("--force must override the auth hold")
	}
}

// TestShouldSkipRestart_AuthHoldBeatsFreshnessGuard asserts ordering: the auth
// hold applies even to a session with no LastStartedAt, which the freshness
// guard alone would wave through.
func TestShouldSkipRestart_AuthHoldBeatsFreshnessGuard(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-beats-freshness")
	inst.LastStartedAt = time.Time{} // freshness guard would permit the restart
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	if skip, _ := ShouldSkipRestart(inst, time.Now(), false); !skip {
		t.Fatal("the auth hold must apply regardless of freshness")
	}
}

// TestSpawnFailurePreview_PrefersAuthHold asserts the auth block is what the user
// reads on a pane-less session: "run /login" must not be buried under a generic
// spawn-failure dump.
func TestSpawnFailurePreview_PrefersAuthHold(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-preview")
	t.Cleanup(func() { clearSpawnFailureRecord(inst.ID) })

	if err := writeSpawnFailureRecord(SpawnFailureRecord{
		InstanceID:  inst.ID,
		Reason:      "spawn_died_fast",
		DyingOutput: "generic dying output",
	}); err != nil {
		t.Fatalf("write spawn failure: %v", err)
	}
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401 · Please run /login")

	preview := inst.spawnFailurePreview()
	if !strings.Contains(preview, "authentication") {
		t.Fatalf("the auth block must win the preview, got:\n%s", preview)
	}
	if strings.Contains(preview, "generic dying output") {
		t.Fatalf("the generic spawn-failure block must not be shown instead, got:\n%s", preview)
	}
}

// TestWriteAuthHoldRecord_PrivatePerms asserts the sidecar is 0o600 and its
// directory carries no group/other bits. Evidence is a verbatim pane tail taken
// at the moment of a credential failure (agent output, prompt text, error
// bodies), and authHoldDir() falls back to a shared /tmp when the data dir cannot
// be resolved — so the record must never be world-readable.
func TestWriteAuthHoldRecord_PrivatePerms(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-perms")
	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401 · Please run /login")

	path := authHoldRecordPath(inst.ID)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sidecar: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("sidecar perm = %#o, want 0600", got)
	}

	dir := filepath.Dir(path)
	if dir == tempAgentDeckPath("runtime", "auth-hold") {
		// The shared-/tmp fallback dir may pre-exist with foreign perms and
		// MkdirAll does not chmod an existing dir; only the file mode is ours.
		t.Skip("data dir resolution fell back to temp; directory mode is not this call's to assert")
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat auth-hold dir: %v", err)
	}
	if got := di.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("auth-hold dir perm = %#o, want no group/other bits", got)
	}
}

// TestWriteAuthHoldRecord_TightensLegacyPerms asserts a record left behind by an
// older build (0o644) is narrowed the next time it is touched, so upgrading is
// enough to close the exposure on an already-held session.
func TestWriteAuthHoldRecord_TightensLegacyPerms(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-legacy-perms")
	path := authHoldRecordPath(inst.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy, err := json.Marshal(AuthHoldRecord{
		InstanceID: inst.ID,
		Reason:     AuthHoldReasonLive,
		Evidence:   "API Error: 401",
		Timestamp:  time.Now().Add(-2 * authHoldObservationRefresh).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}

	// Re-observing the same condition re-stamps the record, which rewrites it.
	inst.noteAuthHoldLocked(AuthHoldReasonLive, "API Error: 401")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sidecar: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("rewritten sidecar perm = %#o, want 0600", got)
	}
}

// TestNoteAuthHold_RestampsStillObservedRecord asserts a session wedged on the
// banner for a long time keeps its Timestamp current. The death path reads
// Timestamp as "last seen", so without the re-stamp an hour-long wedge would die
// outside authHoldDeathWindow and read as an unrelated crash. The re-stamp must
// touch nothing else: the first sample is the cleanest evidence and the boot
// counter is cumulative.
func TestNoteAuthHold_RestampsStillObservedRecord(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-restamp")
	stale := time.Now().Add(-2 * authHoldObservationRefresh)
	if err := writeAuthHoldRecord(AuthHoldRecord{
		InstanceID:   inst.ID,
		Reason:       AuthHoldReasonLive,
		Evidence:     "first evidence",
		Timestamp:    stale.Unix(),
		BootAttempts: 3,
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	inst.noteAuthHoldLocked(AuthHoldReasonLive, "later evidence")

	rec := inst.AuthHold()
	if rec == nil {
		t.Fatal("expected the record to survive")
	}
	if rec.Timestamp <= stale.Unix() {
		t.Fatalf("timestamp must be re-stamped, got %d (seeded %d)", rec.Timestamp, stale.Unix())
	}
	if rec.Evidence != "first evidence" {
		t.Fatalf("the re-stamp must keep the original evidence, got %q", rec.Evidence)
	}
	if rec.BootAttempts != 3 {
		t.Fatalf("the re-stamp must preserve the boot counter, got %d", rec.BootAttempts)
	}
	if rec.Reason != AuthHoldReasonLive {
		t.Fatalf("the re-stamp must keep the reason, got %q", rec.Reason)
	}
}

// TestRefreshAuthHoldOnDeath_DiscardsStaleLiveRecord is the staleness half of the
// hardening. A live-banner sidecar written hours ago by a process that then died
// — before it could observe the recovery that cleared the 401 — must NOT make a
// later, unrelated crash look like an auth death. The stale record is unlinked,
// not merely ignored: IsAuthHeld is the cross-process CLI gate and reads the
// sidecar directly, so leaving it would strand a session a plain restart fixes.
func TestRefreshAuthHoldOnDeath_DiscardsStaleLiveRecord(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-stale-live")
	if err := writeAuthHoldRecord(AuthHoldRecord{
		InstanceID: inst.ID,
		Reason:     AuthHoldReasonLive,
		Evidence:   "API Error: 401",
		Timestamp:  time.Now().Add(-2 * authHoldDeathWindow).Unix(),
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	reader := &Instance{ID: inst.ID, Title: inst.Title, Status: StatusError}
	reader.refreshAuthHoldOnDeathLocked()

	if reader.authHeld {
		t.Fatal("a stale live-banner record must not attribute this death to auth")
	}
	if rec := reader.AuthHold(); rec != nil {
		t.Fatalf("the stale record must be unlinked, still on disk: %+v", rec)
	}
	if held, _ := reader.IsAuthHeld(); held {
		t.Fatal("the CLI gate must no longer hold the session")
	}
}

// TestRefreshAuthHoldOnDeath_AdoptsRecentLiveRecord pins the other side of the
// window: a banner seen moments before the pane vanished is exactly the evidence
// the hold exists for, and must still be adopted and promoted.
func TestRefreshAuthHoldOnDeath_AdoptsRecentLiveRecord(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-recent-live")
	if err := writeAuthHoldRecord(AuthHoldRecord{
		InstanceID: inst.ID,
		Reason:     AuthHoldReasonLive,
		Evidence:   "API Error: 401",
		Timestamp:  time.Now().Add(-authHoldDeathWindow / 2).Unix(),
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	reader := &Instance{ID: inst.ID, Title: inst.Title, Status: StatusError}
	reader.refreshAuthHoldOnDeathLocked()

	if !reader.authHeld {
		t.Fatal("a recent live-banner record must still be adopted on death")
	}
	rec := reader.AuthHold()
	if rec == nil || rec.Reason != AuthHoldReasonDeath {
		t.Fatalf("the hold must be promoted to %q, got %+v", AuthHoldReasonDeath, rec)
	}
}

// TestRefreshAuthHoldOnDeath_KeepsOldDeathRecord asserts the staleness check does
// NOT turn the hold into a timer. A death record is the post-mortem: the process
// already exited on the banner, and only observing the session healthy (or the
// user stopping it) may release that verdict — never the passage of time.
func TestRefreshAuthHoldOnDeath_KeepsOldDeathRecord(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-old-death")
	if err := writeAuthHoldRecord(AuthHoldRecord{
		InstanceID: inst.ID,
		Reason:     AuthHoldReasonDeath,
		Evidence:   "API Error: 401",
		Timestamp:  time.Now().Add(-72 * time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	reader := &Instance{ID: inst.ID, Title: inst.Title, Status: StatusError}
	reader.refreshAuthHoldOnDeathLocked()

	if !reader.authHeld {
		t.Fatal("an aged death record must still hold: nothing has proven the credential works")
	}
	if reader.AuthHold() == nil {
		t.Fatal("an aged death record must not be discarded by age")
	}
}

// TestAuthHoldRecord_CanExplainDeathAt tabulates the freshness rule the death
// path depends on, including the corrupt-record case: a record that cannot prove
// when it was observed cannot justify a hold, because over-holding has no escape
// hatch on the automatic paths.
func TestAuthHoldRecord_CanExplainDeathAt(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		rec  *AuthHoldRecord
		want bool
	}{
		{"nil", nil, false},
		{"death always explains", &AuthHoldRecord{Reason: AuthHoldReasonDeath, Timestamp: now.Add(-72 * time.Hour).Unix()}, true},
		{"death without timestamp", &AuthHoldRecord{Reason: AuthHoldReasonDeath}, true},
		{"fresh live", &AuthHoldRecord{Reason: AuthHoldReasonLive, Timestamp: now.Unix()}, true},
		{"live inside window", &AuthHoldRecord{Reason: AuthHoldReasonLive, Timestamp: now.Add(-authHoldDeathWindow / 2).Unix()}, true},
		{"live outside window", &AuthHoldRecord{Reason: AuthHoldReasonLive, Timestamp: now.Add(-2 * authHoldDeathWindow).Unix()}, false},
		{"live without timestamp", &AuthHoldRecord{Reason: AuthHoldReasonLive}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.canExplainDeathAt(now); got != tc.want {
				t.Fatalf("canExplainDeathAt = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNoteAuthHold_PromotionPreservesBootAttempts asserts the live → death
// promotion carries the cumulative boot counter. AuthHoldSurvivedBoot gates
// self-heal on it, so resetting the counter would re-advertise as machine-
// recoverable a session whose automatic boot already died on this credential.
func TestNoteAuthHold_PromotionPreservesBootAttempts(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-promotion-counter")
	inst.noteAuthHoldLocked(AuthHoldReasonLive, "API Error: 401")
	inst.RecordAuthBootFailure()
	inst.RecordAuthBootFailure()

	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "dying output")

	rec := inst.AuthHold()
	if rec == nil || rec.Reason != AuthHoldReasonDeath {
		t.Fatalf("promotion must be recorded, got %+v", rec)
	}
	if rec.BootAttempts != 2 {
		t.Fatalf("boot attempts must survive the promotion, got %d", rec.BootAttempts)
	}
	if rec.Evidence != "dying output" {
		t.Fatalf("promotion must carry the new evidence, got %q", rec.Evidence)
	}
	if !inst.AuthHoldSurvivedBoot() {
		t.Fatal("a promoted hold that already ate a boot must still report as survived")
	}
}

// TestWriteAuthHoldRecord_NarrowsLegacyDirPerms asserts an install upgraded from
// the 0o755 era gets its auth-hold directory narrowed on the next write —
// MkdirAll alone never touches an existing directory.
func TestWriteAuthHoldRecord_NarrowsLegacyDirPerms(t *testing.T) {
	inst := newAuthHoldTestInstance(t, "auth-hold-legacy-dir")
	dir := authHoldDir()
	if dir == tempAgentDeckPath("runtime", "auth-hold") {
		t.Skip("data dir resolution fell back to temp; the directory is not this call's to chmod")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("seed legacy dir mode: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	inst.noteAuthHoldLocked(AuthHoldReasonDeath, "API Error: 401")

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat auth-hold dir: %v", err)
	}
	if got := di.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("legacy dir perm = %#o, want narrowed to no group/other bits", got)
	}
}
