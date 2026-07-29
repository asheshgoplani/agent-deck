package tmux

import "strings"

// Auth-failure detection (fleet-death hardening).
//
// SubstateAuth401 deliberately covers BOTH an auth banner and a dead-connection
// banner ("socket connection closed") — for the coarse status they are the same
// verdict: the pane is alive but wedged. The auth-resilience layer needs a
// narrower question: *is this specifically a credential failure?* Only that case
// justifies holding a session out of every automatic boot path, because
// restarting it cannot help until the user re-authenticates, and each doomed
// restart forks the single rotating OAuth refresh token and makes the outage
// worse for every OTHER session on the same credentials.
//
// A socket drop, by contrast, is exactly the case a restart DOES fix, so it must
// not be swept into the hold.

// authFailureBannerPatterns are the rendered fragments that specifically mean
// "these credentials are not valid". Observed shapes (field evidence, #1400 plus
// the 2026-07-26 fleet-death incident):
//
//	Please run /login · API Error: 401 {"type":"error","error":{"type":"authentication_error",...}}
//	API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired"}}
//	Invalid API key · Please run /login
//
// Anchored on the rendered banner text rather than bare tokens like "401" so
// ordinary conversation about auth errors does not match. The connection-failure
// pattern ("socket connection closed") is deliberately ABSENT: a dropped socket
// is restart-recoverable and must stay outside the auth hold.
var authFailureBannerPatterns = []string{
	"API Error: 401",
	"API Error (401",
	"Please run /login",
	`"type":"authentication_error"`,
	"Invalid authentication credentials",
	"OAuth token has expired",
	"Invalid API key",
}

// IsAuthFailureContent reports whether the pane content shows a tool-rendered
// CREDENTIAL failure banner (as opposed to any other error banner).
//
// It reuses the same last-15-lines window and the same over-match guards as
// HasErrorBanner (quoted "⎿" tool output, the user's own input line, prose on an
// assistant-turn line without a structural banner marker), so a conductor
// quoting a child's 401 or a human discussing one never trips it.
//
// Claude-compatible renderings only; any other tool returns false. Callers pass
// the tool name as resolved for prompt detection (see inferToolFromSessionFields).
func IsAuthFailureContent(tool, content string) bool {
	if strings.ToLower(strings.TrimSpace(tool)) != "claude" {
		return false
	}
	return scanClaudeBannerLines(content, authFailureBannerPatterns)
}

// IsAuthFailure reports whether this detector's tool would render the given
// content as a credential-failure banner. Method form for call sites that
// already hold a PromptDetector.
func (d *PromptDetector) IsAuthFailure(content string) bool {
	return IsAuthFailureContent(d.tool, content)
}

// LastSampleAuthFailure reports whether the MOST RECENT sample that could
// actually read the pane classified it as a credential failure, and returns the
// retained evidence snapshot for that sample.
//
// "Most recent readable sample" is the precise question the death path needs:
// once the pane is gone it cannot be re-read, so the last live verdict is the
// only evidence of why the process exited. It is deliberately NOT "has this
// session ever shown a 401" — a session that showed a 401, recovered, and later
// died of a dropped socket must not be attributed to auth. Every content-
// classified sample overwrites the verdict, so a healthy or socket-error sample
// clears it; only the death itself (which classifies nothing) leaves it standing.
func (s *Session) LastSampleAuthFailure() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lastSampleAuthFailure {
		return "", false
	}
	return s.lastAuthFailureContent, true
}

// noteSampleAuthFailureLocked records this sample's credential-failure verdict
// and, when positive, the pane snapshot that justifies it. Caller holds s.mu.
//
// Called on every content-classified sample (including negative ones) so the
// verdict always describes the latest readable state of the pane. The evidence
// snapshot is only overwritten on a positive verdict, so the text that proves
// the failure survives to the post-mortem.
func (s *Session) noteSampleAuthFailureLocked(isAuthFailure bool, content string) {
	s.lastSampleAuthFailure = isAuthFailure
	if isAuthFailure {
		s.lastAuthFailureContent = tailLines(content, authFailureEvidenceLines)
	}
}

// authFailureEvidenceLines bounds the retained evidence snapshot. Enough to show
// the banner and the lines around it; small enough to keep in memory per session
// and to write into a sidecar without bloat.
const authFailureEvidenceLines = 20

// tailLines returns the last n non-empty-trimmed lines of content, joined.
func tailLines(content string, n int) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
