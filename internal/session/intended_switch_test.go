package session

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestIntendedSwitch_ToolDataRoundTripAndClear(t *testing.T) {
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	rec := &IntendedSwitch{Account: "work", Model: "opus", Reason: "planned", At: at, ExpiresAt: at.Add(time.Hour)}
	base := json.RawMessage(`{"context_level":"primer"}`)

	td := WriteIntendedSwitchToToolData(base, rec)
	got := ReadIntendedSwitchFromToolData(td)
	if got == nil || *got != *rec {
		t.Fatalf("round trip = %+v, want %+v", got, rec)
	}
	if ReadContextLevelFromToolData(td) != "primer" {
		t.Errorf("sibling key lost: %s", td)
	}

	// An explicit clear must win over the stored row in the save merge.
	cleared := WriteIntendedSwitchToToolData(td, nil)
	if merged := statedb.MergeToolDataExtras(td, cleared); ReadIntendedSwitchFromToolData(merged) != nil {
		t.Errorf("cleared record resurrected by MergeToolDataExtras: %s", merged)
	}

	inst := &Instance{IntendedSwitch: rec}
	if j := inst.ActiveIntendedSwitchJSON(at.Add(59 * time.Minute)); j == nil || j.ExpiresAt != "2026-10-18T10:00:00Z" {
		t.Errorf("active record JSON = %+v", j)
	}
	if j := inst.ActiveIntendedSwitchJSON(at.Add(time.Hour)); j != nil {
		t.Errorf("record at expires_at should be omitted, got %+v", j)
	}
}
