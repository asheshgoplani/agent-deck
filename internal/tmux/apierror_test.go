package tmux

import "testing"

// The captured 2026-08-07 pane. Both lines are real: the banner renders on an
// assistant-glyph line and the whimsical completion line follows it.
const apiErrorPaneCapture = `⏺ Read(internal/session/instance.go)
  ⎿  Read 40 lines

⏺ API Error: Unable to connect to API (ENOTFOUND)
✻ Sautéed for 39m 27s

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`

func claudeDetector() *PromptDetector { return NewPromptDetector("claude") }

// §6: the captured 2026-08-07 pane classifies api-error.
func TestClassifySubstate_TransportBanner_IsAPIError(t *testing.T) {
	if got := claudeDetector().ClassifySubstate(apiErrorPaneCapture); got != SubstateAPIError {
		t.Fatalf("captured transport pane: want %q, got %q", SubstateAPIError, got)
	}
}

// The ordinary Node/undici rendering names the host INSIDE the parens, so the
// code is not followed by ")". Matching on a literal closing paren would make
// the feature inert against the form a real outage most often produces.
func TestClassifySubstate_TransportBannerWithHost_IsAPIError(t *testing.T) {
	tail := "\n\n╭──────────────────────────────────────────╮\n│ ❯                                        │\n╰──────────────────────────────────────────╯"
	cases := []struct {
		name string
		line string
	}{
		{"ENOTFOUND with host", `⏺ API Error: Unable to connect to API (ENOTFOUND api.anthropic.com)`},
		{"ECONNREFUSED with address", `⏺ API Error: Unable to connect to API (ECONNREFUSED 127.0.0.1:443)`},
		{"ConnectionRefused with address", `⏺ API Error: Unable to connect to API (ConnectionRefused 127.0.0.1:443)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeDetector().ClassifySubstate(tc.line + tail); got != SubstateAPIError {
				t.Fatalf("%q: want %q, got %q", tc.line, SubstateAPIError, got)
			}
		})
	}
}

// A host-bearing banner quoted behind "⎿" stays rejected: dropping the closing
// paren must not weaken the conductor guard.
func TestClassifySubstate_QuotedTransportBannerWithHost_NotAPIError(t *testing.T) {
	quoted := `⏺ Bash(agent-deck session output worker-3)
  ⎿  ⏺ API Error: Unable to connect to API (ENOTFOUND api.anthropic.com)
  ⎿  ✻ Sautéed for 39m 27s

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(quoted); got == SubstateAPIError {
		t.Fatalf("a quoted host-bearing child banner must not classify api-error, got %q", got)
	}
}

// §6: a conductor quoting the banner behind "⎿" does not match.
func TestClassifySubstate_QuotedTransportBanner_NotAPIError(t *testing.T) {
	quoted := `⏺ Bash(agent-deck session output worker-3)
  ⎿  ⏺ API Error: Unable to connect to API (ENOTFOUND)
  ⎿  ✻ Sautéed for 39m 27s

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(quoted); got == SubstateAPIError {
		t.Fatalf("a quoted child banner must not classify api-error, got %q", got)
	}
}

// §6: assistant-line prose mentioning the banner does not match. No
// parenthesised transport code, no " · ", no error JSON — nothing structural.
func TestClassifySubstate_ProseAboutTransportBanner_NotAPIError(t *testing.T) {
	prose := `⏺ The worker showed API Error: Unable to connect to API and never recovered, so I restarted it.

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(prose); got == SubstateAPIError {
		t.Fatalf("assistant prose must not classify api-error, got %q", got)
	}
}

// Claude wraps assistant prose across pane lines and only the FIRST visual line
// carries the "⏺" glyph. A continuation line is therefore an ordinary non-quoted
// line, so a guard that only inspects glyph lines lets wrapped prose naming the
// banner phrase classify api-error with no structural check at all — and under
// mode="resume" that injects a continuation prompt into a healthy session. The
// transport scan requires the structural co-signal on EVERY non-quoted line.
func TestClassifySubstate_WrappedProseAboutTransportBanner_NotAPIError(t *testing.T) {
	tail := "\n\n╭──────────────────────────────────────────╮\n│ ❯                                        │\n╰──────────────────────────────────────────╯"
	cases := []struct {
		name  string
		prose string
	}{
		{
			"phrase on the wrapped continuation line",
			"⏺ The worker showed API Error:\n  Unable to connect to API and never recovered, so I restarted it.",
		},
		{
			"continuation line carrying the · separator too",
			"⏺ The worker wedged this morning:\n  Unable to connect to API · it recovered after a restart.",
		},
		{
			"phrase three lines below the glyph",
			"⏺ Here is what happened during the outage:\n  three of the workers stopped mid-turn and the pane showed\n  Unable to connect to API for about forty minutes.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeDetector().ClassifySubstate(tc.prose + tail); got == SubstateAPIError {
				t.Fatalf("wrapped assistant prose must not classify api-error, got %q", got)
			}
		})
	}
}

