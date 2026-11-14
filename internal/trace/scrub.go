package trace

import (
	"regexp"
	"strings"
)

// redacted is the marker every scrubbed value is replaced with.
const redacted = "[redacted]"

// scrubRules is the secret scrubber applied to every free-form string that
// reaches the spool or trace.db. It is deliberately broad (false positives
// cost a summary detail, false negatives leak a secret). The name/value and
// Authorization rules follow internal/session/spawn_failure.go's
// redactSpawnFailureDiagnostic, which is unexported in package session;
// the token-shape rules are added for free text.
var scrubRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// PEM private key blocks (whole block, or an unterminated head).
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), redacted},
	// Authorization: Bearer/Basic <value>, and a bare "Bearer <value>".
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}`), "${1} " + redacted},
	// Provider token shapes.
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}`), redacted},                                      // Anthropic sk-ant-, OpenAI sk-/sk-proj-
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{12,}`), redacted},                                 // GitHub tokens
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), redacted},                               // GitHub fine-grained PAT
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), redacted},                                // AWS access key id
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), redacted},                              // Slack
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), redacted},                                     // Google API key
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`), redacted},                                   // GitLab PAT
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), redacted}, // JWT
	// Long opaque blobs: hex >= 32, base64 >= 40 (no "-", so UUID-shaped ids survive).
	{regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`), redacted},
	{regexp.MustCompile(`[A-Za-z0-9+/_]{40,}={0,2}`), redacted},
}

// namedValueRe matches name=value / name: value for secret-shaped names.
// A Bearer/Basic value is left to the Authorization rule, which keeps the
// scheme word; every other value is replaced.
var namedValueRe = regexp.MustCompile(`(?i)\b([A-Za-z0-9_-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|auth)[A-Za-z0-9_-]*)(["']?\s*[:=]\s*["']?)([^\s"'&;|,}]+)`)

// Scrub removes secret-shaped substrings from s. It is applied to every
// summary and free-form field before a record is spooled.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	for _, r := range scrubRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return namedValueRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := namedValueRe.FindStringSubmatch(m)
		v := strings.ToLower(parts[3])
		if v == "bearer" || v == "basic" || v == redacted {
			return m
		}
		return parts[1] + parts[2] + redacted
	})
}
