package ui

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// waitTimerPrimary reports whether this process is the primary agent-deck
// instance, the only one that answers prompts for the wait timer (ASK-119).
// A variable so tests decide the election.
var waitTimerPrimary = func() bool {
	db := statedb.GetGlobal()
	if db == nil {
		return false
	}
	primary, err := db.ElectPrimary(30 * time.Second)
	return err == nil && primary
}

// tickWaitTimer runs the per-session wait timer once this sweep's statuses
// are in. Only the primary TUI ticks it, so two agent-deck processes never
// press a key at the same prompt; a headless web server never does. With no
// session armed (the default) it costs one config read and no election.
func (h *Home) tickWaitTimer(instances []*session.Instance) {
	if h.waitTimer == nil {
		return
	}
	active := session.FilterInstancesByArchive(instances, false)
	if h.headless || !h.waitTimer.Armed(active) || !waitTimerPrimary() {
		// Forget every running wait, so a timer switched back on, or a
		// process that becomes primary later, starts counting afresh.
		h.waitTimer.Reset()
		return
	}
	h.waitTimer.Tick(active)
}
