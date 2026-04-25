package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSetField_Title_UpdatesAndReturnsOldValue(t *testing.T) {
	inst := &Instance{Title: "old-title"}

	oldValue, _, err := SetField(inst, FieldTitle, "new-title", nil)
	if err != nil {
		t.Fatalf("SetField returned error: %v", err)
	}
	if oldValue != "old-title" {
		t.Errorf("oldValue = %q, want %q", oldValue, "old-title")
	}
	if inst.Title != "new-title" {
		t.Errorf("inst.Title = %q, want %q", inst.Title, "new-title")
	}
}

func TestSetField_Color_Valid(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"hex", "#ff00aa", "#ff00aa"},
		{"ansi", "203", "203"},
		{"clear_empty", "", ""},
		{"clear_trimmed_whitespace", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := &Instance{Color: "initial"}
			_, _, err := SetField(inst, FieldColor, tc.input, nil)
			if err != nil {
				t.Fatalf("SetField returned error: %v", err)
			}
			if inst.Color != tc.want {
				t.Errorf("inst.Color = %q, want %q", inst.Color, tc.want)
			}
		})
	}
}

// Bad value must NOT mutate the field — mirrors CLI pre-extraction behavior.
func TestSetField_Color_Invalid(t *testing.T) {
	inst := &Instance{Color: "#123456"}
	_, _, err := SetField(inst, FieldColor, "red", nil)
	if err == nil {
		t.Fatal("expected error for invalid color, got nil")
	}
	if inst.Color != "#123456" {
		t.Errorf("inst.Color mutated on error: %q, want %q", inst.Color, "#123456")
	}
}

// Channels are a claude-only CLI flag — without this guard, value would
// persist on a gemini/shell session but buildCommand would never surface it.
func TestSetField_Channels_ClaudeOnly(t *testing.T) {
	inst := &Instance{Tool: "gemini"}
	_, _, err := SetField(inst, FieldChannels, "plugin:telegram@user/repo", nil)
	if err == nil {
		t.Fatal("expected error when setting channels on non-claude session, got nil")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should mention 'claude' constraint: %v", err)
	}
}

// CSV parsing must drop empty tokens — otherwise "a,,c" emits a literal
// empty channel id to the claude binary's --channels flag.
func TestSetField_Channels_ParsesCSV(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	_, _, err := SetField(inst, FieldChannels, "a, b ,, c", nil)
	if err != nil {
		t.Fatalf("SetField returned error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(inst.Channels) != len(want) {
		t.Fatalf("Channels = %v, want %v", inst.Channels, want)
	}
	for i := range want {
		if inst.Channels[i] != want[i] {
			t.Errorf("Channels[%d] = %q, want %q", i, inst.Channels[i], want[i])
		}
	}
}

// TUI form: single space-separated string. CLI form is the next test.
func TestSetField_ExtraArgs_TUIFormat(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	_, _, err := SetField(inst, FieldExtraArgs, "--model opus --verbose", nil)
	if err != nil {
		t.Fatalf("SetField returned error: %v", err)
	}
	want := []string{"--model", "opus", "--verbose"}
	if len(inst.ExtraArgs) != len(want) {
		t.Fatalf("ExtraArgs = %v, want %v", inst.ExtraArgs, want)
	}
	for i := range want {
		if inst.ExtraArgs[i] != want[i] {
			t.Errorf("ExtraArgs[%d] = %q, want %q", i, inst.ExtraArgs[i], want[i])
		}
	}
}

// CLI form: pre-tokenized argv — values with spaces stay one token.
func TestSetField_ExtraArgs_CLIFormat(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	_, _, err := SetField(inst, FieldExtraArgs, "", []string{"--model", "opus haiku"})
	if err != nil {
		t.Fatalf("SetField returned error: %v", err)
	}
	if len(inst.ExtraArgs) != 2 || inst.ExtraArgs[0] != "--model" || inst.ExtraArgs[1] != "opus haiku" {
		t.Errorf("ExtraArgs = %v, want [--model opus haiku]", inst.ExtraArgs)
	}
}

// Without this, "clear all extra args" silently keeps stale flags.
func TestSetField_ExtraArgs_EmptyClears(t *testing.T) {
	inst := &Instance{Tool: "claude", ExtraArgs: []string{"--model", "opus"}}
	_, _, err := SetField(inst, FieldExtraArgs, "", nil)
	if err != nil {
		t.Fatalf("SetField returned error: %v", err)
	}
	if inst.ExtraArgs != nil {
		t.Errorf("ExtraArgs should be nil after clear, got %v", inst.ExtraArgs)
	}
}

func TestSetField_InvalidField(t *testing.T) {
	inst := &Instance{}
	_, _, err := SetField(inst, "bogus", "x", nil)
	if err == nil {
		t.Fatal("expected error for invalid field, got nil")
	}
	if !strings.Contains(err.Error(), "invalid field") {
		t.Errorf("error should mention 'invalid field': %v", err)
	}
}

// Notes must stay live — moving them to FieldRestartRequired would
// silently force a restart for pure metadata edits.
func TestSetField_Notes_LiveEditable(t *testing.T) {
	inst := &Instance{Notes: "old"}
	_, _, err := SetField(inst, FieldNotes, "new", nil)
	if err != nil {
		t.Fatalf("SetField returned error: %v", err)
	}
	if inst.Notes != "new" {
		t.Errorf("Notes = %q, want %q", inst.Notes, "new")
	}
	if RestartPolicyFor(FieldNotes) != FieldLive {
		t.Error("Notes must be live-editable, not restart-required")
	}
}

func TestSetField_TitleLocked_ParsesBool(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
		{"yes", true},
		{"no", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			inst := &Instance{}
			_, _, err := SetField(inst, FieldTitleLocked, tc.input, nil)
			if err != nil {
				t.Fatalf("SetField returned error: %v", err)
			}
			if inst.TitleLocked != tc.want {
				t.Errorf("TitleLocked = %v, want %v", inst.TitleLocked, tc.want)
			}
		})
	}
}

