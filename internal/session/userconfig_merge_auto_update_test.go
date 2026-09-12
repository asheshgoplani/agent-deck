package session

import "testing"

// MergePanelConfigOntoDisk is an allowlist-style merger, so the settings
// panel's "Install updates automatically" and "Restart automatically
// after update" toggles only persist if the merge overlays
// Updates.AutoInstall and Updates.AutoRestart. Both default to true (nil
// == true), so the case that matters is the user's explicit false.
func TestMergePanelConfigOntoDisk_PropagatesAutoInstallAndRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	off := false
	panel := &UserConfig{}
	panel.Updates.AutoInstall = &off
	panel.Updates.AutoRestart = &off
	merged, err := MergePanelConfigOntoDisk(panel)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if merged.Updates.GetAutoInstall() {
		t.Fatal("merge dropped Updates.AutoInstall=false; the toggle would be stuck on")
	}
	if merged.Updates.GetAutoRestart() {
		t.Fatal("merge dropped Updates.AutoRestart=false; the toggle would be stuck on")
	}

	// A panel that does not carry the fields (nil) leaves disk alone.
	if err := SaveUserConfig(merged); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()
	merged2, err := MergePanelConfigOntoDisk(&UserConfig{})
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if merged2.Updates.GetAutoInstall() || merged2.Updates.GetAutoRestart() {
		t.Fatal("a nil panel field must preserve the on-disk false")
	}

	on := true
	panel3 := &UserConfig{}
	panel3.Updates.AutoInstall = &on
	panel3.Updates.AutoRestart = &on
	merged3, err := MergePanelConfigOntoDisk(panel3)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk returned error: %v", err)
	}
	if !merged3.Updates.GetAutoInstall() || !merged3.Updates.GetAutoRestart() {
		t.Fatal("merge failed to propagate the toggles back to true")
	}
}
