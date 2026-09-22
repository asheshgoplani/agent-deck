package tmux

import "testing"

// Status-light accuracy audit (2026-09-17, 92 live sessions on the v1.16.11
// candidate). Every pane below is the captured tail of a real session that the
// audit scored as a mismatch; account and secret text has been replaced with
// neutral placeholders. Each test names the audit defect it pins (A–F) and
// fails on the candidate tree before the corresponding fix.

// Idle Claude footer: a collapsed tool-output marker ("… +N lines") and the
// "/clear to save Nk tokens" hint are BOTH visible on almost every idle
// session that used tools. They sit on different lines and neither is work.
const auditIdleFooterPane = "⏺ Bash(f=~/meetings/notes.md; grep -oiE 'alpha|beta|gamma' \"$f\" | sort | uniq -c)\n" +
	"     … +63 lines (ctrl+o to expand)\n" +
	"⏺ Filed. The 15:00 meeting is in the log.\n" +
	"✻ Cogitated for 24s · done 5:10 PM\n" +
	"──────────────────────────────────────────────────── meeting-watcher ─\n" +
	"❯ \n" +
	"──────────────────────────────────────────────────────────────────────\n" +
	"  [profile] user@host:~/.agent-deck/watcher/meeting-watcher | [Opus 5 (1M context)] ctx:21% in:209.2k out:192\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
	"                                              ✔ Update installed · Restart to update\n" +
	"                                              new task? /clear to save 209.4k tokens\n" +
	"                                                                                 /rc"

// Defect F: hasClaudeBusyIndicator credited "…" and "tokens" ANYWHERE in the
// last 15 lines as active work. 11 of the 92 audited sessions carried the
// substate "running" while sitting idle at the prompt. The timing cue is only
// evidence of work when both fragments are on the SAME line, exactly as the
// coarse status check (hasInterruptBusyContext) already requires.
func TestAudit_F_IdleFooterEllipsisTokensIsNotRunning(t *testing.T) {
	d := NewPromptDetector("claude")
	if d.hasClaudeBusyIndicator(auditIdleFooterPane) {
		t.Fatal("idle footer ('… +N lines' + '/clear to save Nk tokens' on different lines) must not read as busy")
	}
	if got := d.ClassifySubstate(auditIdleFooterPane); got != SubstateIdleAtEmptyPrompt {
		t.Fatalf("substate = %q, want %q", got, SubstateIdleAtEmptyPrompt)
	}
	if !d.hasClaudePrompt(auditIdleFooterPane) {
		t.Fatal("coarse prompt detection must see the empty prompt through the same footer")
	}
}

// Defect F, positive control: a genuine timing line carries "…" and "tokens"
// together, with or without the interrupt hint, and must still be busy.
func TestAudit_F_SameLineTimingCueStillBusy(t *testing.T) {
	d := NewPromptDetector("claude")
	cases := []struct {
		name    string
		content string
	}{
		{"whimsical word with token timing", "✢ Hullaballooing… (53s · ↓ 749 tokens)\n\n❯ "},
		{"timing line above idle footer", "✻ Reticulating… (12s · ↑ 1.2k tokens)\n  new task? /clear to save 209.4k tokens\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !d.hasClaudeBusyIndicator(tc.content) {
				t.Fatalf("same-line '… tokens' timing must read as busy")
			}
			if got := d.ClassifySubstate(tc.content); got != SubstateRunning {
				t.Fatalf("substate = %q, want %q", got, SubstateRunning)
			}
			if d.hasClaudePrompt(tc.content) {
				t.Fatal("a live timing line must suppress prompt detection")
			}
		})
	}
}

