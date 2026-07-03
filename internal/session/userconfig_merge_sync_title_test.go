package session

import "testing"

// MergePanelConfigOntoDisk is an allowlist-style merger: any panel-managed
// field omitted from the function silently fails to persist. The Settings
// panel's "Sync Session Title" toggle writes UserConfig.SyncTitle, so the
// merge must overlay it — otherwise flipping the toggle in the TUI appears
// to work but is reverted to the on-disk value the moment it saves.
//
// TestMergePanelConfigOntoDisk_PropagatesSyncTitle pins the SyncTitle
// overlay for both transitions. The off case is the reported bug: SyncTitle
// defaults to true (nil == true via GetSyncTitle), so a user disabling the
// toggle must have false round-trip through the merge.
func TestMergePanelConfigOntoDisk_PropagatesSyncTitle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	// Toggle off via panel input — the reported bug. Default is true, so the
	// user's explicit false must survive the merge.
	off := false
	panel := &UserConfig{SyncTitle: &off}
	merged, err := MergePanelConfigOntoDisk(panel)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if merged.SyncTitle == nil {
		t.Fatal("merge dropped SyncTitle — panel's false toggle never reaches disk (GetSyncTitle falls back to true)")
	}
	if merged.GetSyncTitle() {
		t.Fatal("merge failed to propagate SyncTitle=false — 'Sync Session Title' toggle would be stuck on")
	}

	// Toggle back on must also propagate.
	on := true
	panel2 := &UserConfig{SyncTitle: &on}
	merged2, err := MergePanelConfigOntoDisk(panel2)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if !merged2.GetSyncTitle() {
		t.Fatal("merge failed to propagate SyncTitle=true — toggle would be stuck off")
	}
}
