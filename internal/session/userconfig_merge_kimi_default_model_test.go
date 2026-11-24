package session

import "testing"

// The Kimi default_model is TOML-only, like the other providers' default_model.
// The settings panel no longer edits it, so the allowlist merge must not overlay
// panel.Kimi.DefaultModel (always empty) over the on-disk value. Before the
// removal the panel owned this field, so merging an empty panel wiped the
// user's [kimi] default_model on every Settings save.
func TestMergePanelConfigOntoDisk_PreservesKimiDefaultModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	if err := SaveUserConfig(&UserConfig{Kimi: KimiSettings{
		DefaultModel:   "kimi-code/k3",
		PermissionMode: KimiPermissionAskWhenNeeded,
	}}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	// A panel that edits only PermissionMode must leave DefaultModel alone.
	panel := &UserConfig{}
	panel.Kimi.PermissionMode = KimiPermissionNeverAsk
	merged, err := MergePanelConfigOntoDisk(panel)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk: %v", err)
	}
	if merged.Kimi.DefaultModel != "kimi-code/k3" {
		t.Fatalf("merge wiped [kimi].default_model: got %q, want kimi-code/k3", merged.Kimi.DefaultModel)
	}
	if merged.Kimi.PermissionMode != KimiPermissionNeverAsk {
		t.Fatalf("merge dropped panel permission_mode: got %q, want %q", merged.Kimi.PermissionMode, KimiPermissionNeverAsk)
	}
}
