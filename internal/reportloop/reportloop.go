// Package reportloop closes the loop between a problem report and the
// release that fixes it (ASK-205):
//
//	report (feedback bug) -> recorded here -> a release's CHANGELOG cites
//	#N under "### Fixes" -> "What's new" shows "Your report #N: fixed in vX"
//	-> "Is it fixed for you?" -> the answer is recorded so it is asked once.
//
// The store is one JSON file per machine (reports.json in the agent-deck
// data dir, mode 0600), changed under a file lock (reports.json.lock) so
// two agent-deck processes never lose each other's rows. A report counts
// as fixed in the oldest version after the one it was filed on (and not
// newer than the running one) whose "### Fixes" or "### Fixed" section
// cites its number (after a "no" answer, a version newer than that answer:
// a later fix is asked about again); the changelog is the file the update path already
// fetches, so this works without gh or network beyond that.
package reportloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

var storeLog = logging.ForComponent(logging.CompUpdate)

// FileName is the store's file name in the agent-deck data dir.
const FileName = "reports.json"

// Feedback is the reporter's answer to "Is it fixed for you?".
type Feedback struct {
	Answer  string    `json:"answer"` // "yes" or "no"
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// Report is one issue this machine filed (or re-confirmed as a duplicate).
type Report struct {
	Number       int       `json:"number"`
	URL          string    `json:"url,omitempty"`
	Title        string    `json:"title"`
	Kind         string    `json:"kind,omitempty"`
	FiledAt      time.Time `json:"filed_at"`
	FiledVersion string    `json:"filed_version"`
	FromAgent    bool      `json:"from_agent,omitempty"`
	// Deduped is true when the report matched an open issue and was added
	// to it as a comment instead of a new issue.
	Deduped bool `json:"deduped,omitempty"`
	// Seen counts how often this machine reported the problem.
	Seen     int       `json:"seen"`
	FixedIn  string    `json:"fixed_in,omitempty"`
	Feedback *Feedback `json:"feedback,omitempty"`
	// NotFixedIn is the newest version the reporter answered "no" on. It
	// outlives the cleared answer so a later refresh never falls back to a
	// fix the reporter already said did not work.
	NotFixedIn string `json:"not_fixed_in,omitempty"`
}

// State is the whole store.
type State struct {
	// LastSeenVersion is the newest version whose "What's new" was shown
	// (or the version of the first run). Empty until the first run.
	LastSeenVersion string   `json:"last_seen_version,omitempty"`
	Reports         []Report `json:"reports"`
}

// ErrUnknownReport is returned for feedback on a number this machine did not file.
var ErrUnknownReport = errors.New("not one of this machine's reports")

// Store reads and writes reports.json. Path empty means DefaultPath().
type Store struct {
	Path string
	mu   sync.Mutex
}

// DefaultPath is <agent-deck data dir>/reports.json.
func DefaultPath() (string, error) {
	return agentpaths.EffectiveDataPath(FileName, FileName)
}

// Default returns the store at DefaultPath.
func Default() (*Store, error) {
	p, err := DefaultPath()
	if err != nil {
		return nil, fmt.Errorf("resolving reports store path: %w", err)
	}
	return &Store{Path: p}, nil
}

// Load reads the store. A missing file is an empty state. A malformed
// file is moved aside to reports.json.bad-<unix time> (kept for a person
// to look at, and logged) and the store starts empty, so one bad write
// never blocks reporting. Taking the lock is best effort for a read: if it
// fails, the failure is logged and the file is read without it (the write
// is atomic, so a reader never sees half a file).
func (s *Store) Load() (st *State, err error) {
	unlock, lerr := s.lock()
	if lerr != nil {
		storeLog.Warn("reports_store_lock_failed_reading_unlocked",
			slog.String("path", s.Path), slog.String("error", lerr.Error()))
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.loadLocked()
	}
	defer func() {
		// A read changed nothing: a release error is logged, not returned.
		if uerr := unlock(); uerr != nil {
			storeLog.Warn("reports_store_unlock_failed", slog.String("path", s.Path), slog.String("error", uerr.Error()))
		}
	}()
	return s.loadLocked()
}

// lock takes the in-process mutex and the cross-process file lock on
// <Path>.lock (flock where the OS has it). The returned func releases both.
func (s *Store) lock() (func() error, error) {
	s.mu.Lock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("creating reports dir: %w", err)
	}
	release, err := lockFile(s.Path + ".lock")
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("locking %s: %w", s.Path, err)
	}
	return func() error {
		defer s.mu.Unlock()
		return release()
	}, nil
}

