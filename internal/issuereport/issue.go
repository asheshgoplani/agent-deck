// Package issuereport builds and files the GitHub issue behind "Report a
// problem" (ASK-109): `agent-deck feedback bug|idea`, the TUI form and the
// Mac app all go through this one core.
//
// Nothing here sends anything on its own. Build only formats; File sends,
// and callers call it only after the user has seen the full issue text
// and confirmed.
package issuereport

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// Repo is the GitHub repository reports are filed on.
const Repo = "asheshgoplani/agent-deck"

// MaxURLLen bounds the prefilled browser URL. GitHub (and browsers) reject
// much longer query strings; 8000 leaves headroom under the ~8 KiB limit.
const MaxURLLen = 8000

// Kind is the issue form the report is filed with.
type Kind string

const (
	KindBug  Kind = "bug"
	KindIdea Kind = "idea"
)

// Report is what the user typed. For KindIdea the fields map onto the
// feature form: WhatHappened = "What problem would this solve?",
// Expected = "Proposed behavior", Steps = "Alternatives considered".
type Report struct {
	Kind         Kind
	Title        string
	WhatHappened string
	Expected     string
	Steps        string
	Tool         string
	// Surface names where the report was filed from ("cli", "tui",
	// "mac app"); it ends up in the footer. Empty means "cli".
	Surface string
}

// Diagnostics is the machine context attached to a bug report.
type Diagnostics struct {
	Version, OS, Arch, Terminal, Tool, LogExcerpt string
}

// Issue is the exact issue that is shown to the user and then filed.
type Issue struct {
	Title  string
	Labels []string
	Body   string
}

// Section headings: these are the field labels of
// .github/ISSUE_TEMPLATE/bug.yml and feature.yml (pinned by a test that
// parses the templates), so triage sees the same layout as a web report.
const (
	hVersion      = "agent-deck version"
	hEnvironment  = "OS and terminal"
	hRepro        = "Steps to reproduce"
	hExpected     = "Expected vs actual"
	hLogs         = "Logs (redacted)"
	hAIDisclosure = "Was this written with AI help?"
	hAIModel      = "If AI helped, which model?"
	hProblem      = "What problem would this solve?"
	hProposal     = "Proposed behavior"
	hAlternatives = "Alternatives considered"
	hWilling      = "Would you be up for implementing it?"

	noResponse   = "_No response_" // what GitHub writes for an empty form field
	humanWritten = "Human-written" // the ai_disclosure option for a form the user typed
	notWilling   = "No, just proposing"
	trimmedMark  = "(trimmed)"
	footerPrefix = "Filed from agent-deck "
	titleCells   = 100
)

// Validate reports whether r has what a report needs.
func (r Report) Validate() error {
	switch r.Kind {
	case KindBug, KindIdea:
	default:
		return &Error{Code: ErrInvalidInput, Message: fmt.Sprintf("unknown report kind %q (want bug or idea)", r.Kind)}
	}
	if strings.TrimSpace(r.WhatHappened) == "" && strings.TrimSpace(r.Title) == "" {
		if r.Kind == KindIdea {
			return &Error{Code: ErrInvalidInput, Message: "describe the problem the idea would solve (--what) or give a --title"}
		}
		return &Error{Code: ErrInvalidInput, Message: "describe what happened (--what) or give a --title"}
	}
	return nil
}

