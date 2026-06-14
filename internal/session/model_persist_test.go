package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSetFieldModelPersistsClaude asserts the tool-agnostic `model` field
// (SetField) persists the operator's selected model into the Claude per-session
// option store (ClaudeOptions.Model) so it survives a restart (#1436). The
// restart-side consumption already prefers this over [claude].default_model
// (#1431); this guards the agent-deck-side persistence.
func TestSetFieldModelPersistsClaude(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("model-claude", t.TempDir(), "claude")

	old, postCommit, err := SetField(inst, FieldModel, "opus", nil)
	if err != nil {
		t.Fatalf("SetField(model) returned error: %v", err)
	}
	if postCommit != nil {
		postCommit()
	}
	if old != "" {
		t.Errorf("old model = %q, want empty (no prior override)", old)
	}

	if got := inst.LaunchModelID(); got != "opus" {
		t.Fatalf("LaunchModelID() = %q after SetField(model, opus); want opus", got)
	}

	// The built command must carry the persisted model on start.
	cmd := inst.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "--model") || !strings.Contains(cmd, "opus") {
		t.Errorf("built command missing persisted --model opus, got:\n%s", cmd)
	}

	// Round-trip through JSON (the tool_data path) and re-check it survives.
	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	revived := &Instance{}
	if err := json.Unmarshal(data, revived); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := revived.LaunchModelID(); got != "opus" {
		t.Errorf("revived LaunchModelID() = %q; want opus (model must survive JSON round-trip)", got)
	}
}

// TestSetFieldModelClearsClaude asserts an empty value clears the override.
func TestSetFieldModelClearsClaude(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("model-clear", t.TempDir(), "claude")
	if _, _, err := SetField(inst, FieldModel, "sonnet", nil); err != nil {
		t.Fatalf("set sonnet: %v", err)
	}
	if got := inst.LaunchModelID(); got != "sonnet" {
		t.Fatalf("precondition: LaunchModelID() = %q; want sonnet", got)
	}
	if _, _, err := SetField(inst, FieldModel, "", nil); err != nil {
		t.Fatalf("clear model: %v", err)
	}
	if got := inst.LaunchModelID(); got != "" {
		t.Errorf("LaunchModelID() = %q after clear; want empty", got)
	}
}

// TestSetFieldModelGemini asserts the same field drives the gemini per-session
// model, proving SetField is tool-agnostic and routes to each tool's store.
func TestSetFieldModelGemini(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("model-gemini", t.TempDir(), "gemini")
	if _, _, err := SetField(inst, FieldModel, "gemini-2.5-pro", nil); err != nil {
		t.Fatalf("set gemini model: %v", err)
	}
	if inst.GeminiModel != "gemini-2.5-pro" {
		t.Errorf("GeminiModel = %q; want gemini-2.5-pro", inst.GeminiModel)
	}
	if got := inst.LaunchModelID(); got != "gemini-2.5-pro" {
		t.Errorf("LaunchModelID() = %q; want gemini-2.5-pro", got)
	}
}

// TestModelFieldRestartRequired asserts the model field forces a restart so the
// new model is actually picked up (the running process keeps its launch model).
func TestModelFieldRestartRequired(t *testing.T) {
	if got := RestartPolicyFor(FieldModel); got != FieldRestartRequired {
		t.Errorf("RestartPolicyFor(model) = %v; want FieldRestartRequired", got)
	}
}

// TestModelFieldIsValidMutable asserts `model` is registered as a mutable field
// so `agent-deck session set <id> model <m>` is accepted.
func TestModelFieldIsValidMutable(t *testing.T) {
	for _, f := range ValidMutableFields {
		if f == FieldModel {
			return
		}
	}
	t.Errorf("FieldModel (%q) not in ValidMutableFields; CLI will reject `session set <id> model`", FieldModel)
}
