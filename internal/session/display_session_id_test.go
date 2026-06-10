package session

import "testing"

// TestDisplaySessionID mirrors the PREVIEW pane's per-tool branching: the
// value DisplaySessionID returns must match the "Session:" line the right
// pane renders for each tool, so a copy of the preview info carries the same
// ID the user sees.
func TestDisplaySessionID(t *testing.T) {
	tests := []struct {
		name string
		inst *Instance
		want string
	}{
		{
			name: "claude",
			inst: &Instance{Tool: "claude", ClaudeSessionID: "claude-abc"},
			want: "claude-abc",
		},
		{
			name: "gemini",
			inst: &Instance{Tool: "gemini", GeminiSessionID: "gem-123"},
			want: "gem-123",
		},
		{
			name: "opencode",
			inst: &Instance{Tool: "opencode", OpenCodeSessionID: "oc-456"},
			want: "oc-456",
		},
		{
			name: "codex",
			inst: &Instance{Tool: "codex", CodexSessionID: "cdx-789"},
			want: "cdx-789",
		},
		{
			name: "claude without id",
			inst: &Instance{Tool: "claude"},
			want: "",
		},
		{
			name: "unknown tool, no tmux session",
			inst: &Instance{Tool: "mystery-tool"},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.inst.DisplaySessionID(); got != tc.want {
				t.Errorf("DisplaySessionID() = %q, want %q", got, tc.want)
			}
		})
	}
}
