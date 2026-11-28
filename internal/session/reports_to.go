package session

import (
	"encoding/json"
	"strings"
)

// Issue #3165: a session launched from inside a sub-session reports to that
// launcher. Parent links stay one level deep (the tree, set-parent and the
// TUI are unchanged); ReportsTo only changes where the session's records go.

const toolDataReportsToKey = "reports_to"

// ReportTargetID is the id of the session this session's records go to: its
// launcher (ReportsTo) when one is recorded, else its tree parent. Every
// comms routing decision (inbox, wake, stats, ledger observers, restart and
// idle-exit lines) reads this instead of ParentSessionID, so a worker under
// nest_under_parent reports to the coordinator and the root gets no copy.
func (inst *Instance) ReportTargetID() string {
	if inst == nil {
		return ""
	}
	if r := strings.TrimSpace(inst.ReportsTo); r != "" && r != inst.ID {
		return r
	}
	return strings.TrimSpace(inst.ParentSessionID)
}

// reportTargetIDIn is ReportTargetID against a registry: a launcher that is no
// longer registered (removed) or is archived falls back to the tree parent
// when that parent is registered and not archived, so a nested worker whose
// coordinator is gone still reaches the root, as it did before issue #3165.
// Otherwise the launcher id is kept: a top-level worker whose launcher was
// removed lands in the unowned ledger as parent_removed, and one whose
// launcher is archived waits in that inbox, like a child of a removed or
// archived tree parent. A plain tree parent (no ReportsTo) is never replaced.
func (inst *Instance) reportTargetIDIn(byID map[string]*Instance) string {
	target := inst.ReportTargetID()
	if inst == nil || byID == nil || target == "" || liveInRegistry(byID, target) {
		return target
	}
	if parent := strings.TrimSpace(inst.ParentSessionID); parent != "" && parent != target && liveInRegistry(byID, parent) {
		return parent
	}
	return target
}

// liveInRegistry reports whether id is registered and not archived.
func liveInRegistry(byID map[string]*Instance, id string) bool {
	inst := byID[id]
	return inst != nil && !inst.IsArchived()
}

// SetReportsTo records the launcher a new session reports to.
func (inst *Instance) SetReportsTo(id string) {
	inst.ReportsTo = strings.TrimSpace(id)
	inst.reportsToCleared = false
}

// ClearReportsTo drops the launcher link, for an explicit set-parent or
// unset-parent: a link the user chose replaces the one launch recorded.
func (inst *Instance) ClearReportsTo() {
	if inst.ReportsTo != "" {
		inst.reportsToCleared = true
	}
	inst.ReportsTo = ""
}

// WriteReportsToToToolData records reports_to in the blob. An empty value
// removes the key (MergeToolDataExtras then carries a stored value forward,
// so a save from a copy that never loaded it cannot erase it); cleared
// writes an explicit empty value instead, which the merge honours.
func WriteReportsToToToolData(td json.RawMessage, value string, cleared bool) json.RawMessage {
	if value == "" && cleared {
		m := map[string]json.RawMessage{}
		if len(td) > 0 {
			_ = json.Unmarshal(td, &m)
		}
		m[toolDataReportsToKey] = json.RawMessage(`""`)
		out, err := json.Marshal(m)
		if err != nil {
			return td
		}
		return out
	}
	return writeToolDataString(td, toolDataReportsToKey, value)
}

// ReadReportsToFromToolData returns reports_to, or "" for missing, cleared,
// malformed or legacy rows.
func ReadReportsToFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		ReportsTo string `json:"reports_to"`
	}
	_ = json.Unmarshal(td, &blob)
	return strings.TrimSpace(blob.ReportsTo)
}
