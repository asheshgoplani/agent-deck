package todos

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// FileName is the list's file inside the agent-deck data directory.
const FileName = "todos.json"

// Sources say where an item was added. Anything else is refused.
const (
	SourceVoice = "voice"
	SourceCLI   = "cli"
	SourcePhone = "phone"
	SourceMac   = "mac"
	SourceTUI   = "tui"
)

// Sources lists every valid source, in documentation order.
var Sources = []string{SourceVoice, SourceCLI, SourcePhone, SourceMac, SourceTUI}

// Limits.
const (
	// MaxTextRunes caps an item's text.
	MaxTextRunes = 500
	// MaxSessionIDLen caps the optional session id.
	MaxSessionIDLen = 128
	// DoneRetention is how long a done item is kept before a later write
	// drops it.
	DoneRetention = 30 * 24 * time.Hour
	// lockWait bounds how long a write waits for another writer.
	lockWait = 10 * time.Second
)

// Item is one to-do. DoneAt is null while the item is open. The JSON
// names are the stable shape of every --json output and API response.
type Item struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	CreatedAt time.Time  `json:"created_at"`
	DoneAt    *time.Time `json:"done_at"`
	Source    string     `json:"source"`
	SessionID string     `json:"session_id,omitempty"`
}

// Done reports whether the item is ticked off.
func (it Item) Done() bool { return it.DoneAt != nil }

type file struct {
	Version int    `json:"version"`
	NextID  int    `json:"next_id"`
	Items   []Item `json:"items"`
}

// Errors callers branch on.
var (
	ErrNotFound    = errors.New("no to-do matches")
	ErrAmbiguous   = errors.New("more than one to-do matches")
	ErrEmptyText   = errors.New("to-do text is empty")
	ErrTextTooLong = fmt.Errorf("to-do text is longer than %d characters", MaxTextRunes)
	ErrBadSource   = fmt.Errorf("source must be one of %s", strings.Join(Sources, ", "))
	ErrBadSession  = errors.New("session id must be at most 128 letters, digits, '-', '_' or '.'")
	ErrBadID       = errors.New("to-do id must be a number")
)

// AmbiguousError carries the candidates of an ambiguous match.
type AmbiguousError struct {
	Query      string
	Candidates []Item
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches %d to-dos", e.Query, len(e.Candidates))
}

func (e *AmbiguousError) Unwrap() error { return ErrAmbiguous }

// Store is the list on disk. A zero Store is not usable; use NewStore or
// Open.
type Store struct {
	dir string
	now func() time.Time
}

// NewStore returns a store rooted at dir (the agent-deck data directory).
func NewStore(dir string) *Store {
	return &Store{dir: dir, now: time.Now}
}

// Open returns the host's store in the agent-deck data directory.
func Open() (*Store, error) {
	dir, err := session.GetAgentDeckDir()
	if err != nil {
		return nil, fmt.Errorf("resolving agent-deck data directory: %w", err)
	}
	return NewStore(dir), nil
}

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Path is the list file.
func (s *Store) Path() string { return filepath.Join(s.dir, FileName) }

func (s *Store) clock() time.Time { return s.now().UTC().Truncate(time.Second) }

// read loads the file; a missing or empty file is an empty list.
func (s *Store) read() (file, error) {
	f := file{Version: 1, NextID: 1}
	data, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("reading to-do list: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parsing to-do list %s: %w", s.Path(), err)
	}
	// Never hand out an id already used, even if next_id was edited down.
	for _, it := range f.Items {
		if n, err := strconv.Atoi(it.ID); err == nil && n >= f.NextID {
			f.NextID = n + 1
		}
	}
	if f.NextID < 1 {
		f.NextID = 1
	}
	return f, nil
}

func (s *Store) write(f file) error {
	f.Version = 1
	cutoff := s.clock().Add(-DoneRetention)
	kept := f.Items[:0]
	for _, it := range f.Items {
		if it.DoneAt != nil && it.DoneAt.Before(cutoff) {
			continue
		}
		kept = append(kept, it)
	}
	f.Items = kept
	if f.Items == nil {
		f.Items = []Item{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding to-do list: %w", err)
	}
	if err := atomicfile.WriteFile(s.Path(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing to-do list: %w", err)
	}
	return nil
}

// update runs fn on the list under the cross-process lock and saves the
// result when fn returns save=true.
func (s *Store) update(fn func(f *file) (save bool, err error)) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating agent-deck data directory: %w", err)
	}
	lock, err := session.AcquireConfigFileLockTimeout(s.Path(), lockWait)
	if err != nil {
		return fmt.Errorf("locking to-do list: %w", err)
	}
	defer lock.Release()
	f, err := s.read()
	if err != nil {
		return err
	}
	save, err := fn(&f)
	if err != nil || !save {
		return err
	}
	return s.write(f)
}

