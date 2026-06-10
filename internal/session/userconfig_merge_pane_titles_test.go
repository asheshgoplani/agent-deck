package session

import "testing"

// MergePanelConfigOntoDisk is an allowlist-style merger: any panel-managed
// field omitted from the function silently fails to persist. This test
// pins the Display.ShowPaneTitles overlay so the wiring can't regress to
// "toggle does nothing" behavior.
//
// TestMergePanelConfigOntoDisk_PropagatesShowPaneTitles pins the
// Display.ShowPaneTitles overlay for both the on and off transitions.
func TestMergePanelConfigOntoDisk_PropagatesShowPaneTitles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	// Toggle on via panel input.
	panel := &UserConfig{
		Display: DisplaySettings{ShowPaneTitles: true},
	}
	merged, err := MergePanelConfigOntoDisk(panel)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if !merged.Display.ShowPaneTitles {
		t.Fatal("merge dropped Display.ShowPaneTitles=true — toggle would never persist to disk")
	}

	// Toggle back off must also propagate (zero-value bool, easy to drop accidentally).
	panel2 := &UserConfig{
		Display: DisplaySettings{ShowPaneTitles: false},
	}
	merged2, err := MergePanelConfigOntoDisk(panel2)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if merged2.Display.ShowPaneTitles {
		t.Fatal("merge failed to propagate Display.ShowPaneTitles=false — toggle would be stuck on")
	}
}
