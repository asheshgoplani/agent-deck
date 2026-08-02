package ui

import (
	"context"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Safety-WIRING tests for the adaptive refresh policy (issue #1753, PR #1765).
//
// refresh_policy_test.go pins the policy functions; these tests pin the two
// call sites in home.go that make the policy safe, by driving the REAL
// backgroundStatusUpdate / refreshAttachedSessionStatus on a constructed Home.
// The repo's documented recurring failure mode is exactly "the policy is
// correct but the wiring line was dropped and the suite stayed green", so each
// test here is built to FAIL if its wiring line is deleted:
//
//  1. The fail-open line (backgroundStatusUpdate: `maxSkips = 0` when no fresh
//     viewport snapshot exists). Entering the adaptive gate always rewrites the
//     session's ledger entry — decide() records every outcome — so these tests
//     seed a ledger baseline with a nonzero skip count and assert it is left
//     UNTOUCHED when the snapshot is nil/untyped/stale. Delete the line and
//     the sweep consults the gate anyway, the entry is rewritten (skips reset
//     to 0, fingerprint rebased), and the assertion fails.
//
//  2. The attach-return invalidation (refreshAttachedSessionStatus:
//     `h.refreshLedger.forget(inst.ID)`). The test seeds a baseline and
//     asserts the entry is GONE after attach-return. Delete the line and the
//     entry survives, and the assertion fails.
//
// These run without a tmux server: SeedServerAliveForTest pins the fast-fail
// probe open, and every subprocess the sweep attempts fails fast and lands on
// the "session is gone" path, which is itself part of the assertion (a polled
// row's status must leave running; a skipped row's must not).

// newWiringHome builds the minimal Home backgroundStatusUpdate needs, with the
// adaptive policy armed exactly as the production constructor arms it
// (home.go: newRefreshLedger + defaultAdaptiveRefreshMaxSkips).
func newWiringHome(t *testing.T, instances ...*session.Instance) *Home {
	t.Helper()
	h := &Home{}
	h.sessionRenderSnapshot.Store(make(map[string]sessionRenderState))
	h.refreshLedger = newRefreshLedger()
	h.adaptiveMaxSkips = defaultAdaptiveRefreshMaxSkips
	h.lastPersistedStatus = make(map[string]string)
	h.lastPersistedAutoNameDesc = make(map[string]string)
	h.clearOnCompactSent = make(map[string]time.Time)
	h.instances = instances
	h.instanceByID = make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		h.instanceByID[inst.ID] = inst
	}
	return h
}

// runningInst returns an Instance in StatusRunning with a (nonexistent) tmux
// session attached, so a real UpdateStatus poll observably moves its status
// off running while a skipped poll leaves it untouched.
func runningInst(t *testing.T, id, tmuxName string) *session.Instance {
	t.Helper()
	inst := instWithTmuxName(t, id, tmuxName)
	inst.Status = session.StatusRunning
	return inst
}

// seedLedgerBaseline drives the ledger to entry {fp, skips: 1} for id — the
// state a quiescent off-screen session is in mid-hold. Returns the baseline
// fingerprint so tests can assert the entry was left untouched.
func seedLedgerBaseline(t *testing.T, l *refreshLedger, id string) sessionFingerprint {
	t.Helper()
	fp := sessionFingerprint{activity: 4242, title: "quiet", command: "node", ok: true}
	if skip, reason := l.decide(id, fp, session.StatusWaiting, false, 2); skip {
		t.Fatalf("seed: baseline sweep must poll, got skip (%s)", reason)
	}
	if skip, reason := l.decide(id, fp, session.StatusWaiting, false, 2); !skip {
		t.Fatalf("seed: second sweep must skip to reach skips=1, got poll (%s)", reason)
	}
	return fp
}

func ledgerEntry(l *refreshLedger, id string) (refreshLedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[id]
	return e, ok
}

// TestBackgroundStatusUpdate_FailsOpenWithoutFreshViewportSnapshot drives the
// real sweep with the three snapshot states that must force maxSkips=0 (gate
// bypassed entirely, every row polls): never published, published with the
// wrong type, and published but older than visibleSessionsMaxAge.
//
// Catches deletion of the `maxSkips = 0` fail-open in backgroundStatusUpdate:
// without it the gate runs, decide() rewrites the seeded ledger entry
// (skips 1 -> 0, fingerprint rebased), and the untouched-entry assertion
// fails.
func TestBackgroundStatusUpdate_FailsOpenWithoutFreshViewportSnapshot(t *testing.T) {
	cases := []struct {
		name    string
		publish func(h *Home)
	}{
		{
			name:    "no snapshot ever published",
			publish: func(h *Home) {},
		},
		{
			name: "snapshot of the wrong type",
			publish: func(h *Home) {
				h.visibleSessions.Store("not a visibleSessionSnapshot")
			},
		},
		{
			name: "snapshot older than visibleSessionsMaxAge",
			publish: func(h *Home) {
				h.visibleSessions.Store(visibleSessionSnapshot{
					ids: map[string]struct{}{"wiring-onscreen-" + h.instances[0].ID: {}},
					at:  time.Now().Add(-visibleSessionsMaxAge - time.Second),
				})
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmux.SeedServerAliveForTest(t, true)
			suffix := string(rune('A' + i))
			instA := runningInst(t, "wiring-a-"+suffix, "agentdeck-wiring-a-"+suffix)
			instB := runningInst(t, "wiring-b-"+suffix, "agentdeck-wiring-b-"+suffix)
			h := newWiringHome(t, instA, instB)
			heldFP := seedLedgerBaseline(t, h.refreshLedger, instB.ID)
			tc.publish(h)

			h.backgroundStatusUpdate()

			// Every row polled: a real UpdateStatus against a nonexistent tmux
			// session moves the status off running. A held row would still be
			// running.
			if got := instA.GetStatusThreadSafe(); got == session.StatusRunning {
				t.Fatalf("instA was not polled: status still %q", got)
			}
			if got := instB.GetStatusThreadSafe(); got == session.StatusRunning {
				t.Fatalf("instB was not polled: status still %q", got)
			}

			// The gate was BYPASSED, not merely vetoed: decide() records every
			// consultation, so the seeded entry must be byte-identical. If the
			// fail-open `maxSkips = 0` line is deleted the gate runs, polls on
			// the unusable in-sweep fingerprint, and rewrites this entry
			// (skips -> 0, fingerprint rebased) — failing here.
			entry, ok := ledgerEntry(h.refreshLedger, instB.ID)
			if !ok {
				t.Fatal("seeded ledger entry disappeared during a failed-open sweep")
			}
			if entry.skips != 1 || entry.fp != heldFP {
				t.Fatalf("failed-open sweep touched the ledger: entry = %+v (skips %d), want fp %+v with skips 1",
					entry.fp, entry.skips, heldFP)
			}
		})
	}
}

// TestBackgroundStatusUpdate_ConsultsGateWithFreshSnapshot is the positive
// control for the fail-open test above: with a FRESH viewport snapshot the
// sweep must consult the gate, which records its verdict in the ledger. This
// pins the gate wiring itself (delete the whole `if maxSkips > 0` block and
// the entry stays untouched, failing here) so the fail-open test's
// "entry untouched" assertion cannot be trivially satisfied by the gate never
// existing.
func TestBackgroundStatusUpdate_ConsultsGateWithFreshSnapshot(t *testing.T) {
	tmux.SeedServerAliveForTest(t, true)
	instOff := runningInst(t, "wiring-fresh-off", "agentdeck-wiring-fresh-off")
	h := newWiringHome(t, instOff)
	seedLedgerBaseline(t, h.refreshLedger, instOff.ID)
	h.visibleSessions.Store(visibleSessionSnapshot{
		ids: map[string]struct{}{"some-other-visible-row": {}},
		at:  time.Now(),
	})

	h.backgroundStatusUpdate()

	// The in-sweep fingerprint for a session with no live tmux backing is
	// unusable (ok=false), so the consulted gate must POLL and rebase the
	// entry: skips reset to 0, baseline no longer the seeded one.
	entry, ok := ledgerEntry(h.refreshLedger, instOff.ID)
	if !ok {
		t.Fatal("gate consultation should have recorded a ledger entry")
	}
	if entry.skips != 0 {
		t.Fatalf("consulted gate must reset the skip counter on poll, got skips=%d", entry.skips)
	}
	if entry.fp.ok {
		t.Fatalf("in-sweep fingerprint for a dead session must be unusable, got %+v", entry.fp)
	}
	if got := instOff.GetStatusThreadSafe(); got == session.StatusRunning {
		t.Fatalf("unusable fingerprint must force a poll: status still %q", got)
	}
}

// TestBackgroundStatusUpdate_VisibleDueRowIsAdmittedAndRebased pins the
// visible-row half of the gate (issue #1753 group-expand case): a VISIBLE row
// whose fingerprint evidence is absent must not be held — it becomes a budget
// candidate, is admitted (well under visiblePollBudgetPerSweep), gets its
// ledger baseline rebased by admitPoll, and is actually polled. Catches
// deletion of the admission block in backgroundStatusUpdate: without it a due
// visible row is never polled and the seeded entry survives unchanged.
func TestBackgroundStatusUpdate_VisibleDueRowIsAdmittedAndRebased(t *testing.T) {
	tmux.SeedServerAliveForTest(t, true)
	inst := runningInst(t, "wiring-vis", "agentdeck-wiring-vis")
	h := newWiringHome(t, inst)
	seedLedgerBaseline(t, h.refreshLedger, inst.ID)
	h.visibleSessions.Store(visibleSessionSnapshot{
		ids: map[string]struct{}{inst.ID: {}},
		at:  time.Now(),
	})

	h.backgroundStatusUpdate()

	entry, ok := ledgerEntry(h.refreshLedger, inst.ID)
	if !ok {
		t.Fatal("admitted visible row should have a rebased ledger entry")
	}
	if entry.skips != 0 || entry.fp.ok {
		t.Fatalf("admitPoll must rebase on the (unusable) in-sweep fingerprint with counters reset, got %+v (skips %d)",
			entry.fp, entry.skips)
	}
	if got := inst.GetStatusThreadSafe(); got == session.StatusRunning {
		t.Fatalf("due visible row was not polled: status still %q", got)
	}
}

// TestRefreshAttachedSessionStatus_ForgetsLedgerBaseline pins the
// attach-return invalidation: refreshAttachedSessionStatus clears hook status
// and forces a live check, so it MUST also drop the adaptive-refresh baseline
// — otherwise the next sweep could skip on a fingerprint recorded before the
// invalidation and hold a stale status for up to maxSkips sweeps.
//
// Catches deletion of the `h.refreshLedger.forget(inst.ID)` line in
// refreshAttachedSessionStatus: without it the seeded entry survives and the
// entry-gone assertion fails.
func TestRefreshAttachedSessionStatus_ForgetsLedgerBaseline(t *testing.T) {
	tmux.SeedServerAliveForTest(t, true)
	inst := runningInst(t, "wiring-attach", "agentdeck-wiring-attach")
	h := newWiringHome(t, inst)
	seedLedgerBaseline(t, h.refreshLedger, inst.ID)

	h.refreshAttachedSessionStatus(inst.ID)

	if entry, ok := ledgerEntry(h.refreshLedger, inst.ID); ok {
		t.Fatalf("attach-return must forget the adaptive-refresh baseline, entry survived: %+v (skips %d)",
			entry.fp, entry.skips)
	}
}

// TestBackgroundStatusUpdate_PipeIdleShortcutIsBoundedByCeiling pins the fix
// for the review's first HIGH finding: the PipeManager quiet-pipe shortcut
// ("skip idle sessions when PipeManager knows they haven't produced output")
// runs BEFORE the fingerprint gate and, before this fix, skipped
// unconditionally for as long as PipeManager reported no output — never
// reaching refreshLedger, so a piped session's baseline was never refreshed
// and it could be held forever.
//
// Drives the REAL backgroundStatusUpdate against a fake, permanently-quiet
// connected pipe (tmux.SeedConnectedPipeForTest — no real tmux -C process,
// no tmux server) for h.adaptiveMaxSkips consecutive sweeps, asserting the
// shortcut is ALLOWED to skip (status stays Running, unpolled) up to the
// ceiling, then MUST stop skipping on the next sweep even though
// PipeManager keeps reporting the identical stale lastOutput throughout.
// Catches deletion of the pipeIdleSkip bound in backgroundStatusUpdate: an
// unbounded shortcut would keep the row at StatusRunning forever and the
// final assertion would fail.
func TestBackgroundStatusUpdate_PipeIdleShortcutIsBoundedByCeiling(t *testing.T) {
	tmux.SeedServerAliveForTest(t, true)
	const tmuxName = "agentdeck-wiring-piped"
	inst := runningInst(t, "wiring-piped", tmuxName)
	h := newWiringHome(t, inst)
	// A fresh viewport snapshot that does NOT include this row keeps it
	// off-screen and keeps maxSkips resolved at its normal value (not the
	// fail-open 0) — the case under test.
	h.visibleSessions.Store(visibleSessionSnapshot{
		ids: map[string]struct{}{"some-other-row": {}},
		at:  time.Now(),
	})

	pm := tmux.NewPipeManager(context.Background(), nil)
	staleOutput := time.Now().Add(-10 * time.Second) // > the 5s PipeManager-idle threshold
	tmux.SeedConnectedPipeForTest(t, pm, tmuxName, staleOutput)
	tmux.SetPipeManager(pm)
	t.Cleanup(func() { tmux.SetPipeManager(nil) })

	for i := 0; i < h.adaptiveMaxSkips; i++ {
		h.backgroundStatusUpdate()
		if got := inst.GetStatusThreadSafe(); got != session.StatusRunning {
			t.Fatalf("sweep %d: PipeManager shortcut should still be allowed to skip (under the ceiling), but status became %q", i, got)
		}
	}

	// The ceiling is now exhausted: this sweep must force a real poll
	// despite PipeManager reporting the SAME stale lastOutput as every prior
	// sweep. A real UpdateStatus against a nonexistent tmux session moves
	// the status off Running.
	h.backgroundStatusUpdate()
	if got := inst.GetStatusThreadSafe(); got == session.StatusRunning {
		t.Fatal("PipeManager quiet-pipe shortcut held the row past the staleness ceiling: it was never polled")
	}
}
