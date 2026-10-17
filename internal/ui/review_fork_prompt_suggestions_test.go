package ui

// Review finding B1 (red on 205b174d0d). A TUI fork with a parent selected in
// the fork dialog is a parented Claude session agent-deck launches. Its first
// start runs the baked fork command verbatim (IsForkAwaitingStart), so the
// parent must be known when CreateForkedInstanceForToolUnder builds that
// command, not set on the finished instance afterwards.

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestReview_ParentedTUIForkStartsWithPromptSuggestionsOff(t *testing.T) {
	cases := []struct {
		name    string
		parent  string
		wantOff bool
	}{
		{"parent chosen in the fork dialog", "parent01-1600000000", true},
		{"root fork", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := session.NewInstanceWithTool("fork-source", "/tmp/fork-source", "claude")
			source.ClaudeSessionID = "source-abc-123"
			source.ClaudeDetectedAt = time.Now()

			deps := defaultForkInstanceDeps()
			var started *session.Instance
			deps.startInstance = func(inst *session.Instance) error { started = inst; return nil }
			deps.createMultiRepoDir = func(_, _ *session.Instance) error { return nil }
			deps.rollback = func(_, _, _ string) {}

			inst, err := completeFork(source, "child fork", "", forkToggles{}, nil,
				tc.parent, "/tmp/parent", false, deps)
			if err != nil {
				t.Fatalf("completeFork: %v", err)
			}
			if started != inst || inst.ParentSessionID != tc.parent {
				t.Fatalf("fork started=%v parent=%q, want parent %q", started == inst, inst.ParentSessionID, tc.parent)
			}
			if tc.parent != "" && inst.ParentProjectPath != "/tmp/parent" {
				t.Fatalf("ParentProjectPath = %q, want /tmp/parent", inst.ParentProjectPath)
			}
			if !inst.IsForkAwaitingStart {
				t.Fatalf("first start must run the baked fork command")
			}
			if got := strings.Contains(inst.Command, "export CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false;"); got != tc.wantOff {
				t.Errorf("first launch command carries the prompt-suggestion export = %v, want %v: %s", got, tc.wantOff, inst.Command)
			}
		})
	}
}
