package session

import (
	"testing"
)

// Phase 1 v1.9 regression coverage for issue #881 (profile divergence) —
// case prof-003.
//
// profileFromClaudeConfigDir is the inference layer that bridges the user's
// shell context (CLAUDE_CONFIG_DIR=~/.claude-work, set by the cdw alias) and
// agent-deck's profile namespace. Before #881 this logic existed only in
// internal/profile and the web/storage path skipped it entirely — same user,
// two different sessions lists. After #881 it is consolidated here and the
// TUI's profile.DetectCurrentProfile delegates to GetEffectiveProfile, but
// the helper itself has zero direct tests. A subtle change to the parsing
// rules (e.g. swapping the dash to an underscore, lowercasing the suffix,
// or accidentally returning "claude" for ~/.claude) would silently route
// users to the wrong profile dir without breaking either parity_test.go or
// the existing GetEffectiveProfile precedence test.
//
// This table covers every documented variant from the function comment plus
// the negative cases that must NOT match (path traversal, Windows-style
// separators that the current Linux build refuses to special-case).
func TestProfileFromClaudeConfigDir_DocumentedVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty input → no inference", in: "", want: ""},

		// The "official" cdw / cdp shapes from the dual-profile setup in CLAUDE.md.
		{name: ".claude-work → work", in: "/home/u/.claude-work", want: "work"},
		{name: ".claude-personal → personal", in: "/home/u/.claude-personal", want: "personal"},

		// .claude with no suffix means "no inference; let config default apply".
		// Critical: must NOT return "claude" — that would shadow every config default.
		{name: ".claude → no inference", in: "/home/u/.claude", want: ""},

		// Generic dashed dir, no leading dot. Last dash-segment wins per the
		// docstring's `/opt/claude-prod -> "prod"` example.
		{name: "claude-prod (no leading dot) → prod", in: "/opt/claude-prod", want: "prod"},
		{name: "deep path .claude-staging → staging", in: "/srv/svc/.claude-staging", want: "staging"},

		// Pathological inputs the current implementation must keep rejecting.
		// A ".claude-" with empty suffix should fall through to the dash-split
		// branch which also yields ""; failing this would mean we infer literal
		// "" as a profile name (then GetProfileDir uses "default" anyway, but
		// a regression that returned "claude-" would break the storage path).
		{name: ".claude- (trailing dash) → no inference", in: "/home/u/.claude-", want: ""},
		{name: "no dash, no dot → no inference", in: "/opt/claude", want: ""},
		{name: "trailing-dash on plain dir → no inference", in: "/opt/profile-", want: ""},

		// The TUI's profile resolution test (prof-002) hits the .claude branch
		// expecting "" so config.json default takes over. Lock that branch
		// here directly so that case can never regress to "claude" silently.
		{name: "/.claude (root install) → no inference", in: "/.claude", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := profileFromClaudeConfigDir(tc.in); got != tc.want {
				t.Errorf("profileFromClaudeConfigDir(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