// Defect A: the Codex usage-limit banner. The composer ("› Ask Codex to do
// anything") is redrawn under the banner, so prompt detection alone reads
// this as an ordinary idle prompt. Captured from a local codex session.
const auditCodexUsageLimitPane = "  Treat this as context only; do not claim native resume.\n" +
	"■ You've hit your usage limit. To continue using Codex and get access to GPT-5.3-Codex, start a free\n" +
	"trial of Plus today (https://chatgpt.com/explore/plus), or try again at Oct 10th, 2026 8:03 AM.\n" +
	"› Without tools, recall the exact ROUNDTRIP token from the transferred conversation, then\n" +
	"  continue by appending the word CONTINUED on the next line.\n" +
	"■ You've hit your usage limit. To continue using Codex and get access to GPT-5.3-Codex, start a free\n" +
	"trial of Plus today (https://chatgpt.com/explore/plus), or try again at Oct 10th, 2026 8:03 AM.\n" +
	"› Ask Codex to do anything\n" +
	"  gpt-5.6-luna · ~/work/disposable-project · Context 100% left…"

func TestAudit_A_CodexUsageLimitBannerIsError(t *testing.T) {
	d := NewPromptDetector("codex")
	if !d.HasErrorBanner(auditCodexUsageLimitPane) {
		t.Fatal("codex usage-limit banner must be an error banner (light: error, not waiting)")
	}
	if got := d.ClassifySubstate(auditCodexUsageLimitPane); got != SubstateUsageLimit {
		t.Fatalf("substate = %q, want %q", got, SubstateUsageLimit)
	}
	if got := d.SubstateDetail(auditCodexUsageLimitPane); got != "try again at Oct 10th, 2026 8:03 AM" {
		t.Fatalf("detail = %q, want the retry time from the banner", got)
	}
	sess := &Session{Command: "codex"}
	if !sess.hasErrorBannerIndicator(auditCodexUsageLimitPane) {
		t.Fatal("GetStatus's banner gate must see the codex banner")
	}
}

func TestAudit_A_CodexLoginBannerIsAuthError(t *testing.T) {
	d := NewPromptDetector("codex")
	content := "■ Your access token could not be refreshed. Please run `codex login` again.\n" +
		"› Ask Codex to do anything\n" +
		"  gpt-5.6-luna · ~/work · Context 100% left…"
	if !d.HasErrorBanner(content) {
		t.Fatal("codex login-required banner must be an error banner")
	}
	if got := d.ClassifySubstate(content); got != SubstateAuth401 {
		t.Fatalf("substate = %q, want %q", got, SubstateAuth401)
	}
	if got := d.SubstateDetail(content); got != "" {
		t.Fatalf("detail = %q, want none for an auth banner", got)
	}
}

// Over-match guards for A: prose mentioning the banner text, a user typing
// about it in the composer, and a scrolled-out banner stay non-error.
func TestAudit_A_CodexBannerDoesNotOverMatch(t *testing.T) {
	d := NewPromptDetector("codex")
	cases := []struct {
		name    string
		content string
	}{
		{"user typing about the limit", "• Done.\n› tell me what to do when I've hit your usage limit\n  gpt-5.6 · ~/work"},
		{"assistant prose about codex login", "• If that happens, run codex login and retry.\n› Ask Codex to do anything\n  gpt-5.6 · ~/work"},
		{"banner scrolled out of the window",
			"■ You've hit your usage limit. Try again at Oct 10th, 2026 8:03 AM.\n" +
				"l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12\nl13\nl14\nl15\nl16\n" +
				"› Ask Codex to do anything"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d.HasErrorBanner(tc.content) {
				t.Fatalf("must not read as an error banner")
			}
			if got := d.ClassifySubstate(tc.content); got == SubstateUsageLimit || got == SubstateAuth401 {
				t.Fatalf("substate = %q, want no error substate", got)
			}
		})
	}
}

