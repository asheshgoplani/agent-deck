package ask

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Display caps for untrusted text (PLAN.md section 5).
const (
	MaxReason     = 200
	MaxQuestion   = 300
	MaxOption     = 80
	MaxOptions    = 6
	MaxTextAnswer = 500 // bytes
	maxTitle      = 200
	maxHost       = 100
)

// DefaultSecretScopes is [ask] secret_scopes when unset.
var DefaultSecretScopes = []string{"conductor/"}

var nameRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}/[A-Za-z0-9_.-]{1,64}$`)

// ValidateName checks a secret name: scope/NAME, ASCII only, one level, no
// leading '.', no "..".
func ValidateName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, "/.") {
		return fmt.Errorf("%w: %q", ErrName, SanitizeText(name, MaxOption))
	}
	return nil
}

// ScopeAllowed reports whether name lies in one of scopes (nil means
// DefaultSecretScopes). A scope entry "conductor" and "conductor/" both mean
// the scope conductor; empty entries never match.
func ScopeAllowed(name string, scopes []string) bool {
	if scopes == nil {
		scopes = DefaultSecretScopes
	}
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" || s == "/" {
			continue
		}
		if !strings.HasSuffix(s, "/") {
			s += "/"
		}
		if strings.HasPrefix(name, s) {
			return true
		}
	}
	return false
}

// SanitizeText makes untrusted text (a reason, question, title) safe to show:
// control and format characters (Cc, Cf: bidi overrides, zero-width) are
// dropped, line and paragraph separators, newlines and tabs fold to one
// space, runs of space collapse, and the result is cut to max runes (never
// bytes) with a trailing ellipsis.
func SanitizeText(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r):
			// dropped
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	out := b.String()
	if max > 0 {
		if rs := []rune(out); len(rs) > max {
			out = strings.TrimSpace(string(rs[:max-1])) + "…"
		}
	}
	return out
}

var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|[^A-Za-z0-9])gh[pousr]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{16,}`),
	regexp.MustCompile(`(^|[^A-Za-z0-9])sk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`(^|[^A-Za-z0-9])(AKIA|ASIA)[0-9A-Z]{16}`),
	regexp.MustCompile(`xox[abposr]-[A-Za-z0-9-]{8,}`),
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*-----`),
}

var tokenRe = regexp.MustCompile(`[A-Za-z0-9+/=]{20,}`)
var hexRe = regexp.MustCompile(`^([0-9a-f]+|[0-9A-F]+)$`)

// LooksLikeCredential reports whether a choice or text answer looks like a
// credential: a known token prefix (ghp_, github_pat_, sk-, AKIA, xoxb-, a
// PEM header) or a 20+ character run of high entropy. Such answers would land
// in the agent's transcript, so deliver refuses them.
func LooksLikeCredential(s string) bool {
	for _, re := range credentialPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	for _, tok := range tokenRe.FindAllString(s, -1) {
		if highEntropy(tok) {
			return true
		}
	}
	return false
}

// highEntropy: a long hex string (not a 40-char git commit id), or a run that
// mixes at least three of lower, upper, digit, symbol with at least 4 bits
// of entropy per character, which random tokens have and words do not.
func highEntropy(tok string) bool {
	if hexRe.MatchString(tok) {
		return len(tok) >= 32 && len(tok) != 40 && strings.ContainsAny(tok, "0123456789") && entropy(tok) >= 3.0
	}
	var lower, upper, digit, symbol int
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z':
			lower = 1
		case r >= 'A' && r <= 'Z':
			upper = 1
		case r >= '0' && r <= '9':
			digit = 1
		default:
			symbol = 1
		}
	}
	return lower+upper+digit+symbol >= 3 && entropy(tok) >= 4.0
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
