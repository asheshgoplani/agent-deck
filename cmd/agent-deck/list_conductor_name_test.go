package main

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The legacy `list --json` builder must carry conductor_name exactly as
// core.SessionRow does (TestCoreRegistryMatchesLegacyHandlers compares bytes).
func TestBuildListJSON_ConductorName(t *testing.T) {
	instances := []*session.Instance{
		{ID: "c1", Title: "ops (renamed)", Tool: "claude", IsConductor: true, ConductorName: "ops"},
		{ID: "w1", Title: "conductor-ops", Tool: "claude"},
	}
	out, err := buildListJSON("_test", instances, map[*session.Instance]bool{instances[0]: true, instances[1]: true})
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
	if got := byID["c1"]["conductor_name"]; got != "ops" {
		t.Errorf("conductor row conductor_name = %v, want ops", got)
	}
	if _, ok := byID["w1"]["conductor_name"]; ok {
		t.Errorf("worker row carries conductor_name: %v", byID["w1"])
	}
}