// Build turns a report plus diagnostics into the issue to file. All user
// text and diagnostics are passed through Scrub.
func Build(r Report, d Diagnostics) Issue {
	home, _ := os.UserHomeDir()
	sc := func(s string) string { return strings.TrimSpace(Scrub(strings.TrimSpace(StripControl(s, true)), home)) }

	what, expected, steps := sc(r.WhatHappened), sc(r.Expected), sc(r.Steps)
	title := oneLine(sc(r.Title))
	if title == "" {
		title = oneLine(firstLine(what))
	}
	title = runewidth.Truncate(title, titleCells, "…")

	surface := oneLine(sc(r.Surface))
	if surface == "" {
		surface = "cli"
	}
	surface = runewidth.Truncate(surface, titleCells, "…")
	tool := sc(r.Tool)
	if tool == "" {
		tool = sc(d.Tool)
	}

	is := Issue{Title: TitlePrefix(r.Kind) + title, Labels: LabelsFor(r.Kind)}
	sections := func(n int) []section {
		c := func(s string) string { return capText(s, n) }
		if r.Kind == KindIdea {
			return []section{
				{hProblem, userText(c(what))},
				{hProposal, userText(c(expected))},
				{hAlternatives, userText(c(steps))},
				{hWilling, notWilling},
				{hAIDisclosure, humanWritten},
				{hAIModel, noResponse},
			}
		}
		env := joinNonEmpty(", ", sc(d.OS)+archSuffix(sc(d.Arch)), sc(d.Terminal))
		if tool != "" {
			env = joinNonEmpty(", ", env, "tool: "+tool)
		}
		ea := "Expected: " + orDash(c(expected)) + "\nActual: " + orDash(c(what))
		return []section{
			{hVersion, orNoResponse(c(sc(d.Version)))},
			{hEnvironment, orNoResponse(c(env))},
			{hRepro, userText(c(steps))},
			{hExpected, userText(ea)},
			{hLogs, logBlock(capTail(sc(d.LogExcerpt), n))},
			{hAIDisclosure, humanWritten},
			{hAIModel, noResponse},
		}
	}
	// Final pass over the assembled title and body, the way SanitizeIssue
	// scrubs a handed-back issue: a preview is then exactly what --issue
	// accepts, byte for byte (the Mac app's send path). Every preview must
	// also be sendable, so each field is capped (cut text marked
	// "(trimmed)", the log keeping its newest lines) and the cap is
	// lowered until the body fits MaxBodyLen.
	is.Title = Scrub(is.Title, home)
	for n := fieldCap; ; n = n * 3 / 4 {
		is.Body = scrubBody(r.Kind, renderBody(sections(n), footerPrefix+surface), home)
		if len(is.Body) <= MaxBodyLen || n <= minFieldCap {
			break
		}
	}
	return is
}

// fieldCap is the first cap, in bytes, on each field of a built body;
// Build lowers it (down to minFieldCap) until the body fits MaxBodyLen.
const (
	fieldCap    = 15000
	minFieldCap = 200
)

// capText cuts s to at most n bytes on a rune boundary, ending the cut
// text with "… (trimmed)" on its own line.
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	mark := "\n… " + trimmedMark
	k := n - len(mark)
	if k < 0 {
		k = 0
	}
	for k > 0 && !utf8.RuneStart(s[k]) {
		k--
	}
	return s[:k] + mark
}

// capTail keeps the newest whole lines of s that fit in n bytes, after a
// "(trimmed)" line.
func capTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	mark := trimmedMark + "\n"
	k := len(s) - n + len(mark)
	if k > len(s) {
		k = len(s)
	}
	if i := strings.IndexByte(s[k:], '\n'); i >= 0 {
		k += i + 1
	} else {
		for k < len(s) && !utf8.RuneStart(s[k]) {
			k++
		}
	}
	return mark + s[k:]
}

// scrubBody scrubs an issue body except for the kind's section heading
// lines, which are fixed text: the runs of lines between them are each
// scrubbed as one text. Build and SanitizeIssue both use it, so a body
// Build made comes back from SanitizeIssue unchanged.
func scrubBody(k Kind, body, home string) string {
	heads := map[string]bool{}
	for _, h := range Headings(k) {
		heads["### "+h] = true
	}
	var out, run []string
	flush := func() {
		if run != nil {
			out = append(out, Scrub(strings.Join(run, "\n"), home))
			run = nil
		}
	}
	for _, l := range strings.Split(body, "\n") {
		if heads[l] {
			flush()
			out = append(out, l)
			continue
		}
		run = append(run, l)
	}
	flush()
	return strings.Join(out, "\n")
}

// TitlePrefix is the title prefix of the kind's issue form.
func TitlePrefix(k Kind) string {
	if k == KindIdea {
		return "[feature] "
	}
	return "[bug] "
}

// LabelsFor returns the labels of the kind's issue form.
func LabelsFor(k Kind) []string {
	if k == KindIdea {
		return []string{"enhancement", "triage"}
	}
	return []string{"bug", "triage"}
}

// MaxBodyLen bounds a body handed in from outside (GitHub's own issue body
// limit is 65536 characters).
const MaxBodyLen = 60000

// MaxTitleLen bounds an issue title handed in from outside (GitHub
// rejects titles longer than 256 characters).
const MaxTitleLen = 256

