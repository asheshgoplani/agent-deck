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
	regexp.MustCompile(`(?i)\b(?:otp|one[ -]time (?:code|password)|pin|verification code|2fa code)\s*(?:is|hai|=|:)\s*\d{4,8}\b`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9_\-\.=]{16,}`),
	regexp.MustCompile(`(?i)://[^/\s:@]+:[^/\s@]{4,}@`), // credentials in a URL
}

var (
	candidateRun = regexp.MustCompile(`[A-Za-z0-9+/_=\-:]{20,}`)
	uuidLike     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	deckIDLike   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9]{10}$`)
	hexOnly      = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// keywordValue finds "<key word> is|=|:|hai <value>" (DB_PASSWORD=...,
// "the password is tiger"). The value is a secret unless it is an ordinary
// word that says something about the secret ("the token is expired").
var keywordValue = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:[a-z0-9]+_)*(password|passwd|pwd|pass|passcode|passphrase|secret|api_?key|apikey|access_?key|secret_?key|private_?key|token|auth|credentials?)(?:_[a-z0-9]+)*\s*(=|:|\bis\b|\bhai\b)\s*["']?([^\s"']{4,})`)

var notAValue = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`expired expiring required invalid wrong missing empty set unset stored failing failed
		incorrect correct needed valid revoked rotated saved changed reset new old the a an in not still now being too
		ok fine working broken there here same different gone weak strong blank null none nil undefined optional
		mandatory hashed encrypted protected leaked exposed visible hidden shown correctly also only already never
		always just being been for from of on off to with without what which where when why how this that`) {
		notAValue[w] = true
	}
}

// HasSecret reports whether text holds something that looks like a key,
// token or password.
func HasSecret(text string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	for _, m := range keywordValue.FindAllStringSubmatch(text, -1) {
		v := strings.ToLower(strings.TrimRight(m[3], ".,;!?)"))
		sep := strings.ToLower(m[2])
		if (sep == "is" || sep == "hai") && notAValue[v] {
			continue
		}
		if len(v) >= 4 {
			return true
		}
	}
	words := strings.Fields(text)
	for i, w := range words {
		for _, run := range candidateRun.FindAllString(w, -1) {
			if looksRandom(run, wordsBefore(words, i)) {
				return true
			}
		}
	}
	return false
}

// wordsBefore is the lowercase words just before word i.
func wordsBefore(words []string, i int) string {
	from := max(0, i-3)
	return strings.ToLower(strings.Join(words[from:i], " "))
}

var (
	secretWords = regexp.MustCompile(`token|key|secret|password|passwd|api|bearer|auth|credential`)
	digestWords = regexp.MustCompile(`checksum|md5|sha|digest|hash|commit|etag|sum`)
	camelHumps  = regexp.MustCompile(`[a-z][A-Z]`)
)

// looksRandom is the high-entropy check: a long run that mixes letters and
// digits with a high Shannon entropy. Ids the deck itself prints (UUIDs,
// session ids, git commit hashes, digests after "sha256:" / "checksum"),
// identifiers (snake_case, camelCase) and paths are not secrets; a hex run
// after "token" or "key" is.
func looksRandom(s, before string) bool {
	if uuidLike.MatchString(s) || deckIDLike.MatchString(s) {
		return false
	}
	if hexOnly.MatchString(s) {
		switch {
		case len(s) < 32:
			return false
		case secretWords.MatchString(before):
			return true
		case digestWords.MatchString(before), len(s) == 40, len(s) == 64:
			return false // a git commit, a sha256 or md5 digest
		}
		return true
	}
	if strings.HasPrefix(strings.ToLower(s), "sha256") || strings.HasPrefix(strings.ToLower(s), "sha1") {
		return false
	}
	digitCount := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digitCount++
		}
	}
	if !strings.ContainsAny(s, "=+/") && digitCount <= 2 && (strings.Contains(s, "_") || len(camelHumps.FindAllString(s, -1)) >= 3) {
		return false // an identifier: TestStatusUpdatePass_v2, handleWebhookRequestV2
	}
	if s == strings.ToLower(s) && strings.Count(s, "/") >= 2 {
		return false // a path
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
	n := len(s)
	switch {
	case digits == 0 && lower == 0 && upper >= 20:
		return entropy(s) >= 3.8 // a key read out in capitals
	case letters == 0 || digits == 0:
		return false
	case n < 24:
		// 20 to 23 lowercase letters and digits, well mixed
		return n >= 20 && digits >= 3 && letters >= 6 && entropy(s) >= 3.6
	case upper == 0 && lower > 0 && digits < 4:
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
