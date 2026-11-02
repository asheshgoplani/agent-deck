package session

import "encoding/json"

// Whether a pi was launched with a socket path in AGENTDECK_PI_SOCKET lives in the
// tool_data extras zone, like identity_injection_persist.go: a pi launched with
// it has a v5 extension serving the socket, and the daemon must never type into
// it, whichever process loads the session. A row without the key is a pi
// launched by a binary that predates the socket, or without it: the key is
// deleted at the zero value.
const toolDataPiSocketExportedKey = "pi_socket_exported"

// WritePiSocketExportedToToolData merges the launch decision into td, removing
// the key when the socket was not exported. Sibling keys are preserved.
func WritePiSocketExportedToToolData(td json.RawMessage, exported bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if exported {
		m[toolDataPiSocketExportedKey] = json.RawMessage("true")
	} else {
		delete(m, toolDataPiSocketExportedKey)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadPiSocketExportedFromToolData extracts the launch decision; false for a
// missing, malformed or legacy row.
func ReadPiSocketExportedFromToolData(td json.RawMessage) bool {
	if len(td) == 0 {
		return false
	}
	var blob struct {
		Exported bool `json:"pi_socket_exported"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.Exported
}
