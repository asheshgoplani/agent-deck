package session

import (
	"reflect"
	"testing"
)

func TestNormalizeUIHiddenTools(t *testing.T) {
	ui := UISettings{HiddenTools: []string{" Gemini ", "codex", "codex", "shell", "nope"}}
	normalizeUIHiddenTools(&ui, nil)
	want := []string{"codex", "gemini"}
	if !reflect.DeepEqual(ui.HiddenTools, want) {
		t.Fatalf("HiddenTools = %v, want %v", ui.HiddenTools, want)
	}
}

func TestNormalizeUIHiddenTools_CustomToolCase(t *testing.T) {
	ui := UISettings{HiddenTools: []string{"MyTool"}}
	custom := map[string]ToolDef{"MyTool": {Command: "my-tool"}}
	normalizeUIHiddenTools(&ui, custom)
	want := []string{"mytool"}
	if !reflect.DeepEqual(ui.HiddenTools, want) {
		t.Fatalf("HiddenTools = %v, want %v", ui.HiddenTools, want)
	}
}

func TestMergePanelConfig_ShowOnlyInstalledTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	if err := SaveUserConfig(&UserConfig{UI: UISettings{ShowOnlyInstalledTools: false}}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	panel := &UserConfig{UI: UISettings{ShowOnlyInstalledTools: true}}
	merged, err := MergePanelConfigOntoDisk(panel)
	if err != nil {
		t.Fatalf("MergePanelConfigOntoDisk: %v", err)
	}
	if !merged.UI.ShowOnlyInstalledTools {
		t.Fatal("ShowOnlyInstalledTools not propagated by merge")
	}
}