// The shape three rounds of fixes could not see: ONE prose line carrying BOTH
// the banner phrase AND a parenthesised transport code, because the assistant is
// SUMMARISING an incident and quotes the banner verbatim. Requiring the
// co-signal on every line does not help — the line has it. This fleet writes
// exactly these sentences: conductors report on wedged children, and
// docs/self-heal.md puts the literal strings in the repo the sessions work in.
//
// The discriminator is position: the real banner OPENS its line (optionally
// behind the "⏺" glyph); prose reaches the phrase mid-sentence.
//
// Known residual, deliberate: a wrapped continuation line that itself BEGINS
// "API Error:" is indistinguishable from the standalone banner rendering by
// content alone, and still classifies. That is a far narrower target than any
// sentence merely containing the phrase.
func TestClassifySubstate_ProseQuotingBannerWithCode_NotAPIError(t *testing.T) {
	tail := "\n\n╭──────────────────────────────────────────╮\n│ ❯                                        │\n╰──────────────────────────────────────────╯"
	cases := []struct {
		name  string
		prose string
	}{
		{
			"glyph line quoting the banner verbatim mid-sentence",
			`⏺ worker-3 showed "API Error: Unable to connect to API (ENOTFOUND api.anthropic.com)" so I restarted it.`,
		},
		{
			"glyph line narrating the outage with the code inline",
			`⏺ The 2026-08-07 outage rendered API Error: Unable to connect to API (ENOTFOUND) in three panes.`,
		},
		{
			"bulleted doc line naming the banner form",
			"⏺ The two renderings are:\n  - banner form: API Error: Unable to connect to API (ECONNREFUSED 127.0.0.1:443)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeDetector().ClassifySubstate(tc.prose + tail); got == SubstateAPIError {
				t.Fatalf("prose quoting the banner must not classify api-error, got %q", got)
			}
		})
	}
}

// The structural requirement on every non-quoted line must not make the real
// banner unmatchable in its STANDALONE (no assistant glyph) rendering — that
// form carries the parenthesised transport code and keeps matching.
func TestClassifySubstate_StandaloneTransportBanner_IsAPIError(t *testing.T) {
	standalone := `API Error: Unable to connect to API (ENOTFOUND api.anthropic.com)
✻ Sautéed for 39m 27s

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(standalone); got != SubstateAPIError {
		t.Fatalf("standalone transport banner: want %q, got %q", SubstateAPIError, got)
	}
}

// A bare transport-code token is NOT a banner. Under mode="resume" a false
// api-error injects a continuation prompt into a session that was never wedged,
// so the discriminator is the rendered wording plus the parenthesised code —
// never a token that ordinary output, test failures or SOURCE CODE can carry.
func TestClassifySubstate_BareTransportTokens_NotAPIError(t *testing.T) {
	tail := "\n\n╭──────────────────────────────────────────╮\n│ ❯                                        │\n╰──────────────────────────────────────────╯"
	cases := []struct {
		name string
		line string
	}{
		{"curl output", `curl: (7) Failed to connect: ECONNREFUSED`},
		{"go test failure", `api_test.go:41: dial tcp 127.0.0.1:8080: connect: ECONNREFUSED`},
		{"go source line", `if errors.Is(err, syscall.ECONNREFUSED) {`},
		{"assistant prose with · separator", `⏺ Fixed the ENOTFOUND lookup · all tests pass`},
		{"assistant prose quoting the banner with a · separator", `⏺ worker-3 showed Unable to connect to API · I restarted it`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeDetector().ClassifySubstate(tc.line + tail); got == SubstateAPIError {
				t.Fatalf("%q must not classify api-error, got %q", tc.line, got)
			}
		})
	}
}

// §6: a recovered pane carrying BOTH the banner and a live spinner classifies
// running. A transport error is recoverable, so the spinner is the truth.
func TestClassifySubstate_TransportBannerWithLiveSpinner_IsRunning(t *testing.T) {
	recovered := `⏺ API Error: Unable to connect to API (ENOTFOUND)
✻ Sautéed for 39m 27s
⠋ Reticulating… (12s · ↓ 431 tokens · esc to interrupt)`
	if got := claudeDetector().ClassifySubstate(recovered); got != SubstateRunning {
		t.Fatalf("recovered pane with a live spinner: want %q, got %q", SubstateRunning, got)
	}
}

// §6: a banner scrolled beyond the recent tail stops matching. The scan window
// is the last 15 NON-EMPTY lines, so 20 lines of later output bury it.
func TestClassifySubstate_TransportBannerScrolledAway_NotAPIError(t *testing.T) {
	buried := "⏺ API Error: Unable to connect to API (ENOTFOUND)\n"
	for i := 0; i < 20; i++ {
		buried += "⏺ Ordinary assistant output line\n"
	}
	buried += "╭──────────────────────────────────────────╮\n│ ❯                                        │\n╰──────────────────────────────────────────╯"
	if got := claudeDetector().ClassifySubstate(buried); got == SubstateAPIError {
		t.Fatalf("a banner scrolled out of the tail must not classify api-error, got %q", got)
	}
}

// §6: "API Error: 401" still classifies auth-401, not api-error. auth-401 is
// checked FIRST and the two marker sets are disjoint.
func TestClassifySubstate_Auth401_StillAuth401(t *testing.T) {
	auth := `⏺ API Error: 401 · Please run /login

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(auth); got != SubstateAuth401 {
		t.Fatalf("401 banner: want %q, got %q", SubstateAuth401, got)
	}
}

// The transport markers must not leak into the auth-hold decision: a transport
// failure is restart-recoverable and must stay outside the credential hold.
func TestIsAuthFailureContent_TransportBanner_NotAuthFailure(t *testing.T) {
	if IsAuthFailureContent("claude", apiErrorPaneCapture) {
		t.Fatal("a transport banner must never arm the credential auth hold")
	}
}

// A non-claude tool has no substate heuristics at all.
func TestClassifySubstate_NonClaude_NoAPIError(t *testing.T) {
	if got := NewPromptDetector("codex").ClassifySubstate(apiErrorPaneCapture); got != SubstateNone {
		t.Fatalf("non-claude tool: want %q, got %q", SubstateNone, got)
	}
}
