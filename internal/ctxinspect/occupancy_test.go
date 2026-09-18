package ctxinspect

import (
	"encoding/json"
	"strings"
	"testing"
)

// Issue #2026: a reading over 100% is proof the window is wrong, not a
// percentage; a window read off the model id is marked inferred on the wire.

func TestOccupancy_OverLimitIsNotAPercentage(t *testing.T) {
	w := WindowInfo{Tokens: 200000, Source: WindowModelDefault}
	occ := w.Occupancy(494561)
	if occ.Known || !occ.OverLimit || occ.Percent != 0 {
		t.Fatalf("494561 of 200000: want over-limit with no percentage, got %+v", occ)
	}
	if _, ok := w.Percent(494561); ok {
		t.Fatal("Percent must not return a number above 100")
	}
	if occ.Window.Tokens != 200000 {
		t.Errorf("the disproved window must be kept for the surface to name: %+v", occ.Window)
	}
}

func TestOccupancy_ExactlyFullIsAllowed(t *testing.T) {
	occ := WindowInfo{Tokens: 1000, Source: WindowEnvOverride}.Occupancy(1000)
	if !occ.Known || occ.OverLimit || occ.Percent != 100 {
		t.Fatalf("1000 of 1000 is a legitimate 100%%: %+v", occ)
	}
}

func TestOccupancy_UnknownWindow(t *testing.T) {
	occ := WindowInfo{}.Occupancy(1000)
	if occ.Known || occ.OverLimit || occ.Inferred || occ.Percent != 0 {
		t.Fatalf("unknown window: want nothing known, got %+v", occ)
	}
}

func TestWindowInferred(t *testing.T) {
	cases := map[WindowSource]bool{
		WindowUnknown:         false,
		WindowHarnessReported: false,
		WindowEnvOverride:     false,
		WindowSettings:        false,
		WindowModelDefault:    true,
		WindowModelFamily:     true,
	}
	for src, want := range cases {
		w := WindowInfo{Tokens: 1, Source: src}
		if got := w.Inferred(); got != want {
			t.Errorf("%s: Inferred() = %v, want %v", src, got, want)
		}
		if got := w.Occupancy(0).Inferred; got != want {
			t.Errorf("%s: Occupancy.Inferred = %v, want %v", src, got, want)
		}
	}
}

func TestWindowInfoJSON_InferredMarker(t *testing.T) {
	raw, err := json.Marshal(WindowInfo{Tokens: 1000000, Source: WindowModelDefault, Detail: "model prefix"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"inferred":true`) {
		t.Errorf("model-default window must be marked inferred on the wire: %s", raw)
	}
	raw, err = json.Marshal(WindowInfo{Tokens: 1000000, Source: WindowEnvOverride})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"inferred"`) {
		t.Errorf("an established window must carry no inferred marker: %s", raw)
	}
	var back WindowInfo
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Tokens != 1000000 || back.Source != WindowEnvOverride {
		t.Errorf("round trip lost data: %+v", back)
	}
}
