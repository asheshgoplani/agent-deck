package core

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// `list --json` (the core registry path) names the tmux server a session
// lives on and the client for it, so a client that runs tmux against the
// session itself (a desktop app's terminal pane over ssh) can reach a
// session moved to another socket with the right tmux. tmux_binary only when
// a [tmux.binaries] entry applies; both absent on the default server.
func TestSessionRowTmuxRoute(t *testing.T) {
	tmux.SetSocketBinaries(map[string]string{"deck36": "/opt/tmux-3.6/bin/tmux"})
	t.Cleanup(func() { tmux.SetSocketBinaries(nil) })

	cases := []struct {
		inst        *session.Instance
		socket, bin interface{}
	}{
		{&session.Instance{ID: "moved", Title: "moved", Tool: "shell", TmuxSocketName: "deck36"}, "deck36", "/opt/tmux-3.6/bin/tmux"},
		{&session.Instance{ID: "lab", Title: "lab", Tool: "shell", TmuxSocketName: "lab"}, "lab", nil},
		{&session.Instance{ID: "default", Title: "default", Tool: "shell"}, nil, nil},
	}
	var instances []*session.Instance
	for _, c := range cases {
		instances = append(instances, c.inst)
	}
	for _, c := range cases {
		data, err := json.Marshal(staticSessionRow(c.inst, instances, "_test", nil))
		if err != nil {
			t.Fatal(err)
		}
		var row map[string]interface{}
		if err := json.Unmarshal(data, &row); err != nil {
			t.Fatal(err)
		}
		if row["tmux_socket_name"] != c.socket || row["tmux_binary"] != c.bin {
			t.Errorf("%s: tmux_socket_name=%v tmux_binary=%v, want %v %v", c.inst.ID, row["tmux_socket_name"], row["tmux_binary"], c.socket, c.bin)
		}
	}
}
