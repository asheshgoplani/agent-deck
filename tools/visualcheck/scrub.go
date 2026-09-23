package main

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// scrubRule is one documented substitution applied to a captured frame
// before it is compared with a golden or embedded in the contact sheet.
// README.md's scrub list mirrors this slice; keep the two in sync by hand
// when either changes.
type scrubRule struct {
	name    string
	pattern *regexp.Regexp
	replace string
}

var scrubRules = []scrubRule{
	{"uuid", regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`), "<uuid>"},
	{"agent-deck-id", regexp.MustCompile(`\b[0-9a-f]{7,12}\b`), "<id>"},
	{"semver", regexp.MustCompile(`\bv?\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?\b`), "<version>"},
	{"clock-time", regexp.MustCompile(`\b\d{1,2}:\d{2}(:\d{2})?\b`), "<time>"},
	{"iso-date", regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2})?(Z|[+-]\d{2}:?\d{2})?)?\b`), "<date>"},
	{"relative-age", regexp.MustCompile(`\b\d+(\.\d+)?(s|ms|m|h|d|w)\b`), "<age>"},
	{"just-now", regexp.MustCompile(`\bjust now\b`), "<age>"},
	// The attach-shell step's real system-shell prompt embeds the sandbox
	// hostname and account name (default bash PS1: host:dir user$). Both
	// vary by machine, and on the g14 test box's Docker runner the
	// container hostname is a fresh random string every run even on the
	// same physical box, so this has to be scrubbed rather than pinned by
	// environment (the sandboxed HOME has no rc files of its own, but the
	// image's /etc/profile / /etc/bashrc sets PS1 unconditionally). The
	// session title "shell-live" is a fixed seed constant, so it anchors
	// the match precisely instead of a loose host:dir pattern.
	{"shell-prompt", regexp.MustCompile(`(?m)^\S*:shell-live \S+\$`), "<shell-prompt>$"},
	{"shell-tmux-label", regexp.MustCompile(`agentdeck_shell-live_[0-9a-f]{8}`), "agentdeck_shell-live_<id>"},
	// The preview pane's "Output" summary for a shell session reads raw
	// scrollback, bypassing the attach step's `clear` (that only clears the
	// visible screen, not history): production's own shell launch command
	// exports several env vars inline and echoes doing so, including a
	// bare process id with no fixed digit count. That line-wraps across a
	// variable number of preview lines depending on that digit count and
	// on the exact (also variable-length) sandbox path inside it, so no
	// single-line pattern can normalize it -- this consumes the whole
	// noisy run in one shot, multiline, anchored on its two fixed
	// substrings (a literal env var name and a literal path fragment that
	// never change).
	{"shell-startup-echo", regexp.MustCompile(`(?s)\d+ AGENTDECK_PROFILE=.*?identity\.md`), "<shell-startup-echo>"},
	{"pid-socket", regexp.MustCompile(`\bvc-[0-9a-f]+\b`), "vc-<pid>"},
	// Greedy through the rest of the path, not just the vc-<pid> segment:
	// the sandbox root's numeric suffix (os.MkdirTemp) varies in digit
	// count between runs, and a narrow terminal truncates the rendered
	// path with "…" at a fixed column -- so a run with a longer suffix
	// truncates one character earlier into the path than a run with a
	// shorter one, leaving a different tail visible even after the
	// vc-<pid> portion itself is scrubbed. Consuming through to the next
	// separator/pipe/ellipsis makes the replacement length-independent.
	{"tmp-path", regexp.MustCompile(`/tmp/vc-[^\s│…]*[…]?`), "/tmp/vc-<tmp>"},
}

func init() {
	// A worktree fork (the "fork" step) tags its row with "[branch]
	// <hostname>" -- the machine's real hostname, which obviously varies
	// between this dev machine and the g14 test box. Added at init time
	// (not a literal in scrubRules) so it stays correct wherever this
	// runs, rather than a hardcoded name only good on one machine.
	if host, err := os.Hostname(); err == nil && host != "" {
		scrubRules = append(scrubRules, scrubRule{
			"hostname", regexp.MustCompile(regexp.QuoteMeta(host)), "<hostname>",
		})
	}
}

// scrubFrame applies every rule in order and returns the result. Applying
// them in a fixed slice order keeps output stable when a frame happens to
// match more than one pattern in the same span (e.g. a semver-shaped
// relative age).
func scrubFrame(frame string) string {
	originalLines := strings.Split(frame, "\n")
	for _, r := range scrubRules {
		frame = r.pattern.ReplaceAllString(frame, r.replace)
	}
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		if i >= len(originalLines) || !strings.Contains(line, "remotes/") || !strings.Contains(line, "· poll <age>") {
			continue
		}
		oldDivider := strings.IndexRune(originalLines[i], '│')
		newDivider := strings.IndexRune(line, '│')
		if oldDivider < 0 || newDivider < 0 {
			continue
		}
		left := strings.TrimRight(line[:newDivider], " ")
		padding := utf8.RuneCountInString(originalLines[i][:oldDivider]) - utf8.RuneCountInString(left)
		if padding >= 0 {
			lines[i] = left + strings.Repeat(" ", padding) + line[newDivider:]
		}
	}
	return strings.Join(lines, "\n")
}