// Review P1-2 (round 2): the codex banner gate needs the same "newest signal
// wins" rule as the Claude scans. After the rate-limit picker (audit E) the
// user keeps working in the same session, or logs in from another terminal
// and retries; the "■" banner then sits a few lines above the new turn. A
// banner followed by a later submitted prompt, an assistant/tool line or a
// live busy cue is history; a banner directly above the composer is current.
func TestAudit_A_CodexBannerClearedByLaterTurn(t *testing.T) {
	d := NewPromptDetector("codex")
	banner := "■ You've hit your usage limit. To continue using Codex, start a free\n" +
		"trial of Plus today (https://chatgpt.com/explore/plus), or try again at Oct 10th, 2026 8:03 AM.\n"
	cleared := []struct {
		name    string
		content string
	}{
		{"later submitted prompt and reply",
			banner + "› continue with the rebase\n" +
				"• Rebased onto main; 3 commits replayed.\n" +
				"› Ask Codex to do anything\n" +
				"  gpt-5.6-luna · ~/work · Context 98% left"},
		{"later submitted prompt, reply still streaming",
			banner + "› continue with the rebase\n" +
				"• Working (12s · esc to interrupt)\n" +
				"› Ask Codex to do anything"},
		{"assistant tool line after the banner",
			banner + "• Ran git status\n" +
				"› Ask Codex to do anything"},
		{"live busy cue in the tail",
			banner + "  Thinking (3s · ctrl+c to interrupt)\n"},
	}
	for _, tc := range cleared {
		t.Run(tc.name, func(t *testing.T) {
			if d.HasErrorBanner(tc.content) {
				t.Fatal("a codex banner the session has moved past must not keep it in error")
			}
			if got := d.ClassifySubstate(tc.content); got == SubstateUsageLimit || got == SubstateAuth401 {
				t.Fatalf("substate = %q, want no error substate", got)
			}
			if got := d.SubstateDetail(tc.content); got != "" {
				t.Fatalf("detail = %q, want none once the banner is history", got)
			}
		})
	}
	current := []struct {
		name    string
		content string
	}{
		{"banner directly above the empty composer", banner + "› Ask Codex to do anything\n  gpt-5.6-luna · ~/work"},
		{"banner above typed-but-unsent composer text", banner + "› what now\n  gpt-5.6-luna · ~/work"},
		{"banner as the last thing on screen", banner},
	}
	for _, tc := range current {
		t.Run(tc.name, func(t *testing.T) {
			if !d.HasErrorBanner(tc.content) {
				t.Fatal("a codex banner with no later turn is current")
			}
			if got := d.ClassifySubstate(tc.content); got != SubstateUsageLimit {
				t.Fatalf("substate = %q, want %q", got, SubstateUsageLimit)
			}
		})
	}
}

// Review round 3 P3-6: a "■ … usage limit" line INSIDE tool output (a cat or
// grep of a file that quotes the banner) is content, not a banner. Tool
// output sits below its "• Ran …" line — "  └ first line", then lines
// indented to the same column — so the "•" stop alone never excludes it when
// the block sits directly above the composer. Codex prints its own banners
// at column 0; only a column-0 "■" counts.
func TestAudit_A_CodexBannerQuotedInToolOutputIsNotError(t *testing.T) {
	d := NewPromptDetector("codex")
	composer := "› Ask Codex to do anything\n  gpt-5.6-luna · ~/work · Context 97% left"
	quoted := []struct {
		name    string
		content string
	}{
		{"cat output: banner on the └ line",
			"• Ran cat notes/limits.txt\n" +
				"  └ ■ You've hit your usage limit. To continue using Codex, start a free\n" +
				"    trial of Plus today (https://chatgpt.com/explore/plus), or try again at Oct 10th, 2026 8:03 AM.\n" +
				composer},
		{"cat output: banner on a later indented line",
			"• Ran cat notes/limits.txt\n" +
				"  └ Seen on 2026-09-17:\n" +
				"    ■ You've hit your usage limit. Try again at Oct 10th, 2026 8:03 AM.\n" +
				"    ■ You are not logged in. Run `codex login` to continue.\n" +
				composer},
		{"cat output: banner as the last thing on screen",
			"• Ran cat notes/limits.txt\n" +
				"  └ ■ You've hit your usage limit. Try again at Oct 10th, 2026 8:03 AM."},
	}
	for _, tc := range quoted {
		t.Run(tc.name, func(t *testing.T) {
			if d.HasErrorBanner(tc.content) {
				t.Fatal("a banner quoted inside tool output must not be an error")
			}
			if got := d.ClassifySubstate(tc.content); got == SubstateUsageLimit || got == SubstateAuth401 {
				t.Fatalf("substate = %q, want no error substate", got)
			}
			if got := d.SubstateDetail(tc.content); got != "" {
				t.Fatalf("detail = %q, want none", got)
			}
		})
	}
	// The real banner, printed by Codex at column 0 after the tool block,
	// is still current.
	real := "• Ran cat notes/limits.txt\n" +
		"  └ nothing here\n" +
		"■ You've hit your usage limit. To continue using Codex, start a free\n" +
		"trial of Plus today (https://chatgpt.com/explore/plus), or try again at Oct 10th, 2026 8:03 AM.\n" +
		composer
	if !d.HasErrorBanner(real) {
		t.Fatal("a column-0 banner below the tool block is current")
	}
	if got := d.ClassifySubstate(real); got != SubstateUsageLimit {
		t.Fatalf("substate = %q, want %q", got, SubstateUsageLimit)
	}
	if got := d.SubstateDetail(real); got != "try again at Oct 10th, 2026 8:03 AM" {
		t.Fatalf("detail = %q", got)
	}
}

