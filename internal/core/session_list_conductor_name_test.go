package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The conductor bridge finds its conductor's inbox from the session list. A
// renamed or decorated conductor no longer carries the title
// "conductor-<name>", so the row must name the conductor by its stable key.
// A worker that merely carries a conductor-shaped title is not a conductor.
func TestSessionRowConductorName(t *testing.T) {
	renamed := &session.Instance{ID: "c1", Title: "ops (renamed)", IsConductor: true, ConductorName: "ops"}
	worker := &session.Instance{ID: "w1", Title: "conductor-ops", Tool: "claude"}
	instances := []*session.Instance{renamed, worker}

	row := staticSessionRow(renamed, instances, "_test", nil)
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"conductor_name":"ops"`) {
		t.Errorf("row JSON lacks conductor_name: %s", data)
	}

	wrow := staticSessionRow(worker, instances, "_test", nil)
	data, err = json.Marshal(wrow)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "conductor_name") {
		t.Errorf("non-conductor row JSON carries conductor_name: %s", data)
	}
}