func (s *Store) loadLocked() (*State, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.Path, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		bad := fmt.Sprintf("%s.bad-%d", s.Path, time.Now().Unix())
		if rerr := os.Rename(s.Path, bad); rerr != nil {
			return nil, fmt.Errorf("parsing %s: %w (and moving it aside failed: %v)", s.Path, err, rerr)
		}
		storeLog.Warn("reports_store_malformed_moved_aside",
			slog.String("path", s.Path), slog.String("moved_to", bad), slog.String("error", err.Error()))
		return &State{}, nil
	}
	return &st, nil
}

func (s *Store) saveLocked(st *State) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("creating reports dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding reports: %w", err)
	}
	if err := atomicfile.WriteFile(s.Path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", s.Path, err)
	}
	return nil
}

// mutate runs fn on the stored state under the lock and saves the result.
func (s *Store) mutate(fn func(*State) error) error {
	return s.mutateIf(func(st *State) (bool, error) { return true, fn(st) })
}

// mutateIf is mutate that saves only when fn reports a change.
func (s *Store) mutateIf(fn func(*State) (bool, error)) (err error) {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() {
		if uerr := unlock(); uerr != nil {
			err = errors.Join(err, uerr)
		}
	}()
	st, err := s.loadLocked()
	if err != nil {
		return err
	}
	changed, err := fn(st)
	if err != nil || !changed {
		return err
	}
	return s.saveLocked(st)
}

// Record adds r, or bumps Seen on the existing row with the same number
// (the first title, URL and filed version are kept).
func (s *Store) Record(r Report) error {
	if r.Number <= 0 {
		return fmt.Errorf("recording report: issue number must be positive, got %d", r.Number)
	}
	return s.mutate(func(st *State) error {
		for i := range st.Reports {
			if st.Reports[i].Number == r.Number {
				st.Reports[i].Seen++
				if st.Reports[i].URL == "" {
					st.Reports[i].URL = r.URL
				}
				return nil
			}
		}
		if r.FiledAt.IsZero() {
			r.FiledAt = time.Now().UTC()
		}
		r.Seen = 1
		st.Reports = append(st.Reports, r)
		return nil
	})
}

// MarkSeen sets LastSeenVersion to v (never moves it backwards).
func (s *Store) MarkSeen(v string) error {
	v = strings.TrimPrefix(v, "v")
	return s.mutate(func(st *State) error {
		if st.LastSeenVersion == "" || update.CompareVersions(v, st.LastSeenVersion) > 0 {
			st.LastSeenVersion = v
		}
		return nil
	})
}

// RecordFeedback stores the answer ("yes"/"no") for report n.
func (s *Store) RecordFeedback(n int, answer, version string) error {
	if answer != "yes" && answer != "no" {
		return fmt.Errorf("recording feedback: answer must be yes or no, got %q", answer)
	}
	return s.mutate(func(st *State) error {
		for i := range st.Reports {
			if st.Reports[i].Number == n {
				v := strings.TrimPrefix(version, "v")
				st.Reports[i].Feedback = &Feedback{Answer: answer, Version: v, At: time.Now().UTC()}
				if answer == "no" && v != "" && (st.Reports[i].NotFixedIn == "" || update.CompareVersions(v, st.Reports[i].NotFixedIn) > 0) {
					st.Reports[i].NotFixedIn = v
				}
				return nil
			}
		}
		return fmt.Errorf("recording feedback on #%d: %w", n, ErrUnknownReport)
	})
}

// Find returns report n, or nil.
func (st *State) Find(n int) *Report {
	for i := range st.Reports {
		if st.Reports[i].Number == n {
			return &st.Reports[i]
		}
	}
	return nil
}

var issueRef = regexp.MustCompile(`#(\d+)\b`)

// FixedIn maps each number in nums to the OLDEST changelog version whose
// "### Fixes" (or "### Fixed") section cites it (#N as a whole number).
// Numbers not cited are absent. It has no bounds; FixedInBounded is what
// a report's state uses.
func FixedIn(changelog string, nums []int) map[int]string {
	after := make(map[int]string, len(nums))
	for _, n := range nums {
		after[n] = ""
	}
	return FixedInBounded(changelog, after, "")
}

