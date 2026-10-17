package telemetry

import "testing"

// A TUI that dies in a panic reports error area=tui kind=panic; the event
// must pass the published schema or it is dropped as a schema drop.
func TestErrorEventAcceptsTUIPanic(t *testing.T) {
	props := map[string]any{"area": "tui", "kind": "panic", "tool": "other",
		"before_first_success": false, "onboarding_step": "none"}
	if err := Validate("error", props); err != nil {
		t.Fatalf("error area=tui kind=panic is rejected by the schema: %v", err)
	}
}
