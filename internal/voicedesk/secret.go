package voicedesk

import (
	"math"
	"regexp"
	"strings"
)

// Secret detection (CONTRACT.md section 4 rule 6). Before any text goes into
// `say` or `show`, keys, tokens and passwords are looked for with a pattern
// list plus a high-entropy check. A hit means: say only that there is a
// secret, and show nothing of that text.

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-(?:ant-|proj-|live-|test-)?[A-Za-z0-9_\-]{16,}`),                // Anthropic, OpenAI, Stripe style
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`),                         // GitHub tokens
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),                                     // GitHub fine-grained
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`),                                   // Slack
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),                                      // AWS access key id
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),                                         // Google API key
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}`),                                        // GitLab
	regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_\-]{35}\b`),                                    // Telegram bot token
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),                                 // PEM keys
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`), // JWT
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:[a-z0-9]+_)*(?:password|passwd|pwd|pass|passcode|passphrase|secret|api_?key|apikey|access_?key|secret_?key|private_?key|token|auth|credentials?)(?:_[a-z0-9]+)*\s*(?:is|hai|=|:)\s*["']?\S{6,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9_\-\.=]{16,}`),
	regexp.MustCompile(`(?i)://[^/\s:@]+:[^/\s@]{4,}@`), // credentials in a URL
}

var (
	candidateRun = regexp.MustCompile(`[A-Za-z0-9+/_=\-]{24,}`)
	uuidLike     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	deckIDLike   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9]{10}$`)
	hexOnly      = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// HasSecret reports whether text holds something that looks like a key,
// token or password.
func HasSecret(text string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	for _, run := range candidateRun.FindAllString(text, -1) {
		if looksRandom(run) {
			return true
		}
	}
	return false
}

// looksRandom is the high-entropy check: a long run that mixes letters and
// digits with a high Shannon entropy. Ids the deck itself prints (UUIDs,
// session ids, 40-char git commit hashes) and paths are not secrets.
func looksRandom(s string) bool {
	if uuidLike.MatchString(s) || deckIDLike.MatchString(s) {
		return false
	}
	if hexOnly.MatchString(s) {
		return len(s) >= 32 && len(s) != 40 // a 40-hex run is a git commit hash
	}
	if !strings.ContainsAny(s, "0123456789") || s == strings.ToLower(s) && strings.Count(s, "/") >= 2 {
		return false // a path or a kebab-case title (no digits, or lowercase with slashes)
	}
	var letters, digits, upper, lower int
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'A' && r <= 'Z':
			letters++
			upper++
		case r >= 'a' && r <= 'z':
			letters++
			lower++
		}
	}
	if letters == 0 || digits == 0 || upper == 0 && lower > 0 && digits < 4 {
		return false
	}
	return entropy(s) >= 3.8
}

func entropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}