// FixedInBounded maps each number in after (number -> the version it was
// filed on) to the OLDEST changelog version that cites it under "### Fixes"
// or "### Fixed" AND is strictly newer than that filed version (an older
// line citing the same number is about something else), and not newer than
// upTo (the running version: a fix that is not installed is not fixed for
// this person). An empty filed version or upTo means no bound on that side.
func FixedInBounded(changelog string, after map[int]string, upTo string) map[int]string {
	upTo = strings.TrimPrefix(upTo, "v")
	out := map[int]string{}
	for _, e := range update.ParseChangelog(changelog) {
		v := strings.TrimPrefix(e.Version, "v")
		if strings.EqualFold(v, "unreleased") {
			continue
		}
		if upTo != "" && update.CompareVersions(v, upTo) > 0 {
			continue
		}
		for _, m := range issueRef.FindAllStringSubmatch(fixesSection(e.Content), -1) {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			filed, ok := after[n]
			if !ok {
				continue
			}
			if filed = strings.TrimPrefix(filed, "v"); filed != "" && update.CompareVersions(v, filed) <= 0 {
				continue
			}
			if cur, ok := out[n]; !ok || update.CompareVersions(v, cur) < 0 {
				out[n] = v
			}
		}
	}
	return out
}

// fixesSection returns the text under "### Fixes" or "### Fixed" (up to
// the next ###); the real CHANGELOG.md uses both headings.
func fixesSection(content string) string {
	return section(content, "fixes", "fixed")
}

// section returns the text under any "### <name>" heading in names
// (case-insensitive), each up to the next ### heading.
func section(content string, names ...string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "### ") {
			h := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			in = false
			for _, name := range names {
				in = in || strings.EqualFold(h, name)
			}
			continue
		}
		if in {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// VersionNotes is one version's part of "What's new".
type VersionNotes struct {
	Version    string   `json:"version"`
	Date       string   `json:"date,omitempty"`
	Highlights []string `json:"highlights"`
}

// FixedReport is one of this machine's reports fixed in the range.
type FixedReport struct {
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	URL      string    `json:"url,omitempty"`
	FixedIn  string    `json:"fixed_in"`
	Feedback *Feedback `json:"feedback,omitempty"`
}

// WhatsNew is what to show after an update.
type WhatsNew struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	Fixed    []FixedReport  `json:"fixed"`
	Versions []VersionNotes `json:"versions"`
}

// Empty reports whether there is nothing to show.
func (w *WhatsNew) Empty() bool { return len(w.Fixed) == 0 && len(w.Versions) == 0 }

// Highlights returns every version's highlight bullets, newest first.
func (w *WhatsNew) Highlights() []string {
	var out []string
	for _, v := range w.Versions {
		out = append(out, v.Highlights...)
	}
	return out
}

// NeedsFeedback returns the fixed reports not answered yet.
func (w *WhatsNew) NeedsFeedback() []FixedReport {
	var out []FixedReport
	for _, f := range w.Fixed {
		if f.Feedback == nil {
			out = append(out, f)
		}
	}
	return out
}

// WhatsNew computes the notes for (since, current]. since empty means the
// stored LastSeenVersion; with neither (a first run) nothing is shown. It
// also persists FixedIn on the matched reports, writing the store only when
// one changed. It does not mark the version seen: callers do that once the
// user has seen it (MarkSeen).
func (s *Store) WhatsNew(changelog, current, since string) (*WhatsNew, error) {
	current = strings.TrimPrefix(current, "v")
	since = strings.TrimPrefix(since, "v")
	wn := &WhatsNew{To: current, Fixed: []FixedReport{}, Versions: []VersionNotes{}}
	err := s.mutateIf(func(st *State) (bool, error) {
		if since == "" {
			since = st.LastSeenVersion
		}
		wn.From = since
		if since == "" || update.CompareVersions(current, since) <= 0 {
			return false, nil
		}
		entries := update.GetChangesBetweenVersions(update.ParseChangelog(changelog), since, current)
		sort.SliceStable(entries, func(i, j int) bool {
			return update.CompareVersions(entries[i].Version, entries[j].Version) > 0
		})
		for _, e := range entries {
			wn.Versions = append(wn.Versions, VersionNotes{Version: strings.TrimPrefix(e.Version, "v"), Date: e.Date, Highlights: bullets(section(e.Content, "highlights"))})
		}
		changed := false
		for i := range st.Reports {
			r := &st.Reports[i]
			if Refresh(changelog, r, current) {
				changed = true
			}
			if v := r.FixedIn; v != "" && update.CompareVersions(v, since) > 0 {
				wn.Fixed = append(wn.Fixed, FixedReport{Number: r.Number, Title: r.Title, URL: r.URL, FixedIn: r.FixedIn, Feedback: r.Feedback})
			}
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	return wn, nil
}

// ReportFixedIn is the version that fixed r for someone running current:
// the oldest version citing r under "### Fixes" that is newer than r's
// lower bound (LowerBound) and not newer than current. With no changelog
// (changelog == "": it could not be fetched) the stored FixedIn stands
// only within the same bounds; with a changelog, the changelog decides.
func ReportFixedIn(changelog string, r Report, current string) string {
	current = strings.TrimPrefix(current, "v")
	low := LowerBound(r)
	if changelog != "" {
		return FixedInBounded(changelog, map[int]string{r.Number: low}, current)[r.Number]
	}
	v := strings.TrimPrefix(r.FixedIn, "v")
	switch {
	case v == "":
		return ""
	case low != "" && update.CompareVersions(v, low) <= 0:
		return ""
	case current != "" && update.CompareVersions(v, current) > 0:
		return ""
	}
	return v
}

// LowerBound is the version a fix for r must be newer than: the version it
// was filed on, or, after a "no" ("still broken for me in vX"), vX when that
// is newer. A release citing r after a "no" is a new fix to ask about.
func LowerBound(r Report) string {
	low := strings.TrimPrefix(r.FiledVersion, "v")
	if v := strings.TrimPrefix(r.NotFixedIn, "v"); v != "" && (low == "" || update.CompareVersions(v, low) > 0) {
		low = v
	}
	if fb := r.Feedback; fb != nil && fb.Answer == "no" {
		if v := strings.TrimPrefix(fb.Version, "v"); v != "" && (low == "" || update.CompareVersions(v, low) > 0) {
			low = v
		}
	}
	return low
}

// Refresh recomputes r.FixedIn for current (ReportFixedIn) and clears a "no"
// answer that a newer fix supersedes, so "Is it fixed for you?" is asked
// again. It reports whether r changed.
func Refresh(changelog string, r *Report, current string) bool {
	v := ReportFixedIn(changelog, *r, current)
	changed := false
	if fb := r.Feedback; v != "" && fb != nil && fb.Answer == "no" {
		// v is newer than the "no" (LowerBound): a new fix to ask about.
		r.Feedback = nil
		changed = true
	}
	if v != r.FixedIn {
		r.FixedIn = v
		changed = true
	}
	return changed
}

func bullets(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(t, "- ")))
		}
	}
	return out
}

