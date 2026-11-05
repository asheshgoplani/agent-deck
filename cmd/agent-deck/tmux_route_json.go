package main

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// tmuxRouteFields names the tmux server a session lives on, for clients that
// run tmux against it themselves (the Mac app's Terminal pane reads styles,
// sizes and a control-mode mirror over ssh): the -L socket name, and the
// [tmux.binaries] binary for that socket. A 3.4 client cannot talk to a 3.6a
// server, so a bare "tmux -L <socket>" is not enough. Both are "" (omitted
// from JSON) for a session on the default server or a socket without its own
// binary, so older consumers see no change.
func tmuxRouteFields(inst *session.Instance) (socketName, binary string) {
	socketName = inst.TmuxSocketName
	if bin, ok := tmux.ConfiguredBinary(socketName); ok {
		binary = bin
	}
	return socketName, binary
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
