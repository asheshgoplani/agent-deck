package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAutoNameRoundTrip pins that AutoName persists through the same
// json.Marshal → json.Unmarshal path Storage.Save/Load uses.
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
