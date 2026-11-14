package session

import "encoding/json"

// The launch decision lives in the tool_data extras zone, like
// identity_injection_persist.go: the tool the last launch ran (launched_tool),
// whether a pi's launch exported a socket path in AGENTDECK_PI_SOCKET
// (pi_socket_exported: a v5 or later extension serves the socket, so the daemon
// must never type into it, whichever process loads the session), and the tmux
// session the decision was made for (launch_tmux_session).
//
// Only a launch's targeted write (statedb.WriteLaunchDecision, or the restart
// outcome) records them; a full save leaves them to MergeToolDataExtras, which
// carries the stored ones forward, except for a launch that found no row to
// write to (launchUnrecorded), whose first save records them. Every binary's
// full save carries the keys forward, including one that launched the session
// without recording a decision, so the decision is trusted only while the row
// still names the tmux session it was made for (ReadLaunchDecisionFromToolData).
const (
	toolDataPiSocketExportedKey  = "pi_socket_exported"
	toolDataLaunchedToolKey      = "launched_tool"
	toolDataLaunchTmuxSessionKey = "launch_tmux_session"
)

// writeLaunchDecisionToToolData records a launch decision in td for a launch
// that spawned in tmuxSession. Sibling keys are preserved. The flag is written
// explicitly at false, so no stale value can ride under the new binding.
func writeLaunchDecisionToToolData(td json.RawMessage, tmuxSession, tool string, exported bool) json.RawMessage {
	if tool == "" || tmuxSession == "" {
		return td
	}
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	for k, v := range map[string]any{
		toolDataPiSocketExportedKey:  exported,
		toolDataLaunchedToolKey:      tool,
		toolDataLaunchTmuxSessionKey: tmuxSession,
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			return td
		}
		m[k] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadLaunchDecisionFromToolData extracts the launch decision of a row whose
// tmux_session column is tmuxSession: whether the launch exported the pi
// socket, and the tool it launched. A decision made for another tmux session
// describes an earlier launch, so it reads as none (false, ""), as does a
// missing, malformed or legacy row.
func ReadLaunchDecisionFromToolData(td json.RawMessage, tmuxSession string) (exported bool, tool string) {
	if len(td) == 0 || tmuxSession == "" {
		return false, ""
	}
	var blob struct {
		Exported    bool   `json:"pi_socket_exported"`
		Tool        string `json:"launched_tool"`
		TmuxSession string `json:"launch_tmux_session"`
	}
	if json.Unmarshal(td, &blob) != nil || blob.TmuxSession != tmuxSession {
		return false, ""
	}
	return blob.Exported, blob.Tool
}
