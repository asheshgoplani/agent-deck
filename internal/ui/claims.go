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
	// instance takes the session over. Heartbeats refresh every status sweep
	// (2–10s), so 15s tolerates one slow sweep without flapping.
	claimStaleAfter = 15 * time.Second

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
// claim polling off every session is owned — exactly today's behavior.
func (h *Home) isOwned(sessionID string) bool {
	if !h.claimPolling {
		return true
	}
	h.ownedMu.RLock()
	defer h.ownedMu.RUnlock()
	return h.ownedSessions[sessionID]
}

// reconcileClaims claims in-scope sessions, releases out-of-scope ones, and
// refreshes claim heartbeats. Runs once per background status sweep.
func (h *Home) reconcileClaims(instances []*session.Instance) {
	if !h.claimPolling {
		return
	}
	db := statedb.GetGlobal()
	if db == nil {
		return
	}

	h.groupScopeMu.RLock()
	scope := h.groupScope
	h.groupScopeMu.RUnlock()

	active := session.FilterInstancesByArchive(instances, false)
	archived := session.FilterInstancesByArchive(instances, true)
	in, out := splitInstancesByScope(active, scope)

	inIDs := make([]string, 0, len(in))
	for _, inst := range in {
		inIDs = append(inIDs, inst.ID)
	}
	owned, err := db.ClaimSessions(inIDs, scope, claimStaleAfter)
	if err != nil {
		uiLog.Warn("claim_reconcile_failed", slog.String("error", err.Error()))
		return // keep previous owned set; next sweep retries
	}
	if err := db.RefreshClaimHeartbeats(); err != nil {
		uiLog.Debug("claim_heartbeat_refresh_failed", slog.String("error", err.Error()))
	}

	// Release claims we hold for sessions that moved out of scope, plus
	// archived sessions: they're display-frozen and never polled, so holding
	// their claim only blocks other instances from noticing they're dead.
	outIDs := make([]string, 0, len(out)+len(archived))
	for _, inst := range out {
		outIDs = append(outIDs, inst.ID)
	}
	for _, inst := range archived {
		outIDs = append(outIDs, inst.ID)
	}
	if err := db.ReleaseClaims(outIDs); err != nil {
		uiLog.Debug("claim_release_failed", slog.String("error", err.Error()))
	}

	h.ownedMu.Lock()
	h.ownedSessions = owned
	h.ownedMu.Unlock()

	// Orphan sweep: the primary instance slow-polls sessions no scoped
	// instance claims, so their statuses and notifications stay alive.
	// Merged into the owned set for THIS sweep only; never claimed.
	if time.Since(h.lastOrphanSweep) >= orphanSweepEvery {
		h.lastOrphanSweep = time.Now()
		if isPrimary, err := db.ElectPrimary(30 * time.Second); err == nil && isPrimary {
			claims, err := db.LoadClaims()
			if err == nil {
				allIDs := make([]string, 0, len(active))
				for _, inst := range active {
					allIDs = append(allIDs, inst.ID)
				}
				// ownedSessions is read by concurrent goroutines that snapshot the
				// map reference under RLock and use it after RUnlock (see
				// reconcileLivePipes). That's safe only while writers replace the
				// map wholesale, so merge via copy-on-write — never mutate a
				// published map in place.
				h.ownedMu.Lock()
				merged := make(map[string]bool, len(h.ownedSessions)+8)
				for id := range h.ownedSessions {
					merged[id] = true
				}
				for _, id := range orphanIDs(allIDs, claims, claimStaleAfter) {
					merged[id] = true
				}
				h.ownedSessions = merged
				h.ownedMu.Unlock()
			}
		}
	}
}

// shouldSweepInstance combines the archived skip with the ownership gate.
func (h *Home) shouldSweepInstance(inst *session.Instance) bool {
	return shouldPollStatusInLoop(inst) && h.isOwned(inst.ID)
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
