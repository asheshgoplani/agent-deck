package ui

import (
	"strings"
	"testing"
)

func TestNewDialogKimiModelSuggestionsUseFriendlyNames(t *testing.T) {
	setXDGTestHome(t)
	d := NewNewDialog()
	d.SetDefaultTool("kimi")
	d.SetSize(120, 60)
	d.Show()
	d.focusIndex = d.indexOf(focusModel)
	d.updateFocus()

	view := d.View()
	for _, want := range []string{"K3.1 Pro", "K3.1 Pro Highspeed", "K3", "K3 256K", "K2.8", "K2.7 Highspeed"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Kimi model dropdown missing %q: %q", want, view)
		}
	}
	if strings.Contains(view, "kimi-code/") {
		t.Fatalf("Kimi model dropdown shows raw aliases instead of names: %q", view)
	}

	d.openModelSuggestions()
	d.modelSuggestionCursor = 1
	d.ApplyHighlightedModelSuggestion()
	if got := d.modelInput.Value(); got != "K3.1 Pro" {
		t.Fatalf("selected model display = %q, want K3.1 Pro", got)
	}
	if got := d.GetLaunchModelID(); got != "kimi-code/k3.1-pro" {
		t.Fatalf("selected model ID = %q, want kimi-code/k3.1-pro", got)
	}
}

func TestNewDialogKimiDefaultModelUsesFriendlyName(t *testing.T) {
	home := setXDGTestHome(t)
	writeXDGTestConfig(t, home, "[kimi]\ndefault_model = \"kimi-code/k3\"\n")
	d := NewNewDialog()
	d.SetDefaultTool("kimi")
	d.SetSize(120, 60)
	d.Show()

	if got := d.modelInput.Value(); got != "K3" {
		t.Fatalf("default model display = %q, want K3", got)
	}
	if got := d.GetLaunchModelID(); got != "kimi-code/k3" {
		t.Fatalf("default model ID = %q, want kimi-code/k3", got)
	}
}
