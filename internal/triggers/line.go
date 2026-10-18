// Package triggers is the read model behind `agent-deck triggers` (issue
// #2702): which sender woke which session, how often, and whether its target
// still exists. Senders tag themselves on `session send` (push); a scan of
// launchd/systemd user units finds the ones that have not fired yet (pull).
package triggers

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/agents"
)

// LineCells is the display budget of a stored "last thing it said" line.
const LineCells = 80

var (
	envelopePattern = regexp.MustCompile(`^\[agent-deck from:[^\]]*\]\s*`)
	// Token shapes, the same families the GitHub sanitize gate refuses.
	tokenPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
		regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}\b`),
		regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_-]{30,}\b`), // bot tokens
		regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}\b`), // JWT
		regexp.MustCompile(`\b[A-Fa-f0-9]{40,}\b`),
		regexp.MustCompile(`\b[A-Za-z0-9+/_-]{48,}={0,2}`),
	}
	secretAssignPattern = regexp.MustCompile(`(?i)\b([A-Z0-9_.-]*(?:token|secret|passw(?:or)?d|api[_-]?key|credential)[A-Z0-9_.-]*)(\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)`)
)

// Redact replaces secret-shaped substrings with [redacted].
func Redact(s string) string {
	s = secretAssignPattern.ReplaceAllString(s, `${1}${2}[redacted]`)
	for _, p := range tokenPatterns {
		s = p.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// FirstLine is what a fire stores as "the last thing it said": the first
// non-empty line without the send envelope, control characters dropped,
// secrets redacted and cut to LineCells cells. It is computed before the
// line is stored, so a secret never reaches the database.
func FirstLine(message string) string {
	line := ""
	for _, l := range strings.Split(message, "\n") {
		l = strings.TrimSpace(envelopePattern.ReplaceAllString(strings.TrimSpace(l), ""))
		if l != "" {
			line = l
			break
		}
	}
	line = Redact(agents.SanitizeForDisplay(line))
	if runewidth.StringWidth(line) > LineCells {
		line = runewidth.Truncate(line, LineCells, "…")
	}
	return line
}

// TextHash keys the no-op rule: the same message twice in a row changes
// nothing. Only the hash of the whole message is stored, never the text.
func TextHash(message string) string {
	norm := strings.TrimSpace(envelopePattern.ReplaceAllString(strings.TrimSpace(message), ""))
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:8])
}
