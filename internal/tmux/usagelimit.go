package tmux

import "strings"

// Usage-limit detection (#1802).
//
// A Claude session that has exhausted its plan's rolling usage window is
// healthy in every way agent-deck can observe: the pane is alive, the composer
// accepts input, and every send is typed AND submitted successfully. What fails
// is one layer further in — the turn itself is rejected by the API and
// completes in zero seconds. Field evidence (2026-07-30), verbatim:
//
//	❯ [HEARTBEAT] Check sessions in your group (intelas-conductor). …
//	  ⎿  You've hit your session limit · resets 8:50pm (UTC)
//	     /usage-credits to request more usage from your admin.
//
//	✻ Cooked for 0s
//
// Nine periodic sends were delivered and bounced this way over 4h24m. Each one
// exited 0 — correctly, because delivery genuinely succeeded — and the coarse
// status stayed "idle", which is exactly the state periodic senders require in
// order to keep sending. Nothing in the stack reported a problem, because every
// layer's success criterion stops at "the text reached the composer".
//
// Detection deliberately differs from IsAuthFailureContent in one way: it does
// NOT skip the "⎿" tool-result connector. That prefix is where Claude renders
// this particular rejection, so the auth guard's blanket "⎿" skip would miss
// every real occurrence. User-input prefixes ("❯", ">", "│") are still skipped
// so a human typing ABOUT a limit never matches.
//
// Residual known limitation, accepted deliberately: pane text alone cannot
// distinguish "this session is throttled" from "this session is displaying
// another session's limit banner" (e.g. a conductor reading a child's pane via
// `session output`, which also renders behind "⎿"). Detection still wins that
// trade because Substate is ADDITIVE — it never changes the canonical status
// string, so a false positive is a mislabel, not a wrong action — whereas
// skipping "⎿" would make the detector unable to fire at all.

// usageLimitBannerPatterns are the rendered fragments that mean "this account's
// usage window is exhausted". Anchored on the rendered phrasing rather than
// bare tokens like "limit" so ordinary conversation does not match.
//
// The first two are observed field evidence. "usage limit" is the same banner
// family for plans that word it that way; "/usage-credits" is the actionable
// hint Claude renders directly beneath the banner and is a Claude-rendered
// string rather than something a user would type mid-sentence.
var usageLimitBannerPatterns = []string{
	"hit your session limit",
	"hit your usage limit",
	"/usage-credits",
}

// usageLimitInputPrefixes mark the FIRST visual line of a user-typed block:
// someone asking ABOUT a usage limit must not be read as being subject to one.
// This is claudeQuotedLinePrefixes MINUS "⎿", because "⎿" is the connector
// Claude renders the real banner behind (see the package comment above).
//
// A prefix alone is not enough: a wrapped question puts its later visual lines
// below the prefixed one with no marker of their own, so matching per-line would
// read the continuation of "why did we ❯ hit your session limit?" as a live
// banner. hasUsageLimitBanner therefore tracks input blocks rather than lines.
var usageLimitInputPrefixes = []string{"❯", ">", "│"}

// usageLimitBlockEndPrefixes are the glyphs Claude renders at message level. Any
// of them means the user's input block has ended and what follows is
// tool-rendered again, so continuation-skipping must stop there: "⎿" is the
// tool-result connector the banner itself renders behind, "⏺" an assistant turn,
// "✻"/"●" a completion or tool-call line.
var usageLimitBlockEndPrefixes = []string{"⎿", "⏺", "✻", "●"}

// hasUsageLimitBanner looks for a usage-limit banner in the last 15 non-empty
// lines — the same window as hasClaudePrompt and scanClaudeBannerLines.
//
// Unlike those two it walks FORWARD, because whether a line is a continuation of
// user input is only knowable from the line above it. A reverse scan meets the
// continuation first and cannot classify it.
func hasUsageLimitBanner(content string) bool {
	// Fast reject before the line walk: ClassifySubstate runs per session per
	// poll, and the scan below allocates (Split) and can touch 15 lines. Every
	// pattern contains one of these two tokens, so a pane holding neither cannot
	// match. Stripping ANSI once up front (a no-op returning the same string
	// when there are no escapes) keeps a colour escape inside the banner from
	// hiding the token from this fast path, and lets the loop skip the per-line
	// StripANSI it would otherwise need.
	stripped := StripANSI(content)
	if !strings.Contains(stripped, "limit") && !strings.Contains(stripped, "/usage-credits") {
		return false
	}

	all := strings.Split(stripped, "\n")

	// Window: the last 15 non-empty lines, kept in document order so the walk
	// below can see each line's predecessor. Blank lines stay in the window
	// because a blank line terminates a user-input block.
	start := len(all)
	nonEmpty := 0
	for i := len(all) - 1; i >= 0 && nonEmpty < 15; i-- {
		if strings.TrimSpace(all[i]) != "" {
			nonEmpty++
		}
		start = i
	}

	// inUserInput is true while walking the visual lines of a user-typed block:
	// set by an input prefix, cleared by a blank line or any message-level glyph.
	// Continuations inside such a block are skipped, which is what keeps a
	// wrapped question about a limit from reading as a live banner.
	inUserInput := false
	for _, raw := range all[start:] {
		line := strings.TrimSpace(raw)
		if line == "" {
			inUserInput = false
			continue
		}
		if hasAnyPrefix(line, usageLimitInputPrefixes) {
			inUserInput = true
			continue
		}
		if hasAnyPrefix(line, usageLimitBlockEndPrefixes) {
			inUserInput = false
		} else if inUserInput {
			continue
		}
		// Same guard scanClaudeBannerLines applies: on an assistant-turn line,
		// require a structural banner marker so an agent writing prose about a
		// usage limit ("you should check /usage-credits") is not read as being
		// subject to one. The real banner carries the " · " segment separator.
		if strings.HasPrefix(line, claudeAssistantLinePrefix) && !containsAny(line, claudeBannerStructuralMarkers) {
			continue
		}
		for _, pat := range usageLimitBannerPatterns {
			if strings.Contains(line, pat) {
				return true
			}
		}
	}
	return false
}

// IsUsageLimitContent reports whether the pane content shows a tool-rendered
// usage/quota exhaustion banner — the account's rolling usage window is spent
// and no turn can make progress until it resets.
//
// Distinct from IsAuthFailureContent: credentials are fine, so re-authenticating
// does nothing and holding the session out of every boot path is wrong. The
// condition is self-resolving at a known time, so the right response is to stop
// sending until then, not to restart or re-login.
//
// Claude-compatible renderings only; any other tool returns false. Callers pass
// the tool name as resolved for prompt detection (see inferToolFromSessionFields).
func IsUsageLimitContent(tool, content string) bool {
	if strings.ToLower(strings.TrimSpace(tool)) != "claude" {
		return false
	}
	return hasUsageLimitBanner(content)
}

// IsUsageLimit reports whether this detector's tool would render the given
// content as a usage-limit banner. Method form for call sites that already hold
// a PromptDetector.
func (d *PromptDetector) IsUsageLimit(content string) bool {
	return IsUsageLimitContent(d.tool, content)
}