// Review P2-7: only Codex's exact login-required wording is an auth banner.
// A "■" warning that merely mentions authentication (an MCP server, a git
// remote) must never become auth-401, because that verdict feeds the fleet
// auth gate. Such a banner degrades to the pre-existing waiting verdict.
func TestAudit_A_CodexAuthPatternsAreNarrow(t *testing.T) {
	d := NewPromptDetector("codex")
	notAuth := []string{
		"■ Warning: authentication for MCP server \"jira\" failed; continuing without it.\n› Ask Codex to do anything",
		"■ git push rejected: unauthorized (remote requires a token).\n› Ask Codex to do anything",
		"■ Sign in to GitHub in your browser to enable code search.\n› Ask Codex to do anything",
		"■ Could not log in to the registry; skipping package metadata.\n› Ask Codex to do anything",
	}
	for _, content := range notAuth {
		if d.HasErrorBanner(content) {
			t.Fatalf("must not be an error banner: %q", content)
		}
		if got := d.ClassifySubstate(content); got != SubstateNone {
			t.Fatalf("substate = %q for %q, want none", got, content)
		}
	}
	auth := []string{
		"■ Your access token could not be refreshed. Please run `codex login` again.\n› Ask Codex to do anything",
		"■ You are not logged in. Run `codex login` to continue.\n› Ask Codex to do anything",
	}
	for _, content := range auth {
		if !d.HasErrorBanner(content) {
			t.Fatalf("login-required banner must be an error: %q", content)
		}
		if got := d.ClassifySubstate(content); got != SubstateAuth401 {
			t.Fatalf("substate = %q for %q, want %q", got, content, SubstateAuth401)
		}
	}
}

// Defect C: the session recovered (a later completed turn sits BELOW the
// banner and the zero-work "Crunched for 0s" line) but stayed error /
// model-unavailable because the scan window still contained the stale lines.
// Newest signal wins: anything above a later completed turn is history.
const auditRecoveredAfterBannerPane = "⏺ SEEDED_TOKEN_PLACEHOLDER\n" +
	"✻ Cooked for 2s · done 8:25 PM\n" +
	"❯ No tools. Recall the exact ROUNDTRIP token from earlier. Reply with only the token.\n" +
	"  ⎿  1 agent type available\n" +
	"  ⎿  16 skills available\n" +
	"⏺ Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic\n" +
	"  API key instead, or ask your admin to enable access\n" +
	"✻ Crunched for 0s · done 8:25 PM\n" +
	"❯ No tools. Recall the exact ROUNDTRIP token from earlier. Reply with only the token.\n" +
	"⏺ ROUNDTRIP_TOKEN_PLACEHOLDER\n" +
	"✻ Worked for 2s · done 8:26 PM\n" +
	"──────────────────────────────────────────────── switch-fresh ─\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────\n" +
	"  [profile] user@host:~/work/disposable-project\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
	"                                  ✔ Update installed · Restart to update\n" +
	"                                                                     /rc"

