package ui

import (
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestOpenCode2PickerModelAndEditParity(t *testing.T) {
	d := NewNewDialog()
	d.SetDefaultTool("opencode2")
	if d.GetSelectedCommand() != "opencode2" || !d.selectedToolSupportsModel() {
		t.Fatal("new picker did not select the v2 launcher/model row")
	}
	d.filterModelSuggestions()
	if !slices.Contains(d.modelSuggestions, "openai/gpt-5.5") {
		t.Fatal("v2 launcher has no OpenCode model suggestions")
	}
	d.modelInput.SetValue("openai/gpt-5.5")
	if d.GetLaunchModelID() != "openai/gpt-5.5" {
		t.Fatal("v2 model selection was dropped")
	}
	if ToolIcon("opencode2") != ToolIcon("opencode") || ToolColor("opencode2") != ToolColor("opencode") {
		t.Fatal("v2 command has shell branding")
	}
	for _, tool := range []string{"opencode", "opencode2"} {
		inst := session.NewInstanceWithTool("edit", t.TempDir(), tool)
		edit := NewEditSessionDialog()
		edit.Show(inst)
		if changes := edit.GetChanges(inst); len(changes) != 0 || edit.switchPending() {
			t.Fatalf("opening %s produces changes/switch: %v", tool, changes)
		}
		for idx := range edit.fields {
			field := &edit.fields[idx]
			if field.key != session.FieldTool {
				continue
			}
			if !slices.Contains(field.pillOptions, "opencode2") || !slices.Contains(field.pillOptions, "opencode") {
				t.Fatal("edit picker is missing a launcher")
			}
			other := "opencode2"
			if tool == "opencode2" {
				other = "opencode"
			}
			field.pillCursor = slices.Index(field.pillOptions, other)
			if edit.switchPending() {
				t.Fatal("changing OpenCode launchers must not request an unsupported conversation transfer")
			}
			changes := edit.GetChanges(inst)
			if len(changes) != 1 || changes[0].Value != other {
				t.Fatalf("launcher change = %v", changes)
			}
		}
	}
}

func TestOpenCode2SettingsAndRemotePicker(t *testing.T) {
	panel := NewSettingsPanel()
	panel.LoadConfig(&session.UserConfig{DefaultTool: "opencode2"})
	if panel.toolValues[panel.selectedTool] != "opencode2" {
		t.Fatal("settings lost v2 default launcher")
	}
	d := NewNewDialog()
	d.ResetRemoteDefaults()
	d.SetRemoteCreationCatalog(&session.RemoteCreationCatalog{
		DefaultTool: "opencode2",
		Tools:       []session.RemoteCreationTool{{Name: "opencode2", Kind: "opencode", Models: []string{"remote/model"}}},
	})
	if d.GetSelectedCommand() != "opencode2" || !d.selectedToolSupportsModel() {
		t.Fatal("remote catalog did not expose the v2 model row")
	}
	d.filterModelSuggestions()
	if !slices.Contains(d.modelSuggestions, "remote/model") {
		t.Fatal("remote v2 model catalog was not used")
	}
}