// "maybe" must error, not silently coerce to false.
func TestSetField_TitleLocked_RejectsBadBool(t *testing.T) {
	inst := &Instance{TitleLocked: true}
	_, _, err := SetField(inst, FieldTitleLocked, "maybe", nil)
	if err == nil {
		t.Fatal("expected error for invalid bool, got nil")
	}
	if inst.TitleLocked != true {
		t.Error("TitleLocked mutated on error")
	}
}

// Pins the live-vs-restart contract — a regression that moves Command
// into FieldLive would silently lie to users about when edits take effect.
func TestRestartPolicyFor(t *testing.T) {
	liveFields := []string{
		FieldTitle, FieldColor, FieldNotes, FieldTitleLocked,
		FieldNoTransitionNotify, FieldClaudeSessionID, FieldGeminiSessionID,
	}
	restartFields := []string{
		FieldCommand, FieldWrapper, FieldTool, FieldChannels,
		FieldExtraArgs, FieldPath,
	}
	for _, f := range liveFields {
		if got := RestartPolicyFor(f); got != FieldLive {
			t.Errorf("RestartPolicyFor(%q) = %v, want FieldLive", f, got)
		}
	}
	for _, f := range restartFields {
		if got := RestartPolicyFor(f); got != FieldRestartRequired {
			t.Errorf("RestartPolicyFor(%q) = %v, want FieldRestartRequired", f, got)
		}
	}
}

// Mirrors cmd-package coverage to ensure no permutations dropped during
// the move from cmd/agent-deck/session_cmd.go to internal/session/.
func TestIsValidSessionColor_Exported(t *testing.T) {
	valid := []string{"", "#ff00aa", "#FF00AA", "#000000", "0", "255", "127"}
	invalid := []string{"red", "#12", "#gggggg", "256", "-1", "  ", "#ff00aa00"}
	for _, v := range valid {
		if !IsValidSessionColor(v) {
			t.Errorf("IsValidSessionColor(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if IsValidSessionColor(v) {
			t.Errorf("IsValidSessionColor(%q) = true, want false", v)
		}
	}
}

// Fields without a tmux side effect must return nil postCommit. CLI/TUI
// both branch on `if postCommit != nil`; a non-nil leak triggers a
// pointless subprocess on every CLI invocation.
func TestSetField_PostCommit_NilForSimpleFields(t *testing.T) {
	cases := []struct {
		field, value string
	}{
		{FieldTitle, "y"},
		{FieldColor, "#aabbcc"},
		{FieldNotes, "n"},
		{FieldCommand, "claude"},
		{FieldWrapper, ""},
		{FieldTool, "claude"},
		{FieldChannels, "a"},
		{FieldExtraArgs, "--m"},
		{FieldTitleLocked, "true"},
		{FieldNoTransitionNotify, "false"},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			inst := &Instance{Title: "x", Tool: "claude"}
			_, postCommit, err := SetField(inst, tc.field, tc.value, nil)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if postCommit != nil {
				t.Errorf("expected nil postCommit for %s, got non-nil", tc.field)
			}
		})
	}
}

// Editing a session-id field on a not-running session must skip the tmux
// subprocess (postCommit nil), not fire it speculatively.
func TestSetField_PostCommit_NilWhenSessionNotRunning(t *testing.T) {
	inst := &Instance{Title: "x", Tool: "claude"} // tmuxSession is nil
	_, postCommit, err := SetField(inst, FieldClaudeSessionID, "abc-123", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if postCommit != nil {
		t.Error("expected nil postCommit when tmux session not running")
	}
}

// A refactor that did `inst = &Instance{...}` would wipe Claude launch
// options. Guards that with a Title edit + ToolOptionsJSON round-trip.
func TestSetField_PreservesToolOptionsJSON(t *testing.T) {
	inst := &Instance{
		Title:           "before",
		ToolOptionsJSON: json.RawMessage(`{"model":"opus"}`),
	}
	_, _, err := SetField(inst, FieldTitle, "after", nil)
	if err != nil {
		t.Fatalf("SetField returned error: %v", err)
	}
	if string(inst.ToolOptionsJSON) != `{"model":"opus"}` {
		t.Errorf("ToolOptionsJSON clobbered by title edit: %s", inst.ToolOptionsJSON)
	}
}
