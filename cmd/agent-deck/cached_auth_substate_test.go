package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestCachedListRetainsAuthSubstate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	inst := &session.Instance{ID: "cached-auth", Tool: "claude", Status: session.StatusError}
	dir := filepath.Join(home, "data", "agent-deck", "runtime", "auth-hold")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(session.AuthHoldRecord{InstanceID: inst.ID, Reason: session.AuthHoldReasonDeath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, inst.ID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := buildListJSON("default", []*session.Instance{inst}, map[*session.Instance]bool{inst: true})
	if err != nil {
		t.Fatal(err)
	}
	var rows []core.SessionRow
	if err := json.Unmarshal(output, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Substate != "auth-401" || rows[0].StatusSource != "cached" {
		t.Fatalf("cached auth diagnostic lost: %+v", rows)
	}
}