// SanitizeIssue checks an issue handed back by a caller that previewed it
// (the --issue path of the CLI) and returns it cleaned the way Build
// cleans its own output: control and escape characters removed and Scrub
// applied again to the title and body. On an issue Build made this
// changes nothing (Scrub is idempotent), so a real preview comes back
// byte-identical; a caller that compares the result with what it showed
// sees whether anything was removed.
//
// The issue must have a title with the kind's prefix and more (at most
// MaxTitleLen characters, one line), exactly the kind's labels, and a
// non-empty body under MaxBodyLen that carries every section heading of
// the kind's issue form. Anything else is ErrInvalidInput.
func SanitizeIssue(k Kind, is Issue) (Issue, error) {
	bad := func(msg string) (Issue, error) { return Issue{}, &Error{Code: ErrInvalidInput, Message: msg} }
	switch k {
	case KindBug, KindIdea:
	default:
		return bad(fmt.Sprintf("unknown report kind %q (want bug or idea)", k))
	}
	if strings.ContainsAny(is.Title, "\r\n") {
		return bad("issue title must be one line")
	}
	if n := utf8.RuneCountInString(is.Title); n > MaxTitleLen {
		return bad(fmt.Sprintf("issue title is %d characters; the limit is %d", n, MaxTitleLen))
	}
	if len(is.Body) > MaxBodyLen {
		return bad(fmt.Sprintf("issue body is %d bytes; the limit is %d", len(is.Body), MaxBodyLen))
	}
	home, _ := os.UserHomeDir()
	out := Issue{
		Title:  Scrub(StripControl(is.Title, false), home),
		Labels: append([]string(nil), is.Labels...),
		Body:   scrubBody(k, StripControl(is.Body, true), home),
	}
	prefix := TitlePrefix(k)
	if !strings.HasPrefix(out.Title, prefix) || strings.TrimSpace(strings.TrimPrefix(out.Title, prefix)) == "" {
		return bad(fmt.Sprintf("issue title must start with %q and say something after it", prefix))
	}
	want := LabelsFor(k)
	got := append([]string(nil), out.Labels...)
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		return bad(fmt.Sprintf("issue labels must be exactly %s", strings.Join(want, ", ")))
	}
	if strings.TrimSpace(out.Body) == "" {
		return bad("issue body must not be empty")
	}
	for _, h := range Headings(k) {
		if !strings.Contains("\n"+out.Body, "\n### "+h+"\n") {
			return bad(fmt.Sprintf("issue body lacks the %q section of the %s form", h, k))
		}
	}
	return out, nil
}

// Headings are the section headings of the kind's issue form, in order.
func Headings(k Kind) []string {
	if k == KindIdea {
		return []string{hProblem, hProposal, hAlternatives, hWilling, hAIDisclosure, hAIModel}
	}
	return []string{hVersion, hEnvironment, hRepro, hExpected, hLogs, hAIDisclosure, hAIModel}
}

