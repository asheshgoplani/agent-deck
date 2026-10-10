package session

import "encoding/json"

// TrackCommandExit is creation provenance stored in tool_data so a fresh CLI
// process can distinguish a one-shot command from an older interactive shell.
// False is written explicitly so edits cannot resurrect a stale true value
// through MergeToolDataExtras.
func writeTrackCommandExitToToolData(td json.RawMessage, track bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	m["track_command_exit"], _ = json.Marshal(track)
	out, _ := json.Marshal(m)
	return out
}

func readTrackCommandExitFromToolData(td json.RawMessage) bool {
	var v struct {
		Track bool `json:"track_command_exit"`
	}
	_ = json.Unmarshal(td, &v)
	return v.Track
}
