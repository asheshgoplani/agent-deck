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
)

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
	_ = db.RefreshClaimHeartbeats()

	// Release claims we hold for sessions that moved out of scope.
	outIDs := make([]string, 0, len(out))
	for _, inst := range out {
		outIDs = append(outIDs, inst.ID)
	}
	_ = db.ReleaseClaims(outIDs)

	h.ownedMu.Lock()
	h.ownedSessions = owned
	h.ownedMu.Unlock()
}
