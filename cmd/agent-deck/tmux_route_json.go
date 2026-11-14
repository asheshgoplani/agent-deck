package main

import (
	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// tmuxRouteFields: see core.TmuxRoute (tmux_socket_name, tmux_binary).
func tmuxRouteFields(inst *session.Instance) (socketName, binary string) {
	return core.TmuxRoute(inst)
}

// addTmuxRouteJSON adds tmux_socket_name and tmux_binary to `session show
// --json`, with the same omission rules as `list --json`.
func addTmuxRouteJSON(jsonData map[string]interface{}, inst *session.Instance) {
	socketName, binary := tmuxRouteFields(inst)
	if socketName != "" {
		jsonData["tmux_socket_name"] = socketName
	}
	if binary != "" {
		jsonData["tmux_binary"] = binary
	}
}
