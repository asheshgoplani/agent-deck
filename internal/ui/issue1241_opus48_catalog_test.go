package ui

import (
	"slices"
	"testing"
)

// TestKnownModelIDsIncludeOpus48 is the regression gate for issue #1241:
// the latest Claude Opus 4.8 must be selectable in the TUI new-session model
// picker, mirroring the web MODEL_ID_CATALOG. knownModelIDsForTool feeds the
// dropdown rows, so the new ID has to appear alongside the existing 4.7 entry.
func TestKnownModelIDsIncludeOpus48(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"claude", "claude-opus-4-8"},
		{"opencode", "anthropic/claude-opus-4-8"},
	}
	for _, tc := range cases {
		ids := knownModelIDsForTool(tc.tool)
		if !slices.Contains(ids, tc.want) {
			t.Errorf("knownModelIDsForTool(%q) missing %q; got %v", tc.tool, tc.want, ids)
		}
	}
}
