// Package history bridges agent-deck's live session state onto the ported
// agenthop history model. This file is agent-hopdeck-specific glue; the
// model/source/tree subpackages are verbatim ports and stay free of
// agent-deck coupling.
package history

import (
	"github.com/asheshgoplani/agent-deck/internal/history/model"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// OverlayInstanceStatus upgrades the status of any browsed session that is
// currently a live agent-deck instance, using agent-deck's authoritative
// status machine instead of the ported ~/.claude/sessions registry.
// Sessions with no matching live instance keep their ported status
// (recent/closed by mtime, or registry-derived). Mutates projects in place.
func OverlayInstanceStatus(projects []model.Project, instances []*session.Instance) {
	byClaudeID := make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		if inst != nil && inst.ClaudeSessionID != "" {
			byClaudeID[inst.ClaudeSessionID] = inst
		}
	}
	for pi := range projects {
		for si := range projects[pi].Sessions {
			s := &projects[pi].Sessions[si]
			inst, ok := byClaudeID[s.ID]
			if !ok {
				continue
			}
			switch inst.GetStatusThreadSafe() {
			case session.StatusRunning, session.StatusStarting:
				s.Status = model.StatusRunningBusy
			case session.StatusWaiting:
				s.Status = model.StatusWaiting
			case session.StatusIdle:
				s.Status = model.StatusRunningIdle
			default:
				// stopped/error/queued: leave ported status as-is.
			}
		}
	}
}
