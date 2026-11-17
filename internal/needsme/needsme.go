// Package needsme answers "which of my sessions are waiting for me": every
// local profile plus every configured remote, in parallel, read-only.
package needsme

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultTimeout is the per-host time budget.
const DefaultTimeout = 3 * time.Second

// Row is the part of a `list --json` row this package reads.
type Row struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Group          string `json:"group"`
	Profile        string `json:"profile"`
	Status         string `json:"status"`
	Substate       string `json:"substate"`
	SubstateDetail string `json:"substate_detail"`
	LastActivityAt string `json:"last_activity_at"` // not used for ordering
	// WaitingSince is when the session began waiting, when the host says so.
	WaitingSince string `json:"waiting_since"`
	CreatedAt    string `json:"created_at"`
	Archived     bool   `json:"archived"`
}

// Source is one place to ask: a local profile or a remote host.
type Source struct {
	Host    string // "" = this machine, else the remote name
	Profile string // local profile name (informational for remotes)
	Fetch   func(ctx context.Context) ([]Row, error)
}

// Options configure Collect.
type Options struct {
	Sources []Source
	Timeout time.Duration
	Now     func() time.Time
}

// Session is one waiting session (desk contract section 5).
type Session struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Group          string `json:"group"`
	Profile        string `json:"profile"`
	Host           string `json:"host"`
	Status         string `json:"status"`
	Substate       string `json:"substate"`
	Reason         string `json:"reason"`
	Since          string `json:"since"`
	WaitingSeconds int64  `json:"waiting_seconds"`
}

// HostError is a host that could not be asked.
type HostError struct {
	Host    string `json:"host"`
	Profile string `json:"profile,omitempty"`
	Error   string `json:"error"`
}

// Result is the `needs-me --json` object.
type Result struct {
	Count    int         `json:"count"`
	Sessions []Session   `json:"sessions"`
	Errors   []HostError `json:"errors"`
}

// Reason is the one-line human reason for a waiting session, from the
// substate and its free-text detail (detail is only shown for the limit and
// blocked substates).
func Reason(substate, detail string) string {
	detail = cleanDetail(detail)
	withDetail := func(base string) string {
		if detail == "" {
			return base
		}
		return base + " (" + detail + ")"
	}
	switch substate {
	case "interactive-menu":
		return "asks a question"
	case "idle-at-empty-prompt":
		return "finished, waiting for your next message"
	case "auth-401":
		return "needs you to log in"
	case "usage-limit":
		return withDetail("hit a usage limit")
	case "model-limit":
		return withDetail("hit a model limit")
	case "model-unavailable":
		return "model unavailable"
	case "account-blocked":
		return withDetail("account blocked")
	case "unknown-exit", "process-exited":
		return "process exited"
	}
	return "waiting for you"
}

func cleanDetail(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(' ')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if rs := []rune(out); len(rs) > 80 {
		out = string(rs[:80])
	}
	return out
}

type fetched struct {
	rows []Row
	err  error
}

// Collect asks every source in parallel (each bounded by opts.Timeout) and
// returns the waiting sessions, longest-waiting first. A source that fails or
// times out becomes an entry in Errors; Collect itself never fails.
func Collect(ctx context.Context, opts Options) Result {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	out := make([]fetched, len(opts.Sources))
	var wg sync.WaitGroup
	for i, src := range opts.Sources {
		wg.Add(1)
		go func(i int, src Source) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			done := make(chan fetched, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- fetched{err: errFailed}
					}
				}()
				rows, err := src.Fetch(cctx)
				done <- fetched{rows, err}
			}()
			select {
			case f := <-done:
				if f.err != nil && cctx.Err() == context.DeadlineExceeded {
					f.err = context.DeadlineExceeded
				}
				out[i] = f
			case <-cctx.Done():
				out[i] = fetched{err: cctx.Err()}
			}
		}(i, src)
	}
	wg.Wait()

	res := Result{Sessions: []Session{}, Errors: []HostError{}}
	seen := map[string]bool{}
	for i, src := range opts.Sources {
		f := out[i]
		if f.err != nil {
			e := HostError{Host: src.Host, Error: errorWord(src, f.err)}
			if src.Host == "" {
				e.Profile = src.Profile
			}
			res.Errors = append(res.Errors, e)
			continue
		}
		for _, r := range f.rows {
			if r.Status != "waiting" || r.Archived {
				continue
			}
			profile := r.Profile
			if profile == "" {
				profile = src.Profile
			}
			key := src.Host + "\x00" + profile + "\x00" + r.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			s := Session{ID: r.ID, Title: r.Title, Group: r.Group, Profile: profile, Host: src.Host,
				Status: "waiting", Substate: r.Substate, Reason: Reason(r.Substate, r.SubstateDetail)}
			// Only an explicit waiting_since counts (`list --json` writes it
			// from state.db's status-change stamp). last_activity_at can be
			// the creation time, which would give a wrong wait and order. A
			// time in the future (a skewed or hostile remote clock) is
			// unknown, so it can never sort a session first.
			if t, ok := parseTime(r.WaitingSince); ok && !t.After(now().Add(time.Minute)) {
				s.Since = r.WaitingSince
				if w := int64(now().Sub(t) / time.Second); w > 0 {
					s.WaitingSeconds = w
				}
			}
			res.Sessions = append(res.Sessions, s)
		}
	}
	sort.SliceStable(res.Sessions, func(i, j int) bool {
		a, b := res.Sessions[i], res.Sessions[j]
		if (a.Since == "") != (b.Since == "") {
			return a.Since != ""
		}
		if a.WaitingSeconds != b.WaitingSeconds {
			return a.WaitingSeconds > b.WaitingSeconds
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.ID < b.ID
	})
	res.Count = len(res.Sessions)
	return res
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// Line is the plain-text line for a session: "title [host]: reason, waiting 2m".
func Line(s Session) string {
	var b strings.Builder
	b.WriteString(s.Title)
	if s.Host != "" {
		b.WriteString(" [" + s.Host + "]")
	}
	b.WriteString(": " + s.Reason)
	switch w := s.WaitingSeconds; {
	case w >= 3600:
		fmt.Fprintf(&b, ", waiting %dh", w/3600)
	case w >= 60:
		fmt.Fprintf(&b, ", waiting %dm", w/60)
	case w > 0:
		fmt.Fprintf(&b, ", waiting %ds", w)
	}
	return b.String()
}

// ErrBadReply marks a host that answered with something unreadable.
var ErrBadReply = errors.New("bad-reply")

var errFailed = errors.New("failed")

// errorWord maps a fetch error to one fixed word; raw text (addresses, user
// names) never reaches the output. timeout: the source ran out of time;
// bad-reply: the reply was not a session list; failed: a local source
// failed, or any source crashed; unreachable: any other remote failure
// (ssh, a missing agent-deck, a non-zero exit).
func errorWord(src Source, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrBadReply):
		return "bad-reply"
	case errors.Is(err, errFailed) || src.Host == "":
		return "failed"
	}
	return "unreachable"
}

// ErrorLine is the plain-text line for an errors entry.
func ErrorLine(e HostError) string {
	where := e.Host
	if where == "" {
		where = "this machine"
		if e.Profile != "" {
			where += " (profile " + e.Profile + ")"
		}
	}
	return where + ": " + e.Error
}
