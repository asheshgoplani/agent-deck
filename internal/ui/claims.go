package ui

import (
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const (
	// claimStaleAfter is how old a claim heartbeat may be before another
	// instance takes the session over. nextStatusInterval can pause up to
	// maxStatusInterval (10s) AFTER a sweep that itself may run several
	// seconds long, so the gap between two heartbeats from a live owner can
	// exceed the naive "one sweep interval" estimate. 30s clears that worst
	// case with margin while still reclaiming a genuinely dead owner promptly.
	claimStaleAfter = 30 * time.Second

	// orphanSweepEvery is how often the primary instance polls for orphaned
	// sessions (no live claim from any scoped instance).
	orphanSweepEvery = 30 * time.Second
)

// orphanIDs returns ids with no live claim: absent row or stale heartbeat.
func orphanIDs(all []string, claims map[string]statedb.ClaimRow, staleAfter time.Duration) []string {
	cutoff := time.Now().Add(-staleAfter).Unix()
	var out []string
	for _, id := range all {
		row, ok := claims[id]
		if !ok || row.Heartbeat < cutoff {
			out = append(out, id)
		}
	}
	return out
}

// pathInScope reports whether a group path falls inside a -g scope. Empty
// scope matches everything. Same semantics as Home.isInGroupScope.
func pathInScope(path, scope string) bool {
	if scope == "" {
		return true
	}
	return path == scope || strings.HasPrefix(path, scope+"/")
}

// splitInstancesByScope partitions instances into inside/outside the scope.
func splitInstancesByScope(instances []*session.Instance, scope string) (in, out []*session.Instance) {
	for _, inst := range instances {
		if pathInScope(inst.GroupPath, scope) {
			in = append(in, inst)
		} else {
			out = append(out, inst)
		}
	}
	return in, out
}

// isOwned reports whether this instance actively polls the session. With
// claim polling off, or before the first successful reconcile (or whenever
// the claim DB is unavailable), ownedSessions is nil and every session is
// owned — degradation is fail-open, never fail-closed: a broken claim table
// must never cause an instance to silently stop polling everything it holds.
func (h *Home) isOwned(sessionID string) bool {
	if !h.claimPolling {
		return true
	}
	h.ownedMu.RLock()
	owned := h.ownedSessions
	h.ownedMu.RUnlock()
	if owned == nil {
		return true
	}
	return owned[sessionID]
}

// isPolledByMe reports whether this instance polls the session this sweep,
// either because it owns it or because it's the primary's orphan sweep
// target. Orphans are polled (status + WriteStatus) but never claimed, never
// pinned to pipes, never idle-watched, never revived — callers that gate
// those must keep using isOwned, not isPolledByMe.
func (h *Home) isPolledByMe(sessionID string) bool {
	if h.isOwned(sessionID) {
		return true
	}
	h.ownedMu.RLock()
	defer h.ownedMu.RUnlock()
	return h.orphanPolled[sessionID]
}

// invalidateNewlyOwnedMemo drops memo[id] for every id that is owned now but
// wasn't in prevOwned (nil prevOwned means "never reconciled before", so
// every currently owned id counts as newly owned). A stale per-instance
// status memo inherited from before this instance owned the session must not
// suppress the first write-through as the new owner.
func invalidateNewlyOwnedMemo(memo map[string]string, owned, prevOwned map[string]bool) {
	for id := range owned {
		if !prevOwned[id] {
			delete(memo, id)
		}
	}
}

// reconcileClaims claims in-scope sessions, releases out-of-scope ones, and
// (on the primary, on the sweep the orphan interval is due) refreshes the
// separate orphan-polled set. Runs once per background status sweep.
func (h *Home) reconcileClaims(instances []*session.Instance) {
	if !h.claimPolling {
		return
	}
	db := statedb.GetGlobal()
	if db == nil {
		// Claim machinery unavailable: fail open. Leave ownedSessions as-is
		// (nil on first reconcile, or the last good set) rather than clearing
		// it — isOwned/isPolledByMe treat nil as "own everything", matching
		// flag-off behavior instead of polling nothing.
		return
	}

	h.groupScopeMu.RLock()
	scope := h.groupScope
	h.groupScopeMu.RUnlock()

	// Snapshot the previously owned set (only this goroutine writes it) so the
	// release below can be intersected with it.
	h.ownedMu.RLock()
	prevOwned := h.ownedSessions
	h.ownedMu.RUnlock()

	// Single pass over the sweep snapshot: claim targets (in-scope live),
	// the orphan-sweep universe (all live), and release candidates. Archived
	// sessions are display-frozen and never polled, so holding their claim
	// only blocks other instances from noticing they're dead; release them
	// like out-of-scope ones. Both release sets are intersected with the
	// previous owned snapshot so a large archived backlog doesn't generate
	// no-op DELETE churn every sweep.
	inIDs := make([]string, 0, len(instances))
	activeIDs := make([]string, 0, len(instances))
	var releaseIDs []string
	for _, inst := range instances {
		if inst.IsArchived() {
			if prevOwned[inst.ID] {
				releaseIDs = append(releaseIDs, inst.ID)
			}
			continue
		}
		activeIDs = append(activeIDs, inst.ID)
		if pathInScope(inst.GroupPath, scope) {
			inIDs = append(inIDs, inst.ID)
		} else if prevOwned[inst.ID] {
			releaseIDs = append(releaseIDs, inst.ID)
		}
	}

	// A claim for a session not yet flushed to `instances` by SaveInstances
	// can be pruned by a neighbor's PruneStaleSessionClaims; this self-heals
	// on the next 2s re-claim, so it's a benign, transient race.
	owned, err := db.ClaimSessions(inIDs, scope, claimStaleAfter)
	if err != nil {
		uiLog.Warn("claim_reconcile_failed_degraded_fail_open", slog.String("error", err.Error()))
		return // keep previous owned set (nil stays nil, fail-open); next sweep retries
	}

	if err := db.ReleaseClaims(releaseIDs); err != nil {
		uiLog.Debug("claim_release_failed", slog.String("error", err.Error()))
	}

	// Sessions newly owned this sweep may carry a status memo written while a
	// different instance (or nobody, in fail-open mode) owned them; drop it
	// so the first sweep as new owner always writes through.
	invalidateNewlyOwnedMemo(h.lastPersistedStatus, owned, prevOwned)

	h.ownedMu.Lock()
	h.ownedSessions = owned
	h.ownedMu.Unlock()

	// Orphan sweep: the primary instance slow-polls sessions no scoped
	// instance claims, so their statuses and notifications stay alive.
	// Tracked in a SEPARATE set (never merged into ownedSessions) so orphans
	// can never poison the next sweep's prevOwned snapshot (no-op DELETE
	// churn) or leak into the strictly-owned pipe/idle/reviver filters.
	if time.Since(h.lastOrphanSweep) < orphanSweepEvery {
		h.resetOrphanPolled()
		return
	}

	// Headless web daemons never hold primary: the startup gate in
	// cmd/agent-deck/main.go excludes webHeadless precisely so a headless
	// instance can't block a subsequent TUI start under allow_multiple=false.
	// reconcileClaims runs in headless mode too (via the status worker), so
	// mirror that exception here rather than letting it capture primary.
	if h.headless {
		h.resetOrphanPolled()
		return
	}

	isPrimary, err := db.ElectPrimary(30 * time.Second)
	if err != nil {
		// Transient error: don't advance lastOrphanSweep, retry next sweep
		// instead of silently doubling the orphan interval.
		h.resetOrphanPolled()
		return
	}
	if !isPrimary {
		// Valid, non-transient outcome: some other instance holds primary.
		// Advance lastOrphanSweep so we don't re-run ElectPrimary every sweep.
		h.lastOrphanSweep = time.Now()
		h.resetOrphanPolled()
		return
	}

	claims, err := db.LoadClaims()
	if err != nil {
		// Transient error: don't advance lastOrphanSweep, retry next sweep.
		h.resetOrphanPolled()
		return
	}

	polled := make(map[string]bool, 8)
	for _, id := range orphanIDs(activeIDs, claims, claimStaleAfter) {
		polled[id] = true
	}

	h.ownedMu.Lock()
	h.orphanPolled = polled
	h.ownedMu.Unlock()
	h.lastOrphanSweep = time.Now()
}

// resetOrphanPolled clears the orphan-polled set on sweeps where the orphan
// pass is not due (or not ours to run) so it never outlives its due sweep.
func (h *Home) resetOrphanPolled() {
	h.ownedMu.Lock()
	h.orphanPolled = nil
	h.ownedMu.Unlock()
}

// shouldSweepInstance combines the archived skip with the polling gate: the
// background sweep polls a session it owns OR that is due for this sweep's
// orphan pass (isPolledByMe), so orphan statuses actually get polled and
// persisted instead of being silently dropped.
func (h *Home) shouldSweepInstance(inst *session.Instance) bool {
	return shouldPollStatusInLoop(inst) && h.isPolledByMe(inst.ID)
}

// ownedOnly filters instances to those this instance actively polls.
// With claim polling off it returns instances unchanged.
func (h *Home) ownedOnly(instances []*session.Instance) []*session.Instance {
	if !h.claimPolling {
		return instances
	}
	out := make([]*session.Instance, 0, len(instances))
	for _, inst := range instances {
		if h.isOwned(inst.ID) {
			out = append(out, inst)
		}
	}
	return out
}
