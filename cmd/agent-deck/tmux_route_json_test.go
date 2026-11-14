package main

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A client that runs tmux itself against a session (the Mac app's Terminal
// pane, over ssh) needs the session's -L socket and the tmux binary that can
// talk to that server: a 3.4 client fails against a 3.6a server. list --json
// and session show --json name both; a session on the default server, or on
// a socket without a [tmux.binaries] entry, carries no tmux_binary.
func TestTmuxRouteJSON_ListAndShow(t *testing.T) {
	tmux.SetSocketBinaries(map[string]string{"deck36": "/opt/tmux-3.6/bin/tmux"})
	t.Cleanup(func() { tmux.SetSocketBinaries(nil) })

	instances := []*session.Instance{
		{ID: "moved", Title: "moved", Tool: "shell", TmuxSocketName: "deck36"},
		{ID: "plain-socket", Title: "plain-socket", Tool: "shell", TmuxSocketName: "lab"},
		{ID: "default", Title: "default", Tool: "shell"},
	}
	out, err := buildListJSON("_test", instances)
	if err != nil {
		t.Fatalf("buildListJSON: %v", err)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	byID := map[string]map[string]interface{}{}
	for _, r := range rows {
		byID[r["id"].(string)] = r
	}
	want := map[string][2]interface{}{
		"moved":        {"deck36", "/opt/tmux-3.6/bin/tmux"},
		"plain-socket": {"lab", nil},
		"default":      {nil, nil},
	}
	for id, w := range want {
		if got := byID[id]["tmux_socket_name"]; got != w[0] {
			t.Errorf("list %s tmux_socket_name = %v, want %v", id, got, w[0])
		}
		if got := byID[id]["tmux_binary"]; got != w[1] {
			t.Errorf("list %s tmux_binary = %v, want %v", id, got, w[1])
		}
	}

	for _, inst := range instances {
		show := map[string]interface{}{}
		addTmuxRouteJSON(show, inst)
		w := want[inst.ID]
		if show["tmux_socket_name"] != w[0] || show["tmux_binary"] != w[1] {
			t.Errorf("show %s = %v, want socket %v binary %v", inst.ID, show, w[0], w[1])
		}
	}
}

func TestConfiguredBinary_OnlyForAnEntry(t *testing.T) {
	tmux.SetSocketBinaries(map[string]string{"deck36": "/opt/tmux-3.6/bin/tmux"})
	t.Cleanup(func() { tmux.SetSocketBinaries(nil) })
	if bin, ok := tmux.ConfiguredBinary(" deck36 "); !ok || bin != "/opt/tmux-3.6/bin/tmux" {
		t.Errorf("deck36 = %q %v", bin, ok)
	}
	for _, s := range []string{"", "lab"} {
		if bin, ok := tmux.ConfiguredBinary(s); ok || bin != "" {
			t.Errorf("%q = %q %v, want none", s, bin, ok)
		}
	}
}