// CleanText trims the text, folds runs of whitespace (newlines included)
// into one space and drops other control characters.
func CleanText(text string) string {
	var b strings.Builder
	space := false
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r) || r == utf8.RuneError:
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// ValidSource reports whether s is one of Sources.
func ValidSource(s string) bool {
	for _, v := range Sources {
		if s == v {
			return true
		}
	}
	return false
}

func validSessionID(id string) bool {
	if len(id) > MaxSessionIDLen {
		return false
	}
	for _, r := range id {
		if !(r == '-' || r == '_' || r == '.' || r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r))) {
			return false
		}
	}
	return true
}

// AddOptions are the optional fields of a new item.
type AddOptions struct {
	Source    string // default SourceCLI
	SessionID string
}

// Add appends an open item and returns it.
func (s *Store) Add(text string, opts AddOptions) (Item, error) {
	text = CleanText(text)
	if text == "" {
		return Item{}, ErrEmptyText
	}
	if utf8.RuneCountInString(text) > MaxTextRunes {
		return Item{}, ErrTextTooLong
	}
	if opts.Source == "" {
		opts.Source = SourceCLI
	}
	if !ValidSource(opts.Source) {
		return Item{}, ErrBadSource
	}
	opts.SessionID = strings.TrimSpace(opts.SessionID)
	if !validSessionID(opts.SessionID) {
		return Item{}, ErrBadSession
	}
	var out Item
	err := s.update(func(f *file) (bool, error) {
		out = Item{
			ID:        strconv.Itoa(f.NextID),
			Text:      text,
			CreatedAt: s.clock(),
			Source:    opts.Source,
			SessionID: opts.SessionID,
		}
		f.NextID++
		f.Items = append(f.Items, out)
		return true, nil
	})
	if err != nil {
		return Item{}, err
	}
	return out, nil
}

// List returns the open items, oldest first; with all, done items too
// (open ones first, then done ones by when they were done).
func (s *Store) List(all bool) ([]Item, error) {
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(f.Items))
	for _, it := range f.Items {
		if all || !it.Done() {
			out = append(out, it)
		}
	}
	sortItems(out)
	return out, nil
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Done() != b.Done() {
			return !a.Done()
		}
		if a.Done() && !a.DoneAt.Equal(*b.DoneAt) {
			return a.DoneAt.Before(*b.DoneAt)
		}
		return idLess(a.ID, b.ID)
	})
}

func idLess(a, b string) bool {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	if ea == nil && eb == nil {
		return na < nb
	}
	return a < b
}

// Counts returns how many items are open and done.
func (s *Store) Counts() (open, done int, err error) {
	f, err := s.read()
	if err != nil {
		return 0, 0, err
	}
	for _, it := range f.Items {
		if it.Done() {
			done++
		} else {
			open++
		}
	}
	return open, done, nil
}

// IsID reports whether q has the form of an item id.
func IsID(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 9 {
		return false
	}
	for _, r := range q {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normID(q string) string {
	n, err := strconv.Atoi(strings.TrimSpace(q))
	if err != nil {
		return strings.TrimSpace(q)
	}
	return strconv.Itoa(n)
}

func indexOf(f *file, id string) int {
	id = normID(id)
	for i, it := range f.Items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// Done ticks off the item named by query: an id (a bare number is only
// ever an id), or words of its text (Match). changed is false when the item was already done.
func (s *Store) Done(query string) (it Item, changed bool, err error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Item{}, false, ErrNotFound
	}
	err = s.update(func(f *file) (bool, error) {
		i := -1
		if IsID(query) {
			// A bare number is an id and only an id: "done 12" never ticks
			// off "call 12 people" when item 12 does not exist.
			if i = indexOf(f, query); i < 0 {
				return false, ErrNotFound
			}
		} else {
			idx, merr := matchIndex(f.Items, query)
			if merr != nil {
				return false, merr
			}
			i = idx
		}
		it = f.Items[i]
		if it.Done() {
			return false, nil
		}
		t := s.clock()
		f.Items[i].DoneAt = &t
		it = f.Items[i]
		changed = true
		return true, nil
	})
	return it, changed, err
}

// Undo reopens the done item id. changed is false when it was open.
func (s *Store) Undo(id string) (it Item, changed bool, err error) {
	if !IsID(id) {
		return Item{}, false, ErrBadID
	}
	err = s.update(func(f *file) (bool, error) {
		i := indexOf(f, id)
		if i < 0 {
			return false, ErrNotFound
		}
		it = f.Items[i]
		if !it.Done() {
			return false, nil
		}
		f.Items[i].DoneAt = nil
		it = f.Items[i]
		changed = true
		return true, nil
	})
	return it, changed, err
}

// Remove deletes item id and returns what it was.
func (s *Store) Remove(id string) (Item, error) {
	if !IsID(id) {
		return Item{}, ErrBadID
	}
	var it Item
	err := s.update(func(f *file) (bool, error) {
		i := indexOf(f, id)
		if i < 0 {
			return false, ErrNotFound
		}
		it = f.Items[i]
		f.Items = append(f.Items[:i], f.Items[i+1:]...)
		return true, nil
	})
	return it, err
}
