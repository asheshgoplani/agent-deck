package send

import (
	"errors"
	"testing"
	"time"
)

// fakeSettleTarget scripts pane captures for the settle gate. captures[i] is
// returned by the i-th CapturePaneFresh call; the last entry repeats once
// exhausted, so a script ending in a rendered composer means "ready from here
// on". status is what GetStatus reports (unused by the gate, present because
// AgentReadyChecker requires it).
type fakeSettleTarget struct {
	captures     []string
	captureErr   error
	status       string
	captureCalls int
}

func (f *fakeSettleTarget) CapturePaneFresh() (string, error) {
	i := f.captureCalls
	f.captureCalls++
	if f.captureErr != nil {
		return "", f.captureErr
	}
	if len(f.captures) == 0 {
		return "", nil
	}
	if i >= len(f.captures) {
		i = len(f.captures) - 1
	}
	return f.captures[i], nil
}

func (f *fakeSettleTarget) GetStatus() (string, error) {
	if f.status == "" {
		return "waiting", nil
	}
	return f.status, nil
}

func claudeGates() PromptGates { return PromptGates{ClaudeComposer: true} }

// fastSettle is the production shape with the clock compressed so tests run in
// milliseconds. MinAge is left to each test.
func fastSettle(minAge time.Duration, timeout time.Duration) SpawnSettleOptions {
	return SpawnSettleOptions{
		MinAge:    minAge,
		StableFor: 20 * time.Millisecond,
		Poll:      2 * time.Millisecond,
		Timeout:   timeout,
	}
}

func TestSpawnSettleDue(t *testing.T) {
	if SpawnSettleDue(time.Time{}, time.Minute) {
		t.Fatal("an unknown spawn time must not engage the gate")
	}
	if !SpawnSettleDue(time.Now().Add(-2*time.Second), time.Minute) {
		t.Fatal("a spawn 2s ago is inside the window")
	}
	if SpawnSettleDue(time.Now().Add(-5*time.Minute), time.Minute) {
		t.Fatal("a spawn 5m ago is outside the window")
	}
	// Clock skew between the restarting process and this one must not read as
	// "long settled".
	if !SpawnSettleDue(time.Now().Add(time.Second), time.Minute) {
		t.Fatal("a stamp in the near future still means just-spawned")
	}
}

// The regression this gate exists for: `session send` moments after
// `session restart`. The composer is already on screen (Claude paints it before
// its input handler mounts), so every pane-based signal says "ready" — but the
// spawn is younger than MinAge, so delivery must still be held back.
func TestWaitForSpawnSettle_HoldsUntilSpawnIsOldEnough(t *testing.T) {
	target := &fakeSettleTarget{captures: []string{renderComposer("")}}
	const minAge = 120 * time.Millisecond
	spawnedAt := time.Now()

	start := time.Now()
	if err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(minAge, 5*time.Second), time.Sleep); err != nil {
		t.Fatalf("expected the gate to clear, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < minAge {
		t.Fatalf("gate returned after %s, before the %s post-spawn floor", elapsed, minAge)
	}
}

// A spawn already older than MinAge with a stable composer clears quickly:
// steady-state sends must not pay the cold-start budget.
func TestWaitForSpawnSettle_ClearsImmediatelyWhenAlreadySettled(t *testing.T) {
	target := &fakeSettleTarget{captures: []string{renderComposer("")}}
	spawnedAt := time.Now().Add(-10 * time.Second)

	start := time.Now()
	if err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(5*time.Second, 5*time.Second), time.Sleep); err != nil {
		t.Fatalf("expected the gate to clear, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("an already-settled session waited %s", elapsed)
	}
}

