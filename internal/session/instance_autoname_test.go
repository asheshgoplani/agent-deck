package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAutoNameRoundTrip pins that AutoName survives a json.Marshal →
// json.Unmarshal round-trip of the Instance struct itself.
//
// NOTE: this is NOT the persistence path. Storage.Save/Load go through
// InstanceData → statedb.InstanceRow → SQLite columns, not Instance's own JSON
// tags. This test passing while that SQLite path dropped the field is exactly
// how the "auto name lost on reopen" bug shipped. The real persistence
// round-trip is covered by TestStorageSaveWithGroups_PersistsAutoName.
func TestAutoNameRoundTrip(t *testing.T) {
	inst := NewInstance("lively-fjord", t.TempDir())
	inst.AutoName = true

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("json.Marshal(inst): %v", err)
	}
	if !strings.Contains(string(data), `"auto_name":true`) {
		t.Errorf("marshalled instance missing \"auto_name\":true; got:\n%s", string(data))
	}

	revived := &Instance{}
	if err := json.Unmarshal(data, revived); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !revived.AutoName {
		t.Errorf("revived AutoName = false, want true")
	}
}

// TestAutoNameOmitemptyZeroValue pins that an unset AutoName is omitted from
// JSON, keeping existing state.json files byte-identical until the flag is set.
func TestAutoNameOmitemptyZeroValue(t *testing.T) {
	inst := NewInstance("plain-session", t.TempDir())

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("json.Marshal(inst): %v", err)
	}
	if strings.Contains(string(data), "auto_name") {
		t.Errorf("zero-value AutoName should be omitted (omitempty); got:\n%s", string(data))
	}
}

func TestSetAutoNameDescriptionIgnoresEmptyInput(t *testing.T) {
	inst := NewInstance("lively-fjord", t.TempDir())
	inst.SetAutoNameDescription("Review and improve SketchUp house models")

	inst.SetAutoNameDescription(" \t\n")

	if got := inst.GetAutoNameDescription(); got != "Review and improve SketchUp house models" {
		t.Errorf("description after empty update = %q, want previous meaningful title", got)
	}
}