// FixedLine is the one-line headline for a fixed report.
func FixedLine(f FixedReport) string {
	return fmt.Sprintf("Your report #%d: fixed in v%s (%s)", f.Number, f.FixedIn, f.Title)
}

// FeedbackCommand is the CLI that answers "Is it fixed for you?": the
// form a person types, and the flags an agent session adds.
func FeedbackCommand(n int) string {
	return fmt.Sprintf("agent-deck feedback fixed %d --fixed|--not-fixed [--comment TEXT] (agents add --from-agent --json)", n)
}

// Text renders the plain-text block (CLI and `agent-deck update`).
// The first line is the title; your fixed reports come right after it.
func (w *WhatsNew) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "What's new in v%s\n", w.To)
	for _, f := range w.Fixed {
		b.WriteString(FixedLine(f) + "\n")
		if f.Feedback == nil {
			fmt.Fprintf(&b, "  Is it fixed for you? %s\n", FeedbackCommand(f.Number))
		} else {
			fmt.Fprintf(&b, "  Your answer: %s (thank you)\n", f.Feedback.Answer)
		}
	}
	for _, v := range w.Versions {
		if len(v.Highlights) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\nv%s\n", v.Version)
		for _, h := range v.Highlights {
			b.WriteString("  - " + h + "\n")
		}
	}
	return b.String()
}

// ChangelogEnv overrides where the changelog is read from: an http(s) URL
// or a local file path. Tests and the sandbox simulation set it; unset means
// the CHANGELOG.md on GitHub main (the same file `agent-deck update` shows).
const ChangelogEnv = update.ChangelogEnv

// FetchChangelog returns the changelog text (at most 1 MiB).
func FetchChangelog(ctx context.Context) (string, error) {
	src := strings.TrimSpace(os.Getenv(ChangelogEnv))
	if src == "" {
		src = "https://raw.githubusercontent.com/" + update.GitHubRepo + "/main/CHANGELOG.md"
	}
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		data, err := os.ReadFile(strings.TrimPrefix(src, "file://")) // #nosec G304 -- operator-set override
		if err != nil {
			return "", fmt.Errorf("reading changelog: %w", err)
		}
		return string(data), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// #nosec G704 -- src is the fixed GitHub CHANGELOG URL, or the
	// AGENTDECK_CHANGELOG_URL the operator set (tests, the sandbox
	// simulation); nothing request- or user-supplied reaches it.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", fmt.Errorf("building changelog request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- same constrained destination as the request above
	if err != nil {
		return "", fmt.Errorf("fetching changelog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching changelog: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading changelog: %w", err)
	}
	return string(data), nil
}
