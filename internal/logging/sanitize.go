package logging

import "strings"

// SanitizeValue strips characters that let an untrusted value forge fake log
// lines or otherwise corrupt structured/text log output (CRLF-style log
// injection, CodeQL go/log-injection). Newlines, carriage returns, and other
// C0 control characters are replaced with a visible escape marker so the
// original value stays legible without letting it inject new record
// boundaries. Call this on any user- or session-supplied string (path,
// title, session ID, env value, ...) before passing it to a log call.
//
// Every path builds a fresh string; the input is never returned. The two
// former fast paths ("" and "nothing needed escaping" returned s unchanged)
// were behaviorally identical but made this function useless as a sanitizer
// barrier: dataflow analysis follows those returns and sees the tainted value
// arriving at the log sink untouched, so go/log-injection kept firing on every
// call site no matter how many were fixed. Returning a built string on all
// paths is what makes the barrier real.
//
// One deliberate consequence: because the value is rebuilt rune by rune,
// invalid UTF-8 bytes become U+FFFD rather than surviving intact as they did
// on the old fast path. That is the right outcome for a log sanitizer — raw
// undecodable bytes are exactly what corrupts a log stream — but it is a real
// behavior change for non-UTF-8 input, so it is pinned by a test.
func SanitizeValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteByte('\t')
		case r < 0x20 || r == 0x7f:
			b.WriteRune(0xfffd)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