func TestAudit_C_ErrorClearsAfterLaterCompletedTurn(t *testing.T) {
	d := NewPromptDetector("claude")
	if hasModelUnavailableNoop(auditRecoveredAfterBannerPane) {
		t.Fatal("a 'Crunched for 0s' line above a later completed turn is history, not state")
	}
	if got := d.ClassifySubstate(auditRecoveredAfterBannerPane); got != SubstateIdleAtEmptyPrompt {
		t.Fatalf("substate = %q, want %q", got, SubstateIdleAtEmptyPrompt)
	}

	// Same rule for the #1400 auth banner: recovered after a later turn.
	recovered := "⏺ Please run /login · API Error: 401 {\"type\":\"error\"}\n" +
		"❯ retry\n" +
		"⏺ Back online.\n" +
		"✻ Worked for 3s · done 8:30 PM\n" +
		"❯ "
	if d.HasErrorBanner(recovered) {
		t.Fatal("an auth banner above a later completed turn must not keep the session in error")
	}
	if got := d.ClassifySubstate(recovered); got != SubstateIdleAtEmptyPrompt {
		t.Fatalf("substate = %q, want %q", got, SubstateIdleAtEmptyPrompt)
	}
}

// Review P1-1 (round 2): the boundary is a LATER submitted prompt, never the
// failed turn's own summary line. Claude prints the turn summary BELOW an
// error banner (the recovered tail above shows "⏺ Your organization has
// disabled …" then "✻ Crunched for 0s · done"), so a turn that fails after
// doing work ends "⏺ Please run /login · API Error: 401 …" / "✻ Worked for
// 45s · done" / "❯ " — the mid-turn OAuth-expiry shape #1400 and the fleet
// auth hold exist for. That banner is current: error, auth-401, hold armed.
const auditFailedLastTurnPane = "❯ run the release checklist and report\n" +
	"⏺ Bash(make verify)\n" +
	"  ⎿  ok\n" +
	"⏺ Please run /login · API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"OAuth token has expired\"}}\n" +
	"✻ Worked for 45s · done 9:41 PM\n" +
	"──────────────────────────────────────────────────── worker ─\n" +
	"❯ \n" +
	"──────────────────────────────────────────────────────────────\n" +
	"  [profile] user@host:~/work | [Opus 5 (1M context)] ctx:12%\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents"

func TestAudit_C_FailedLastTurnBannerIsCurrent(t *testing.T) {
	d := NewPromptDetector("claude")
	if !d.HasErrorBanner(auditFailedLastTurnPane) {
		t.Fatal("a 401 banner inside the LAST turn is current even though the turn's summary line follows it")
	}
	if got := d.ClassifySubstate(auditFailedLastTurnPane); got != SubstateAuth401 {
		t.Fatalf("substate = %q, want %q", got, SubstateAuth401)
	}
	if !IsAuthFailureContent("claude", auditFailedLastTurnPane) {
		t.Fatal("the fleet auth hold must arm on a mid-turn 401 (IsAuthFailureContent)")
	}
	sess := &Session{Command: "claude"}
	if !sess.hasErrorBannerIndicator(auditFailedLastTurnPane) {
		t.Fatal("GetStatus's banner gate must route this frame to error")
	}

	// Same rule for the model-unavailable no-op: the failed turn's own
	// "Crunched for 0s" summary is not a recovery.
	failedNoop := "❯ recall the token\n" +
		"⏺ Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic\n" +
		"  API key instead, or ask your admin to enable access\n" +
		"✻ Crunched for 0s · done 8:25 PM\n" +
		"❯ "
	if !hasModelUnavailableNoop(failedNoop) {
		t.Fatal("the no-op banner of the last turn is current")
	}
	if got := d.ClassifySubstate(failedNoop); got != SubstateModelUnavailable {
		t.Fatalf("substate = %q, want %q", got, SubstateModelUnavailable)
	}

	// Typed-but-unsent text in the input box is not a new turn: the banner
	// above it stays current.
	typing := "⏺ Please run /login · API Error: 401 {\"type\":\"error\"}\n" +
		"✻ Worked for 45s · done 9:41 PM\n" +
		"❯ what happened"
	if !d.HasErrorBanner(typing) {
		t.Fatal("text typed into the input box is not a submitted prompt; the banner above it is still current")
	}
}

