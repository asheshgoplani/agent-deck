package session

import "testing"

// MergePanelConfigOntoDisk is an allowlist-style merger: any panel-managed
// field omitted from the function silently fails to persist. This test
// pins the Display.ShowSessionTimestamps overlay so the wiring can't
// regress to "toggle does nothing" behavior again.

func TestMergePanelConfigOntoDisk_PropagatesShowSessionTimestamps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	// Toggle on via panel input.
	panel := &UserConfig{
		Display: DisplaySettings{ShowSessionTimestamps: true},
	}
	merged, err := MergePanelConfigOntoDisk(panel)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if !merged.Display.ShowSessionTimestamps {
		t.Fatal("merge dropped Display.ShowSessionTimestamps=true — toggle would never persist to disk")
	}

	// Toggle back off must also propagate (zero-value bool, easy to drop accidentally).
	panel2 := &UserConfig{
		Display: DisplaySettings{ShowSessionTimestamps: false},
	}
	merged2, err := MergePanelConfigOntoDisk(panel2)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if merged2.Display.ShowSessionTimestamps {
		t.Fatal("merge failed to propagate Display.ShowSessionTimestamps=false — toggle would be stuck on")
	}
}
