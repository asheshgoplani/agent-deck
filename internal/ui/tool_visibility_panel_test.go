package ui

import (
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestToolVisibilityPanelIncludesKimiOnce(t *testing.T) {
	panel := NewToolVisibilityPanel()
	panel.LoadConfig(&session.UserConfig{Tools: map[string]session.ToolDef{
		"kimi":   {},
		"custom": {},
	}})

	want := []string{
		"claude", "gemini", "opencode", "codex", "pi", "copilot", "crush", "cursor", "hermes",
		"deepseek", "omp", "antigravity", "kimi", "custom",
	}
	if !reflect.DeepEqual(panel.toolNames, want) {
		t.Fatalf("toolNames = %#v, want %#v", panel.toolNames, want)
	}
	if !panel.visibleTools["kimi"] {
		t.Fatal("Kimi should be visible by default")
	}
}
