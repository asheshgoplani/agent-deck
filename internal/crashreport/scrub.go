package crashreport

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/getsentry/sentry-go"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
)

// Placeholders scrubEvent writes in place of what it removes, next to the
// issuereport ones (<token>, <email>, <host>, <ip>, <user>, <private-key>).
const (
	PlaceholderText = "<text>"
	PlaceholderPath = "<path>"
)

// maxMessageRunes bounds every free-text field that is kept.
const maxMessageRunes = 200

var (
	// Quoted spans: Go's %q output, single quotes, backticks and typographic
	// quotes. Session titles, prompts and command lines reach error text
	// this way; the quoted part is never sent.
	quotedPattern = regexp.MustCompile("\"(?:[^\"\\\\\\n]|\\\\.)*\"|'[^'\\n]*'|`[^`\\n]*`|“[^”\\n]*”|‘[^’\\n]*’")
	// Paths: absolute (/x/y), home-relative (~ or ~/x, what Scrub leaves
	// of a home path), relative with a directory (a/b), and Windows ones.
	pathPattern = regexp.MustCompile(`(?:~(?:/[^\s:;,()<>\[\]{}]*)?|/[^\s:;,()<>\[\]{}]+|\b[A-Za-z]:\\[^\s:;,()<>]+|\b[\w.-]+(?:/[\w.-]+)+)`)
	// Breadcrumb categories and exception types are code-chosen names.
	categoryPattern = regexp.MustCompile(`^[a-z0-9_.-]{1,40}$`)
	typePattern     = regexp.MustCompile(`^[A-Za-z0-9_.*\[\]]{1,80}$`)
	releasePattern  = regexp.MustCompile(`^[0-9A-Za-z.+_-]{1,40}$`)
	// The repository module path; frames under it keep a repo-relative name.
	modulePrefix = "github.com/asheshgoplani/agent-deck/"
)

// digitsPattern: runs of digits, normalized before hashing so ports, pids,
// counts and line numbers do not split one error into many groups.
var digitsPattern = regexp.MustCompile(`[0-9]+`)

// MessageHashPrefix marks the only form in which a message leaves the
// machine: "msg:" and 12 hex characters.
const MessageHashPrefix = "msg:"

// MessageHash is what is sent in place of an error message or panic value:
// the first 12 hex characters of the SHA-256 of the scrubbed message
// (ScrubText) with every run of digits replaced by "0". It groups equal
// messages across machines and carries none of their words. The text
// itself is never sent: unquoted session titles, group names and prompts
// reach error text through %s as often as through %q, and no pattern
// scrubber can find them.
func MessageHash(s, home string) string {
	if s == "" {
		return ""
	}
	norm := digitsPattern.ReplaceAllString(ScrubText(s, home), "0")
	sum := sha256.Sum256([]byte(norm))
	return MessageHashPrefix + hex.EncodeToString(sum[:])[:12]
}

// ScrubText normalizes free text before MessageHash: the first line only,
// quoted spans replaced by <text>, issuereport.Scrub (home, tokens, keys,
// emails, hosts, IPs, user names), then any remaining path replaced by
// <path>, at most 200 runes. Its output is hashed, never sent: it still
// contains unquoted words.
func ScrubText(s, home string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = quotedPattern.ReplaceAllString(s, PlaceholderText)
	s = issuereport.Scrub(s, home)
	s = pathPattern.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "<") {
			return m
		}
		return PlaceholderPath
	})
	return truncateRunes(strings.TrimSpace(s), maxMessageRunes)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// scrubEvent rebuilds ev from an allow-list. Messages and exception values
// become MessageHash; no free text is sent. Nothing the SDK or a caller
// put anywhere else (request, user, server name, contexts, extra, modules,
// fingerprint, transaction, logger, breadcrumb data or messages, frame
// source context, variables or absolute paths) survives.
func scrubEvent(ev *sentry.Event, c Config) *sentry.Event {
	if ev == nil {
		return nil
	}
	out := sentry.NewEvent()
	out.EventID = ev.EventID
	out.Timestamp = ev.Timestamp
	out.Level = ev.Level
	out.Platform = "go"
	out.Sdk = sentry.SdkInfo{Name: ev.Sdk.Name, Version: ev.Sdk.Version}
	out.Release = safeRelease(c.Release)
	out.Environment = c.Environment
	out.Tags = tagsFor(c)
	out.Message = MessageHash(ev.Message, c.Home)
	for _, e := range ev.Exception {
		x := sentry.Exception{Type: safeType(e.Type), Value: MessageHash(e.Value, c.Home), Stacktrace: scrubStack(e.Stacktrace)}
		if e.Mechanism != nil {
			x.Mechanism = &sentry.Mechanism{Type: safeCategory(e.Mechanism.Type), Handled: e.Mechanism.Handled,
				ExceptionID: e.Mechanism.ExceptionID, ParentID: e.Mechanism.ParentID, IsExceptionGroup: e.Mechanism.IsExceptionGroup}
		}
		out.Exception = append(out.Exception, x)
	}
	for _, t := range ev.Threads {
		out.Threads = append(out.Threads, sentry.Thread{Stacktrace: scrubStack(t.Stacktrace), Crashed: t.Crashed, Current: t.Current})
	}
	for _, b := range ev.Breadcrumbs {
		if b == nil {
			continue
		}
		out.Breadcrumbs = append(out.Breadcrumbs, &sentry.Breadcrumb{
			Type: safeCategory(b.Type), Category: safeCategory(b.Category), Level: b.Level, Timestamp: b.Timestamp,
		})
	}
	return out
}

// scrubStack keeps function, package, line and a repo-relative file name.
func scrubStack(st *sentry.Stacktrace) *sentry.Stacktrace {
	if st == nil {
		return nil
	}
	out := &sentry.Stacktrace{}
	for _, f := range st.Frames {
		out.Frames = append(out.Frames, sentry.Frame{
			Function: f.Function,
			Module:   f.Module,
			Filename: frameFile(f),
			Lineno:   f.Lineno,
			InApp:    f.InApp,
		})
	}
	return out
}

// frameFile is the file of a frame relative to its module: repository
// files keep their repo path (internal/session/x.go), any other file only
// its base name, so no build or install path is sent.
func frameFile(f sentry.Frame) string {
	name := f.Filename
	if name == "" {
		name = f.AbsPath
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, modulePrefix); i >= 0 {
		return name[i+len(modulePrefix):]
	}
	if strings.HasPrefix(f.Module, strings.TrimSuffix(modulePrefix, "/")) && !path.IsAbs(name) && !strings.Contains(name, "..") {
		return name
	}
	return path.Base(name)
}

func safeCategory(s string) string {
	if s == "" || categoryPattern.MatchString(s) {
		return s
	}
	return "other"
}

func safeType(s string) string {
	if typePattern.MatchString(s) {
		return s
	}
	return "error"
}

func safeRelease(s string) string {
	if releasePattern.MatchString(s) {
		return "agent-deck@" + s
	}
	return "agent-deck@unknown"
}
