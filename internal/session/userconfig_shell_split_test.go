package session

import "testing"

func TestUISettings_GetShellSplit(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"iterm", ShellSplitITerm},
		{"ITERM", ShellSplitITerm},
		{"iTerm", ShellSplitITerm},
		{"tmux", ShellSplitTmux},
		{"TMUX", ShellSplitTmux},
		{"", ""},
		{"unknown", ""},
		{"auto", ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			u := UISettings{ShellSplit: tc.input}
			got := u.GetShellSplit()
			if got != tc.want {
				t.Errorf("GetShellSplit(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