var (
	// ANSI CSI (ESC [ ... final), OSC (ESC ] ... BEL or ESC \\) and
	// two-character ESC sequences.
	escSeqPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[@-Z\\-_]?`)
)

// StripControl removes terminal escape sequences, control characters
// (C0, DEL, C1) and bidi override characters from s. With multiline, new
// lines and tabs are kept; without it a tab becomes a space and new lines
// are removed.
func StripControl(s string, multiline bool) string {
	s = escSeqPattern.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && multiline, r == '\t' && multiline:
			return r
		case r == '\t':
			return ' '
		case r == '\r' && multiline:
			return -1
		case unicode.IsControl(r), r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}

type section struct{ heading, content string }

func renderBody(secs []section, footer string) string {
	var b strings.Builder
	for _, s := range secs {
		b.WriteString("### ")
		b.WriteString(s.heading)
		b.WriteString("\n\n")
		b.WriteString(s.content)
		b.WriteString("\n\n")
	}
	b.WriteString("---\n")
	b.WriteString(footer)
	b.WriteString("\n")
	return b.String()
}

// parseBody splits a body produced by renderBody back into sections and
// footer. ok is false when the body was not built by Build.
func parseBody(body string) (secs []section, footer string, ok bool) {
	head, foot, found := cutLast(body, "\n---\n")
	if !found || !strings.HasPrefix(head, "### ") {
		return nil, "", false
	}
	footer = strings.TrimSuffix(foot, "\n")
	for _, part := range strings.Split("\n"+head, "\n### ")[1:] {
		h, c, _ := strings.Cut(part, "\n\n")
		secs = append(secs, section{h, strings.TrimSuffix(c, "\n")})
	}
	return secs, footer, true
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// userText keeps user lines from being read as Markdown headings or rules
// (which would also confuse parseBody) and fills empty fields the way
// GitHub does.
func userText(s string) string {
	if strings.TrimSpace(s) == "" {
		return noResponse
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		t := strings.TrimLeft(l, " ")
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "---") {
			lines[i] = `\` + t
		}
	}
	return strings.Join(lines, "\n")
}

func logBlock(s string) string {
	if strings.TrimSpace(s) == "" {
		return noResponse
	}
	return "```text\n" + strings.ReplaceAll(s, "```", "'''") + "\n```"
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orNoResponse(s string) string {
	if strings.TrimSpace(s) == "" {
		return noResponse
	}
	return s
}

func archSuffix(a string) string {
	if a == "" {
		return ""
	}
	return " " + a
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// NewIssueURL returns the prefilled github.com "new issue" URL for is. The
// URL never exceeds MaxURLLen: the log excerpt is trimmed first (oldest
// lines dropped), then the longest other text, each cut marked
// "(trimmed)". trimmed reports whether anything was cut.
func NewIssueURL(is Issue) (string, bool) {
	u := encodeIssueURL(is)
	if len(u) <= MaxURLLen {
		return u, false
	}
	secs, footer, ok := parseBody(is.Body)
	if !ok {
		secs = []section{{"", is.Body}}
	}
	fits := func(s []section) bool {
		return len(encodeIssueURL(Issue{is.Title, is.Labels, render(s, footer, ok)})) <= MaxURLLen
	}

	// 1. The log excerpt: keep the newest lines that fit.
	for i := range secs {
		if secs[i].heading != hLogs || !strings.HasPrefix(secs[i].content, "```text\n") {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(secs[i].content, "```text\n"), "\n```"), "\n")
		build := func(keep int) string {
			if keep <= 0 {
				return trimmedMark
			}
			return "```text\n" + trimmedMark + "\n" + strings.Join(lines[len(lines)-keep:], "\n") + "\n```"
		}
		lo, hi := 0, len(lines)-1 // largest keep that fits, searched in [0, len-1]
		for lo < hi {
			mid := (lo + hi + 1) / 2
			secs[i].content = build(mid)
			if fits(secs) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		secs[i].content = build(lo)
	}

	// 2. Then the longest remaining text, repeatedly.
	for !fits(secs) {
		longest := -1
		for i := range secs {
			if strings.HasSuffix(secs[i].content, trimmedMark) && len(secs[i].content) <= len(trimmedMark)+2 {
				continue
			}
			if longest < 0 || len(secs[i].content) > len(secs[longest].content) {
				longest = i
			}
		}
		if longest < 0 {
			break
		}
		runes := []rune(strings.TrimSuffix(secs[longest].content, "\n… "+trimmedMark))
		cut := func(n int) string {
			if n <= 0 {
				return trimmedMark
			}
			return string(runes[:n]) + "\n… " + trimmedMark
		}
		lo, hi := 0, len(runes)-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			secs[longest].content = cut(mid)
			if fits(secs) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		secs[longest].content = cut(lo)
	}

	out := Issue{Title: is.Title, Labels: is.Labels, Body: render(secs, footer, ok)}
	u = encodeIssueURL(out)
	// 3. Last resort (absurd title): shorten the title.
	for len(u) > MaxURLLen && len([]rune(out.Title)) > 20 {
		r := []rune(out.Title)
		out.Title = string(r[:len(r)*3/4]) + "…"
		u = encodeIssueURL(out)
	}
	return u, true
}

func render(secs []section, footer string, structured bool) string {
	if !structured {
		return secs[0].content
	}
	return renderBody(secs, footer)
}

func encodeIssueURL(is Issue) string {
	// title, body, labels in that order (url.Values.Encode would sort).
	u := "https://github.com/" + Repo + "/issues/new?title=" + url.QueryEscape(is.Title) +
		"&body=" + url.QueryEscape(is.Body)
	if len(is.Labels) > 0 {
		u += "&labels=" + url.QueryEscape(strings.Join(is.Labels, ","))
	}
	return u
}

// Text is the plain-text preview shown to the user before anything is
// sent: title, labels and the body, exactly as they will be filed.
func (is Issue) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Title:  %s\n", is.Title)
	fmt.Fprintf(&b, "Labels: %s\n", strings.Join(is.Labels, ", "))
	b.WriteString("\n")
	b.WriteString(is.Body)
	return b.String()
}