// Defect C, guards: the completed-turn boundary must not clear a banner that
// is NEWER than the completed turn, and the zero-work "Crunched for 0s"
// line is itself the no-op marker, never a recovery boundary.
func TestAudit_C_BoundaryDoesNotClearNewerSignals(t *testing.T) {
	d := NewPromptDetector("claude")
	bannerAfterTurn := "✻ Worked for 2s · done 8:26 PM\n" +
		"❯ next\n" +
		"⏺ Please run /login · API Error: 401 {\"type\":\"error\"}\n" +
		"❯ "
	if !d.HasErrorBanner(bannerAfterTurn) {
		t.Fatal("a banner BELOW the last completed turn is current")
	}
	noopAfterTurn := "✻ Worked for 2s · done 8:26 PM\n" +
		"❯ next\n" +
		"⏺ Fable is currently unavailable. Please try again later.\n" +
		"✻ Crunched for 0s · done 8:27 PM\n" +
		"❯ "
	if got := d.ClassifySubstate(noopAfterTurn); got != SubstateModelUnavailable {
		t.Fatalf("substate = %q, want %q (no-op BELOW the last real turn is current)", got, SubstateModelUnavailable)
	}
}

// Defect D: the end-of-session feedback survey is an open picker. Captured
// from a local session (light idle/waiting, substate "running" before the fix
// because the idle footer tripped defect F).
const auditFeedbackSurveyPane = "⏺ Monitor(g14 WhatsApp watcher: hard failures now, or ping queue stuck over 30 min (hourly))\n" +
	"  ⎿  Monitor started · task bcy8fwh7v · persistent\n" +
	"  ⎿  Allowed by auto mode classifier\n" +
	"⏺ Monitor replaced. Hard failures alert immediately; an occupied composer only alerts once per hour.\n" +
	"✻ Crunched for 18s · done 5:47 PM · 1 monitor still running\n" +
	"● How is Claude doing this session? (optional)\n" +
	"  1: Bad    2: Fine   3: Good   0: Dismiss\n" +
	"                                              new task? /clear to save 247.2k tokens\n" +
	"──────────────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"──────────────────────────────────────────────────────────────────────\n" +
	"  [profile] user@host:~/.agent-deck/watcher/whatsapp | [Opus 5 (1M context)] ctx:25% in:247.2k out:81\n" +
	"  ⏵⏵ auto mode on · 1 monitor · ← for agents"

func TestAudit_D_FeedbackSurveyIsInteractiveMenu(t *testing.T) {
	d := NewPromptDetector("claude")
	if !d.hasClaudePrompt(auditFeedbackSurveyPane) {
		t.Fatal("an open survey is waiting on input (coarse: waiting)")
	}
	if got := d.ClassifySubstate(auditFeedbackSurveyPane); got != SubstateInteractiveMenu {
		t.Fatalf("substate = %q, want %q", got, SubstateInteractiveMenu)
	}
}

// Defect E (claude half): the first-run trust dialog. Captured verbatim.
const auditTrustFolderPane = " Accessing workspace:\n" +
	" /Users/user\n" +
	" Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review\n" +
	" what's in this folder first.\n" +
	" Claude Code'll be able to read, edit, and execute files here.\n" +
	" Security guide\n" +
	" ❯ No, exit\n" +
	"   Yes, I trust this folder\n" +
	" Enter to confirm · Esc to cancel"

func TestAudit_E_TrustFolderDialogIsInteractiveMenu(t *testing.T) {
	d := NewPromptDetector("claude")
	if !d.hasClaudePrompt(auditTrustFolderPane) {
		t.Fatal("trust dialog is waiting on input")
	}
	if got := d.ClassifySubstate(auditTrustFolderPane); got != SubstateInteractiveMenu {
		t.Fatalf("substate = %q, want %q", got, SubstateInteractiveMenu)
	}
}

// Defect E (codex half): the model-switch / rate-limit picker. ClassifySubstate
// was Claude-only, so codex could never report an open menu. The gate stays
// explicit per tool: codex gets its own markers, every other tool stays None.
const auditCodexModelPickerPane = "  Your current model is rate limited for this account.\n" +
	"› 1. Switch to gpt-5.6-luna                 Fast and affordable agentic coding model.\n" +
	"  2. Keep current model\n" +
	"  Press enter to confirm or esc to go back"

