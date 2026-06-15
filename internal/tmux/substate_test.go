package tmux

import "testing"

// Honest Status v2: ClassifySubstate distinguishes distinct session conditions
// that all otherwise look like "running"/"waiting" to a coarse observer. The
// motivating failure was a Fable no-op loop ("X is currently unavailable" /
// "Crunched for 0s") that rendered no spinner-completion yet kept the session
// looking alive — supervisors (human + Maestro) could not tell a working
// session from a dead-model loop.
//
// These table tests pin the pure pane-content heuristic. Substate is ADDITIVE:
// it never changes the coarse status string, only enriches it.

func TestClassifySubstate_ModelUnavailable(t *testing.T) {
	d := NewPromptDetector("claude")
	cases := []struct {
		name    string
		content string
	}{
		{
			// Fable-down no-op loop: model reports unavailable, no real work.
			name: "model currently unavailable banner",
			content: "⏺ Fable is currently unavailable. Please try again later.\n" +
				"\n" +
				"❯ \n" +
				"  ? for shortcuts",
		},
		{
			name: "crunched for zero seconds no-op",
			content: "✶ Crunched for 0s (0 tokens)\n" +
				"\n" +
				"❯ ",
		},
		{
			name:    "model unavailable lowercase variant",
			content: "The model is currently unavailable, retrying...\n❯ ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.ClassifySubstate(tc.content); got != SubstateModelUnavailable {
				t.Errorf("got %q, want %q for %s", got, SubstateModelUnavailable, tc.name)
			}
		})
	}
}

func TestClassifySubstate_Auth401(t *testing.T) {
	d := NewPromptDetector("claude")
	cases := []struct {
		name    string
		content string
	}{
		{
			name: "401 auth failure with login instruction",
			content: "⏺ Please run /login · API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"Invalid authentication credentials\"}}\n" +
				"\n❯ ",
		},
		{
			name:    "socket connection closed banner",
			content: "API Error (Connection error.) · socket connection closed\n\n❯ ",
		},
		{
			name:    "bare login instruction banner",
			content: "Please run /login\n\n❯ ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.ClassifySubstate(tc.content); got != SubstateAuth401 {
				t.Errorf("got %q, want %q for %s", got, SubstateAuth401, tc.name)
			}
		})
	}
}

func TestClassifySubstate_IdleAtEmptyPrompt(t *testing.T) {
	d := NewPromptDetector("claude")
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "healthy empty prompt after completion",
			content: "⏺ All tests pass.\n\n❯ \n  ? for shortcuts",
		},
		{
			name:    "bare prompt no activity",
			content: "❯ ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.ClassifySubstate(tc.content); got != SubstateIdleAtEmptyPrompt {
				t.Errorf("got %q, want %q for %s", got, SubstateIdleAtEmptyPrompt, tc.name)
			}
		})
	}
}

func TestClassifySubstate_Running(t *testing.T) {
	d := NewPromptDetector("claude")
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "spinner with interrupt hint",
			content: "✻ Reticulating… (3s · ↑ 50 tokens · ctrl+c to interrupt)",
		},
		{
			name:    "esc to interrupt older clients",
			content: "⠋ Thinking (12s · esc to interrupt)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.ClassifySubstate(tc.content); got != SubstateRunning {
				t.Errorf("got %q, want %q for %s", got, SubstateRunning, tc.name)
			}
		})
	}
}

// Precedence: a model-unavailable line wins over the prompt that the tool
// redraws below it (the no-op loop is the actionable signal).
func TestClassifySubstate_UnavailableWinsOverPrompt(t *testing.T) {
	d := NewPromptDetector("claude")
	content := "⏺ Fable is currently unavailable. Please try again later.\n\n❯ \n  ? for shortcuts"
	if got := d.ClassifySubstate(content); got != SubstateModelUnavailable {
		t.Errorf("got %q, want %q", got, SubstateModelUnavailable)
	}
}

// Precedence: a busy spinner wins over an auth banner quoted in a retry notice
// (busy detection owns that transient state — same rule HasErrorBanner uses).
func TestClassifySubstate_RunningWinsOverQuotedRetry(t *testing.T) {
	d := NewPromptDetector("claude")
	content := "✻ Reticulating… (3s · ↑ 50 tokens · ctrl+c to interrupt)\n" +
		"  ⎿  API Error: 401 · Retrying in 4 seconds… (attempt 3/10)"
	if got := d.ClassifySubstate(content); got != SubstateRunning {
		t.Errorf("got %q, want %q", got, SubstateRunning)
	}
}

