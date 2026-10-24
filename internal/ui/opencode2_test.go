package ui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestCreateSessionToolOpenCode2(t *testing.T) {
	for _, launcher := range []string{"opencode", "opencode2"} {
		t.Run(launcher, func(t *testing.T) {
			tool, command := createSessionTool(launcher)
			if tool != "opencode" || command != launcher {
				t.Fatalf("createSessionTool(%q) = (%q, %q), want (opencode, %s)", launcher, tool, command, launcher)
			}
			// Follow the new-session path: canonical feature identity, selected
			// launcher retained, correct label before the first live status poll.
			inst := session.NewInstanceWithTool("new", t.TempDir(), tool)
			inst.Command = command
			if inst.DisplayToolThreadSafe() != launcher || inst.OpenCodeCommandName() != launcher {
				t.Fatalf("new session lost launcher: display=%q picker=%q", inst.DisplayToolThreadSafe(), inst.OpenCodeCommandName())
			}
		})
	}
}

func TestOpenCode2LocalDisplaySurfaces(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	inst := &session.Instance{ID: "v2-display", Title: "worker", Tool: "opencode", Command: "opencode2", Status: session.StatusRunning}
	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	for _, surface := range []struct {
		name  string
		frame string
	}{
		{"list", h.renderSessionList(120, 30)},
		{"preview", h.renderPreviewPane(100, 40)},
	} {
		if !strings.Contains(stripAnsi(surface.frame), "opencode2") {
			t.Errorf("%s lost launcher identity: %s", surface.name, stripAnsi(surface.frame))
		}
	}
	h.embeddedMode = true
	h.insertModeSessionID = inst.ID
	if frame := stripAnsi(h.renderPreviewPane(100, 40)); !strings.Contains(frame, "opencode2") {
		t.Errorf("embedded preview lost launcher identity: %s", frame)
	}
}

func TestOpenCode2RemoteDisplaySurfaces(t *testing.T) {
	for _, fixture := range []struct {
		name, payload, want string
	}{
		{"v2", `{"id":"remote-display","title":"worker","tool":"opencode","command":"opencode2","status":"running"}`, "opencode2"},
		{"older remote", `{"id":"remote-display","title":"worker","tool":"opencode","status":"running"}`, "opencode"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var remote session.RemoteSessionInfo
			if err := json.Unmarshal([]byte(fixture.payload), &remote); err != nil {
				t.Fatal(err)
			}
			h := NewHome()
			h.width, h.height = 120, 40
			item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "lab"}
			h.flatItems = []session.Item{item}
			var row strings.Builder
			h.renderRemoteSessionItem(&row, item, true)
			for _, surface := range []struct {
				name, frame string
			}{
				{"row", row.String()},
				{"preview", h.renderPreviewPane(100, 40)},
			} {
				if !strings.Contains(stripAnsi(surface.frame), fixture.want) {
					t.Errorf("%s lost launcher identity: %s", surface.name, stripAnsi(surface.frame))
				}
				if fixture.want == "opencode" && strings.Contains(stripAnsi(surface.frame), "opencode2") {
					t.Errorf("%s incorrectly displays v2 for a legacy remote: %s", surface.name, stripAnsi(surface.frame))
				}
			}
			h.embeddedMode = true
			h.insertModeRemoteName, h.insertModeRemoteID = "lab", remote.ID
			frame := stripAnsi(h.renderPreviewPane(100, 40))
			if !strings.Contains(frame, fixture.want) {
				t.Errorf("embedded remote preview lost launcher identity: %s", frame)
			}
			if fixture.want == "opencode" && strings.Contains(frame, "opencode2") {
				t.Errorf("embedded remote preview incorrectly displays v2 for a legacy remote: %s", frame)
			}
		})
	}
}

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
