package logging

import "strings"

// SanitizeValue strips characters that let an untrusted value forge fake log
// lines or otherwise corrupt structured/text log output (CRLF-style log
// injection, CodeQL go/log-injection). Newlines, carriage returns, and other
// C0 control characters are replaced with a visible escape marker so the
// original value stays legible without letting it inject new record
// boundaries. Call this on any user- or session-supplied string (path,
// title, session ID, env value, ...) before passing it to a log call.
func SanitizeValue(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	dirty := false
	for _, r := range s {
		switch {
		case r == '\n':
			dirty = true
			b.WriteString(`\n`)
		case r == '\r':
			dirty = true
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteByte('\t')
		case r < 0x20 || r == 0x7f:
			dirty = true
			b.WriteRune(0xfffd)
		default:
			b.WriteRune(r)
		}
	}
	if !dirty {
		return s
	}
	return b.String()
}
