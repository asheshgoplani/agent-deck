package voicedesk

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Short speech (CONTRACT.md section 4 rule 7): one sentence (two for a
// status), at most three names then "and N more on screen", a read reply
// cut to two sentences or 240 characters with markdown and code removed.

const (
	maxSpokenItems = 3
	maxReplyRunes  = 240
)

var (
	speakTag    = regexp.MustCompile(`(?s)<speak>(.*?)</speak>`)
	codeFence   = regexp.MustCompile("(?s)```.*?(```|$)")
	inlineCode  = regexp.MustCompile("`([^`]*)`")
	mdLink      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdImage     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	htmlTag     = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	mdHeading   = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s*`)
	mdBullet    = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+\.)\s+`)
	mdQuote     = regexp.MustCompile(`(?m)^\s*>\s?`)
	mdTableRule = regexp.MustCompile(`(?m)^\s*\|?[\s:|-]+\|[\s:|-]*$`)
	mdEmphasis  = regexp.MustCompile(`(\*\*|__|\*|_|~~)`)
	spaces      = regexp.MustCompile(`\s+`)
	sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)
)

// Speakable turns a reply into the short part to speak: the <speak> part
// when the reply has one, else the first two sentences, at most 240
// characters, without markdown or code.
func Speakable(reply string) string {
	if m := speakTag.FindStringSubmatch(reply); m != nil {
		reply = m[1]
	}
	t := codeFence.ReplaceAllString(reply, " ")
	t = mdImage.ReplaceAllString(t, " ")
	t = mdLink.ReplaceAllString(t, "$1")
	t = inlineCode.ReplaceAllString(t, "$1")
	t = htmlTag.ReplaceAllString(t, " ")
	t = mdTableRule.ReplaceAllString(t, " ")
	t = mdHeading.ReplaceAllString(t, "")
	t = mdBullet.ReplaceAllString(t, "")
	t = mdQuote.ReplaceAllString(t, "")
	t = strings.ReplaceAll(t, "|", " ")
	t = mdEmphasis.ReplaceAllString(t, "")
	t = strings.TrimSpace(spaces.ReplaceAllString(t, " "))
	return firstSentences(t, 2, maxReplyRunes)
}

// firstSentences keeps n sentences and at most limit runes, cutting at a
// word boundary (never inside a word or a multi-byte rune).
func firstSentences(t string, n, limit int) string {
	end := len(t)
	idx := sentenceEnd.FindAllStringIndex(t, n)
	if len(idx) == n {
		end = idx[n-1][0] + 1
	}
	t = strings.TrimSpace(t[:end])
	if utf8.RuneCountInString(t) <= limit {
		return t
	}
	rs := []rune(t)[:limit]
	cut := string(rs)
	if i := strings.LastIndexByte(cut, ' '); i > limit/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:-") + "…"
}

var numberWords = []string{"No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten",
	"Eleven", "Twelve"}

// countWord is "Three" for 3, "14" past twelve.
func countWord(n int) string {
	if n >= 0 && n < len(numberWords) {
		return numberWords[n]
	}
	return fmt.Sprint(n)
}

func lowerCount(n int) string {
	if n >= 0 && n < len(numberWords) {
		return strings.ToLower(numberWords[n])
	}
	return fmt.Sprint(n)
}

// spokenList is "Three: a, b, c." or "Five: a, b, c, and 2 more on screen."
func spokenList(names []string) string {
	if len(names) == 0 {
		return ""
	}
	shown := names
	if len(shown) > maxSpokenItems {
		shown = shown[:maxSpokenItems]
	}
	s := countWord(len(names)) + ": " + strings.Join(shown, ", ")
	if rest := len(names) - len(shown); rest > 0 {
		s += fmt.Sprintf(", and %d more on screen", rest)
	}
	return s + "."
}

// spokenTitle is a session title as a voice says it: dashes and
// underscores become spaces ("phone-build" -> "phone build"), the voice
// daemon's mic marker is dropped, a remote session gets "on <host>".
func spokenTitle(title, host string) string {
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "🎙"))
	t = strings.NewReplacer("-", " ", "_", " ").Replace(t)
	t = strings.TrimSpace(spaces.ReplaceAllString(t, " "))
	if host != "" {
		t += " on " + host
	}
	return t
}

// statusWords is how a status is said.
func statusWords(status string) string {
	switch status {
	case "running":
		return "working"
	case "waiting":
		return "waiting for you"
	case "idle":
		return "idle"
	case "error":
		return "failed"
	case "stopped":
		return "stopped"
	case "starting":
		return "starting"
	case "":
		return "in an unknown state"
	}
	return status
}

// ago is "4 minutes", "1 hour", "" when unknown or under a minute.
func ago(since time.Time, now time.Time) string {
	if since.IsZero() || now.Before(since) {
		return ""
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return ""
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[n:]
}

// ensureStop ends a line with a full stop when it has no end mark.
func ensureStop(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "…") {
		return s
	}
	return s + "."
}
