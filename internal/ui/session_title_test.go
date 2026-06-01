package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestDisplaySessionTitle pins the substitution rule: auto-named sessions show
// the live (already-cleaned) pane title; everything else shows the handle.
func TestDisplaySessionTitle(t *testing.T) {
	cases := []struct {
		name      string
		autoName  bool
		title     string
		paneTitle string
		want      string
	}{
		{"auto-named with task", true, "lively-fjord", "Review SketchUp models", "Review SketchUp models"},
		{"auto-named but idle (empty pane)", true, "lively-fjord", "", "lively-fjord"},
		{"not auto-named ignores pane", false, "my-feature", "Some task", "my-feature"},
		{"not auto-named, no pane", false, "Auth", "", "Auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := &session.Instance{Title: tc.title, AutoName: tc.autoName}
			if got := displaySessionTitle(inst, tc.paneTitle); got != tc.want {
				t.Errorf("displaySessionTitle(%+v, %q) = %q, want %q",
					inst, tc.paneTitle, got, tc.want)
			}
		})
	}
}