// A pane still repainting (no composer yet) must hold delivery even though the
// spawn is old enough — this is the half-mounted TUI that swallows keystrokes.
func TestWaitForSpawnSettle_HoldsWhileComposerIsAbsent(t *testing.T) {
	target := &fakeSettleTarget{captures: []string{
		"booting\n", "booting\n", "loading conversation…\n",
		renderComposer(""), // composer finally paints
	}}
	spawnedAt := time.Now().Add(-time.Minute)

	if err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(time.Millisecond, 5*time.Second), time.Sleep); err != nil {
		t.Fatalf("expected the gate to clear once the composer painted, got %v", err)
	}
	if target.captureCalls < 4 {
		t.Fatalf("expected the gate to keep probing past the boot frames, got %d captures", target.captureCalls)
	}
}

// A composer that appears and vanishes again (mid-replay repaint) does not
// satisfy the stability requirement on its first sighting.
func TestWaitForSpawnSettle_RequiresContinuousVisibility(t *testing.T) {
	target := &fakeSettleTarget{captures: []string{
		renderComposer(""), "repainting\n", renderComposer(""),
	}}
	spawnedAt := time.Now().Add(-time.Minute)

	if err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(time.Millisecond, 5*time.Second), time.Sleep); err != nil {
		t.Fatalf("expected the gate to clear after the repaint, got %v", err)
	}
	// Frames 1 and 2 cannot both count toward stability: the interruption at
	// frame 2 resets the streak, so the gate must have read past it.
	if target.captureCalls < 3 {
		t.Fatalf("expected the streak to reset on the repaint, got %d captures", target.captureCalls)
	}
}

// A pane that never shows a prompt fails the gate rather than hanging. The
// caller treats that as advisory and sends anyway, so the error must arrive
// bounded by Timeout.
func TestWaitForSpawnSettle_TimesOut(t *testing.T) {
	target := &fakeSettleTarget{captures: []string{"no prompt ever\n"}}
	spawnedAt := time.Now().Add(-time.Minute)

	start := time.Now()
	err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(time.Millisecond, 80*time.Millisecond), time.Sleep)
	if err == nil {
		t.Fatal("expected a timeout error for a pane that never shows a prompt")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout was not bounded: waited %s", elapsed)
	}
}

func TestWaitForSpawnSettle_CaptureErrorsAreNotReadiness(t *testing.T) {
	target := &fakeSettleTarget{captureErr: errors.New("capture-pane: no such pane")}
	spawnedAt := time.Now().Add(-time.Minute)

	if err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(time.Millisecond, 60*time.Millisecond), time.Sleep); err == nil {
		t.Fatal("a target we cannot read must not be reported as settled")
	}
}

// A visibly generating pane ("esc to interrupt") proves a mounted TUI — only a
// mounted one renders that footer — so it satisfies the gate. Otherwise a
// restarted session that picked work straight back up would hold every send for
// the full timeout despite a demonstrably live UI.
func TestWaitForSpawnSettle_BusyPaneCountsAsMounted(t *testing.T) {
	target := &fakeSettleTarget{captures: []string{"working on it\n  esc to interrupt\n"}}
	spawnedAt := time.Now().Add(-time.Minute)

	if err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(time.Millisecond, 5*time.Second), time.Sleep); err != nil {
		t.Fatalf("a generating agent has a mounted UI, got %v", err)
	}
}

// The MinAge floor still applies to a busy pane: "mounted" is not "was mounted
// long enough for its input handler to be armed".
func TestWaitForSpawnSettle_BusyPaneStillWaitsOutMinAge(t *testing.T) {
	target := &fakeSettleTarget{captures: []string{"working\n  esc to interrupt\n"}}
	const minAge = 100 * time.Millisecond
	spawnedAt := time.Now()

	start := time.Now()
	if err := WaitForSpawnSettle(target, "claude", claudeGates(), spawnedAt,
		fastSettle(minAge, 5*time.Second), time.Sleep); err != nil {
		t.Fatalf("expected the gate to clear, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < minAge {
		t.Fatalf("busy pane skipped the %s post-spawn floor (waited %s)", minAge, elapsed)
	}
}
