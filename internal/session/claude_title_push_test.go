package session

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestClaudeNameSlug(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"already a slug passes through", "gitops-assistant-f4", "gitops-assistant-f4"},
		{"spaces become dashes", "Deck Push Test", "deck-push-test"},
		{"underscores and dots collapse", "deck_push.test", "deck-push-test"},
		{"runs of punctuation collapse to one dash", "deck --  push", "deck-push"},
		{"leading and trailing junk trimmed", "  --Deck Push--  ", "deck-push"},
		{"scoped title keeps its separator", "agent-deck/title sync", "agent-deck-title-sync"},
		// A title with nothing addressable left must yield "" so the callers
		// omit --name entirely rather than pushing an empty or junk name.
		{"empty title", "", ""},
		{"punctuation only", "-- ??? --", ""},
		{"non-ascii only", "日本語", ""},
		{"emoji stripped to the ascii remainder", "🚀 ship it", "ship-it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeNameSlug(tc.title); got != tc.want {
				t.Fatalf("ClaudeNameSlug(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestClaudeNameSlugTruncatesAtWordBoundary(t *testing.T) {
	long := "sync-deck-titles-into-claude-session-names-so-sendmessage-resolves"
	got := ClaudeNameSlug(long)
	if len(got) > claudeNameMaxLen {
		t.Fatalf("slug %q is %d bytes, over the %d bound", got, len(got), claudeNameMaxLen)
	}
	if got != "sync-deck-titles-into-claude-session-names-so" {
		t.Fatalf("unexpected truncation: %q", got)
	}
}

func TestClaudeNameSlugTruncatesMidWordWhenNoLateBoundary(t *testing.T) {
	// "a-" then one unbroken word: chopping back to the only dash would leave
	// "a", so the ragged mid-word cut is the better answer.
	long := "a-" + repeat("x", 80)
	got := ClaudeNameSlug(long)
	if len(got) != claudeNameMaxLen {
		t.Fatalf("slug %q is %d bytes, want exactly %d", got, len(got), claudeNameMaxLen)
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestExtraArgsSupplyName(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"nil", nil, false},
		{"unrelated flags", []string{"--model", "opus"}, false},
		{"long flag", []string{"--name", "mine"}, true},
		{"short flag", []string{"-n", "mine"}, true},
		{"equals form", []string{"--name=mine"}, true},
		// A VALUE that happens to read like the flag must not count: the
		// operator passed no name of their own here.
		{"value that looks like a flag name", []string{"--append-system-prompt", "call it --names"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extraArgsSupplyName(tc.args); got != tc.want {
				t.Fatalf("extraArgsSupplyName(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestClaudePushNameGates(t *testing.T) {
	t.Run("claude session slugs its title", func(t *testing.T) {
		inst := &Instance{Tool: "claude", Title: "Title Sync"}
		if got := inst.claudePushName(); got != "title-sync" {
			t.Fatalf("got %q, want %q", got, "title-sync")
		}
	})
	t.Run("non-claude tool is left alone", func(t *testing.T) {
		// Pushing /name into a shell pane would type the literal text at a
		// prompt, and --name is not a flag other tools accept.
		inst := &Instance{Tool: "shell", Title: "Title Sync"}
		if got := inst.claudePushName(); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("operator --name wins", func(t *testing.T) {
		inst := &Instance{Tool: "claude", Title: "Title Sync", ExtraArgs: []string{"--name", "mine"}}
		if got := inst.claudePushName(); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("unslugabble title yields no name", func(t *testing.T) {
		inst := &Instance{Tool: "claude", Title: "🚀"}
		if got := inst.claudePushName(); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

// The push must NOT consult the persisted Status field. That snapshot is
// maintained by the TUI's status worker, so a one-shot CLI rename reads
// whatever was last flushed to disk — gating on it silently disabled the push
// for every `agent-deck rename`. Liveness comes from the pane instead, which
// is why a stale non-idle Status must not by itself be a refusal.
func TestPushTitleToClaudeDoesNotGateOnPersistedStatus(t *testing.T) {
	inst := &Instance{Tool: "claude", Title: "Title Sync", Status: StatusRunning}
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_no_such_session"}
	// No real tmux server backs that name, so GetStatus reports "inactive" and
	// the push still declines — but on pane evidence, not on Status.
	if inst.PushTitleToClaude() {
		t.Fatal("pushed into a session with no live pane")
	}
	if inst.Status != StatusRunning {
		t.Fatal("push must not mutate Status")
	}
}

// No tmux session means no pane to type into — must not panic or report success.
func TestPushTitleToClaudeRefusesSessionWithoutTmux(t *testing.T) {
	inst := &Instance{Tool: "claude", Title: "Title Sync", Status: StatusIdle}
	if inst.PushTitleToClaude() {
		t.Fatal("pushed with no tmux session")
	}
}

func TestClaudeLaunchNameOmittedForNonClaudeTool(t *testing.T) {
	inst := &Instance{Tool: "shell", Title: "Title Sync"}
	if got := inst.ClaudeLaunchName(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// A rename on a live claude pane must hand back a postCommit — that is the
// hook every SetField caller runs to deliver /name. Complements
// TestSetField_PostCommit_NilForSimpleFields, which pins the no-pane case.
func TestSetFieldTitleReturnsPushPostCommitForLivePane(t *testing.T) {
	inst := &Instance{Title: "old", Tool: "claude"}
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_test"}
	_, postCommit, err := SetField(inst, FieldTitle, "Title Sync", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if postCommit == nil {
		t.Fatal("expected a postCommit that pushes the new name to claude, got nil")
	}
}

// The same rename on a tool that is not claude must stay free of side effects.
func TestSetFieldTitleSkipsPushForNonClaudeTool(t *testing.T) {
	inst := &Instance{Title: "old", Tool: "shell"}
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_test"}
	_, postCommit, err := SetField(inst, FieldTitle, "Title Sync", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if postCommit != nil {
		t.Fatal("expected nil postCommit for a non-claude session")
	}
}

// The launch flag is what heals sessions the live push skipped, so it must be
// present on the built command whenever a slug exists.
func TestBuildClaudeExtraFlagsCarriesName(t *testing.T) {
	inst := &Instance{Title: "Title Sync", Tool: "claude"}
	if got := inst.buildClaudeExtraFlags(nil); !strings.Contains(got, `--name title-sync`) {
		t.Fatalf("built flags %q missing --name title-sync", got)
	}
}

// An operator-supplied --name must appear exactly once: ours suppressed, theirs
// intact. Two --name flags would be last-wins-harmless but actively confusing.
func TestBuildClaudeExtraFlagsDefersToOperatorName(t *testing.T) {
	inst := &Instance{Title: "Title Sync", Tool: "claude", ExtraArgs: []string{"--name", "mine"}}
	got := inst.buildClaudeExtraFlags(nil)
	if strings.Count(got, "--name") != 1 {
		t.Fatalf("built flags %q should carry exactly one --name", got)
	}
	if !strings.Contains(got, "mine") {
		t.Fatalf("built flags %q dropped the operator's name", got)
	}
}