func TestAudit_E_CodexModelPickerIsInteractiveMenu(t *testing.T) {
	d := NewPromptDetector("codex")
	if !d.HasPrompt(auditCodexModelPickerPane) {
		t.Fatal("codex picker is waiting on input (coarse: waiting)")
	}
	if got := d.ClassifySubstate(auditCodexModelPickerPane); got != SubstateInteractiveMenu {
		t.Fatalf("substate = %q, want %q", got, SubstateInteractiveMenu)
	}
	// Unknown / other tools stay unknown: no guessing from Claude or codex
	// phrasing on a tool whose renderings we have not captured.
	for _, tool := range []string{"pi", "gemini", "shell", "opencode"} {
		if got := NewPromptDetector(tool).ClassifySubstate(auditCodexModelPickerPane); got != SubstateNone {
			t.Errorf("tool %q: substate = %q, want none", tool, got)
		}
	}
	// A codex composer with no banner and no picker is still None — codex has
	// no idle-at-empty-prompt heuristic and must not borrow Claude's.
	if got := d.ClassifySubstate("› Ask Codex to do anything\n  gpt-5.6 · ~/work"); got != SubstateNone {
		t.Errorf("plain codex composer: substate = %q, want none", got)
	}
}

// Defect B, pane half: the "completed turn at an idle prompt" verdict the
// hook-lag rule in session.UpdateStatus consumes. It must be true for the
// captured conductor pane (hook file still said running 8 minutes after this
// frame) and false whenever a live busy cue, an open menu or pending
// background work is on screen.
const auditCompletedTurnPane = "⏺ Bash(git -C ~/agent-deck log --oneline -3)\n" +
	"     … +24 lines (ctrl+o to expand)\n" +
	"⏺ Both children reported back; nothing else is pending.\n" +
	"✻ Sautéed for 3m 4s · done 9:08 PM\n" +
	"──────────────────────────────────────────────── conductor-agent-deck ─\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  [profile] user@host:~/.agent-deck/conductor/agent-deck | [Opus 5 (1M context)] ctx:38% in:380.8k out:1.2k\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
	"                                                new task? /clear to save 380.8k tokens"

func TestAudit_B_CompletedTurnAtIdlePrompt(t *testing.T) {
	d := NewPromptDetector("claude")
	if !d.CompletedTurnAtIdlePrompt(auditCompletedTurnPane) {
		t.Fatal("completed turn + empty prompt + no busy cue must read as a finished turn")
	}
	notFinished := []struct {
		name    string
		content string
	}{
		{"live spinner below the completion line", auditCompletedTurnPane + "\n✻ Reticulating… (3s · ↑ 50 tokens · ctrl+c to interrupt)"},
		{"awaiting a background agent", "✻ Waiting for 1 background agent to finish\n❯ "},
		{"open menu, not an idle prompt", auditFeedbackSurveyPane},
		{"no completion line at all", "⏺ Working on it.\n❯ "},
		{"user typed into the prompt", "✻ Worked for 2s · done 8:26 PM\n❯ next question"},
		{"zero-work no-op is not a completed turn", "✻ Crunched for 0s · done 8:25 PM\n❯ "},
	}
	for _, tc := range notFinished {
		t.Run(tc.name, func(t *testing.T) {
			if d.CompletedTurnAtIdlePrompt(tc.content) {
				t.Fatal("must not read as a finished turn at an idle prompt")
			}
		})
	}
	// Background shells left alive do not keep the turn open (status-detection
	// audit 2026-09-23): the turn IS finished, the operator can act, and a
	// hook still saying running over this frame is lagging.
	if !d.CompletedTurnAtIdlePrompt("✻ Churned for 6m 24s · done 4:36 PM · 2 shells still running\n❯ ") {
		t.Fatal("a finished turn with background shells alive is still a finished turn")
	}
	if NewPromptDetector("codex").CompletedTurnAtIdlePrompt(auditCompletedTurnPane) {
		t.Fatal("the completed-turn shape is Claude's; other tools stay false")
	}
}