// Over-match guard: a user merely discussing a 401 at the prompt is idle, not
// auth-401 (mirrors HasErrorBanner's prose guard).
func TestClassifySubstate_DoesNotMisclassifyProse(t *testing.T) {
	d := NewPromptDetector("claude")
	content := "⏺ Done with the refactor.\n\n❯ why did the worker show API Error: 401 yesterday?"
	if got := d.ClassifySubstate(content); got == SubstateAuth401 {
		t.Errorf("prose about a 401 must not classify as auth-401, got %q", got)
	}
}

// Non-claude tools yield SubstateNone (heuristics are Claude renderings).
func TestClassifySubstate_NonClaudeReturnsNone(t *testing.T) {
	content := "Fable is currently unavailable\n❯ "
	for _, tool := range []string{"codex", "gemini", "opencode", "shell"} {
		if got := NewPromptDetector(tool).ClassifySubstate(content); got != SubstateNone {
			t.Errorf("tool %q: got %q, want %q", tool, got, SubstateNone)
		}
	}
}

// A spinner char left in SCROLLBACK (far above the recent tail) must NOT keep
// classifying the session as running and mask a current auth banner. Regression
// for the over-broad spinner search (Codex review, substate.go).
func TestClassifySubstate_StaleSpinnerDoesNotMaskAuth(t *testing.T) {
	d := NewPromptDetector("claude")
	// A real spinner line, then >15 lines of quiet output, then a live 401
	// banner at the bottom. The stale spinner is out of the recent-tail window.
	content := "✻ Reticulating… (3s · ctrl+c to interrupt)\n" +
		"l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12\nl13\nl14\nl15\nl16\n" +
		"Please run /login · API Error: 401 {\"type\":\"error\"}\n\n❯ "
	if got := d.ClassifySubstate(content); got != SubstateAuth401 {
		t.Errorf("stale spinner masked the live auth banner: got %q, want %q", got, SubstateAuth401)
	}
}

// A NON-zero "Crunched for Ns" completion line carries the asterisk glyph "✶"
// but is a COMPLETED turn sitting at the prompt, not active work. It must NOT
// classify as running (the asterisk alone is not a busy signal). Regression for
// the asterisk-completion false-positive (Codex review round 2).
func TestClassifySubstate_NonzeroCrunchedCompletionIsNotRunning(t *testing.T) {
	d := NewPromptDetector("claude")
	// A completed (non-zero) crunch line carries the "✶" glyph but is NOT active
	// work. The key guard: the asterisk alone must not classify it as running.
	// (The upstream prompt detector also treats a bare "✶" as a busy spinner, so
	// the result is SubstateNone rather than idle-at-empty-prompt — either way it
	// must not be running, which is the bug Codex flagged.)
	content := "✶ Crunched for 12s (1.2k tokens)\n\n❯ \n  ? for shortcuts"
	if got := d.ClassifySubstate(content); got == SubstateRunning {
		t.Errorf("a completed non-zero crunch at the prompt must not be running, got %q", got)
	}
}

// A live busy cue at the bottom must win over a STALE "Crunched for 0s" no-op
// line still sitting in the recent-tail window — the session is crunching now,
// the zero-crunch line is history. Regression for the precedence inversion
// (Codex review round 5).
func TestClassifySubstate_LiveBusyWinsOverStaleNoop(t *testing.T) {
	d := NewPromptDetector("claude")
	content := "✶ Crunched for 0s (0 tokens)\n" +
		"⏺ Reading the file now\n" +
		"✻ Reticulating… (4s · ↑ 12 tokens · ctrl+c to interrupt)"
	if got := d.ClassifySubstate(content); got != SubstateRunning {
		t.Errorf("a live busy cue must win over a stale zero-crunch line, got %q", got)
	}
}

// Empty / unrecognized content yields SubstateNone.
func TestClassifySubstate_None(t *testing.T) {
	d := NewPromptDetector("claude")
	for _, content := range []string{"", "some random build output\nmaking progress\n"} {
		if got := d.ClassifySubstate(content); got != SubstateNone {
			t.Errorf("content %q: got %q, want %q", content, got, SubstateNone)
		}
	}
}
