package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestDisplaySessionTitle pins the substitution rule for auto-named sessions:
// live pane title → saved description → handle. Non-auto-named sessions always
// show their handle. The saved-description fallback is what keeps a meaningful
// name visible on reopen when the session is stopped/idle and no live pane
// title is available.
func TestDisplaySessionTitle(t *testing.T) {
	cases := []struct {
		name      string
		autoName  bool
		title     string
		savedDesc string // persisted AutoNameDescription
		paneTitle string // live (already-cleaned) pane title
		want      string
	}{
		{"auto-named, live pane follows name", true, "lively-fjord", "", "Review SketchUp models", "lively-fjord"},
		{"auto-named, live pane beats saved desc", true, "lively-fjord", "Old task", "New task", "lively-fjord"},
		{"auto-named, idle keeps name", true, "lively-fjord", "Review SketchUp models", "", "lively-fjord"},
		{"auto-named, idle and no saved desc -> handle", true, "lively-fjord", "", "", "lively-fjord"},
		{"not auto-named ignores pane and desc", false, "my-feature", "ignored", "Some task", "my-feature"},
		{"not auto-named, no pane", false, "Auth", "", "", "Auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := &session.Instance{Title: tc.title, AutoName: tc.autoName}
			inst.SetAutoNameDescription(tc.savedDesc)
			if got := displaySessionTitle(inst, tc.paneTitle); got != tc.want {
				t.Errorf("displaySessionTitle(title=%q autoName=%v savedDesc=%q, pane=%q) = %q, want %q",
					tc.title, tc.autoName, tc.savedDesc, tc.paneTitle, got, tc.want)
			}
		})
	}
}

func TestAutoNamedSessionLabels(t *testing.T) {
	state := sessionRenderState{title: "lively-fjord", autoName: true, paneTitle: "Current task", autoNameDesc: "Old task"}
	if title, subtitle := sessionDisplayLabelsFromState(state); title != "lively-fjord" || subtitle != "Current task" {
		t.Fatalf("live labels = %q, %q", title, subtitle)
	}
	state.paneTitle = ""
	if title, subtitle := sessionDisplayLabelsFromState(state); title != "lively-fjord" || subtitle != "Old task" {
		t.Fatalf("saved labels = %q, %q", title, subtitle)
	}
}
