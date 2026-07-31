package tmux

import "testing"

// realLimitedPane is a verbatim capture of a Claude pane whose plan usage window
// was exhausted (2026-07-30 field evidence). The rejection renders behind the
// "⎿" tool-result connector and the turn completes in zero seconds. The
// zero-duration verb varies run to run ("Cooked", "Baked", "Crunched", …),
// which is why the fixtures below parameterise it.
func realLimitedPane(zeroDurationVerb string) string {
	return "❯ [HEARTBEAT] Check sessions in your group (intelas-conductor). List any that are waiting, auto-respond where safe,\n" +
		"  and report what needs my attention.\n" +
		"  ⎿  You've hit your session limit · resets 8:50pm (UTC)\n" +
		"     /usage-credits to request more usage from your admin.\n" +
		"\n" +
		"✻ " + zeroDurationVerb + " for 0s\n"
}

func TestIsUsageLimitContent(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		content string
		want    bool
	}{
		{
			name:    "real limited pane, session-limit wording",
			tool:    "claude",
			content: realLimitedPane("Cooked"),
			want:    true,
		},
		{
			name:    "usage-limit wording variant",
			tool:    "claude",
			content: "  ⎿  You've hit your usage limit · resets 3:00am (UTC)\n",
			want:    true,
		},
		{
			name:    "credits hint alone still identifies the banner",
			tool:    "claude",
			content: "     /usage-credits to request more usage from your admin.\n",
			want:    true,
		},
		{
			name: "user typing about a usage limit is not a limited session",
			tool: "claude",
			// The "❯" input prefix must keep this out of the verdict.
			content: "❯ why did I hit your session limit yesterday?\n",
			want:    false,
		},
		{
			name: "wrapped user question: phrase only on the continuation line",
			tool: "claude",
			// Regression for the review finding on #1803: only the FIRST visual
			// line of an input block carries "❯", so matching per-line read the
			// wrapped remainder of a question as a live banner.
			content: "❯ why did the worker stall yesterday, did we\n" +
				"  hit your session limit or something else?\n",
			want: false,
		},
		{
			name: "wrapped user question mentioning the credits hint on continuation",
			tool: "claude",
			content: "❯ what does it mean when claude tells me to run\n" +
				"  /usage-credits to get more quota?\n",
			want: false,
		},
		{
			name: "input block ends at the tool-result connector, banner still matches",
			tool: "claude",
			// The continuation-skip must stop at "⎿": in the real pane the banner
			// renders immediately below a wrapped user message.
			content: "❯ [HEARTBEAT] Check sessions in your group (x). List any that are waiting,\n" +
				"  and report what needs my attention.\n" +
				"  ⎿  You've hit your session limit · resets 8:50pm (UTC)\n",
			want: true,
		},
		{
			name: "blank line ends the input block",
			tool: "claude",
			content: "❯ was it a quota thing?\n" +
				"\n" +
				"  ⎿  You've hit your session limit · resets 8:50pm (UTC)\n",
			want: true,
		},
		{
			name: "agent prose about a usage limit is not a limited session",
			tool: "claude",
			// "⏺" assistant line with no structural banner marker: an agent
			// explaining the condition must not be read as being in it.
			content: "⏺ If a worker stalls, check whether you hit your session limit and run /usage-credits.\n",
			want:    false,
		},
		{
			name: "assistant line carrying the real banner still matches",
			tool: "claude",
			// The rendered banner keeps the " · " separator, which is the
			// structural co-signal that distinguishes it from prose.
			content: "⏺ You've hit your session limit · resets 8:50pm (UTC)\n",
			want:    true,
		},
		{
			name:    "healthy idle pane",
			tool:    "claude",
			content: "✻ Cooked for 3m 20s\n\n❯ \n",
			want:    false,
		},
		{
			name:    "auth failure is a different condition",
			tool:    "claude",
			content: "Invalid API key · Please run /login\n",
			want:    false,
		},
		{
			name: "ANSI-coloured banner still matches through the fast path",
			tool: "claude",
			// The fast reject strips ANSI first, so a colour escape landing
			// inside the matched token must not hide the banner.
			content: "  ⎿  You've hit your session li\x1b[31mmit\x1b[0m · resets 8:50pm (UTC)\n",
			want:    true,
		},
		{
			name:    "non-claude tool never matches",
			tool:    "codex",
			content: realLimitedPane("Cooked"),
			want:    false,
		},
		{
			name:    "banner scrolled out of the recent window",
			tool:    "claude",
			content: "  ⎿  You've hit your session limit · resets 8:50pm (UTC)\n" + repeatLines("some later output line", 20),
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUsageLimitContent(tc.tool, tc.content); got != tc.want {
				t.Fatalf("IsUsageLimitContent(%q) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

// TestClassifySubstate_UsageLimit is the regression that the fix exists for
// (#1802).
//
// Before the fix both fixtures were misclassified, and differently: the common
// verb yielded SubstateNone (a throttled session looked unremarkable, so
// periodic senders kept sending into it), and the "Crunched" verb collided with
// the Fable no-op marker and yielded SubstateModelUnavailable — a wrong
// diagnosis pointing at the model rather than the quota.
func TestClassifySubstate_UsageLimit(t *testing.T) {
	d := NewPromptDetector("claude")
	for _, verb := range []string{"Cooked", "Baked", "Crunched", "Churned", "Worked", "Cogitated"} {
		t.Run(verb, func(t *testing.T) {
			if got := d.ClassifySubstate(realLimitedPane(verb)); got != SubstateUsageLimit {
				t.Fatalf("ClassifySubstate(%q variant) = %q, want %q", verb, got, SubstateUsageLimit)
			}
		})
	}
}

// A genuine auth failure must still win: credentials being invalid is terminal
// and needs a different response than waiting for a quota window to reset.
func TestClassifySubstate_AuthWinsOverUsageLimit(t *testing.T) {
	d := NewPromptDetector("claude")
	content := "  ⎿  You've hit your session limit · resets 8:50pm (UTC)\n" +
		"Invalid API key · Please run /login\n"
	if got := d.ClassifySubstate(content); got != SubstateAuth401 {
		t.Fatalf("ClassifySubstate = %q, want %q", got, SubstateAuth401)
	}
}

// A live busy cue means the session is working NOW, so a limit banner still in
// the window is stale and must not win.
func TestClassifySubstate_BusyWinsOverStaleUsageLimit(t *testing.T) {
	d := NewPromptDetector("claude")
	content := "  ⎿  You've hit your session limit · resets 8:50pm (UTC)\n" +
		"✻ Thinking… (esc to interrupt)\n"
	if got := d.ClassifySubstate(content); got != SubstateRunning {
		t.Fatalf("ClassifySubstate = %q, want %q", got, SubstateRunning)
	}
}

// A real Fable no-op (no limit banner) must still classify as model-unavailable
// — the new branch must not swallow the condition it is ordered ahead of.
func TestClassifySubstate_ModelUnavailableStillWorks(t *testing.T) {
	d := NewPromptDetector("claude")
	if got := d.ClassifySubstate("✻ Crunched for 0s\n"); got != SubstateModelUnavailable {
		t.Fatalf("ClassifySubstate = %q, want %q", got, SubstateModelUnavailable)
	}
}

func repeatLines(line string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += line + "\n"
	}
	return out
}
