package ui

import "testing"

// Review round 3 (finding 4): a user who installed the hooks with
// `agent-deck hooks install` (no TUI answer recorded) must not be asked
// again when an upgrade makes the entries drift; presence is consent and the
// drift is repaired silently.
func TestClaudeHooksStartupDecision(t *testing.T) {
	cases := []struct {
		name               string
		present, installed bool
		prompted           string
		want               claudeHooksStartup
	}{
		{"clean install, never asked", true, true, "", claudeHooksStartup{watch: true}},
		{"clean install, accepted", true, true, "accepted", claudeHooksStartup{watch: true}},
		{"CLI install drifted after upgrade, never asked: repair, no prompt", true, false, "", claudeHooksStartup{reinstall: true, watch: true}},
		{"TUI install drifted after upgrade: repair, no prompt", true, false, "accepted", claudeHooksStartup{reinstall: true, watch: true}},
		{"drifted but once declined: still present, repair without asking", true, false, "declined", claudeHooksStartup{reinstall: true, watch: true}},
		{"accepted, then removed: silent reinstall", false, false, "accepted", claudeHooksStartup{reinstall: true, watch: true}},
		{"declined, absent: nothing", false, false, "declined", claudeHooksStartup{}},
		{"absent, never asked: prompt", false, false, "", claudeHooksStartup{prompt: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeHooksStartupDecision(tc.present, tc.installed, tc.prompted); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}
