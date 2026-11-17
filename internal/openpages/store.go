// Package openpages remembers the pages `agent-deck open` showed for a
// session, so a client other than the Mac app (the phone, through the web
// API) can list them and fetch their content.
//
// Each profile keeps one small JSON file per session under
// "<data dir>/pages/<profile>/<session id>.json", newest page last, capped at
// MaxPerSession. The file holds metadata only (path, kind, title, time); the
// content is read from disk when it is served, under the rules of Roots.
package openpages

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// MaxPerSession is how many pages a session remembers; older ones drop off.
const MaxPerSession = 50

// Kinds of page.
const (
	KindHTML     = "html"
	KindMarkdown = "markdown"
	KindText     = "text"
	KindImage    = "image"
	KindURL      = "url"
)

// Where a page was shown.
const (
	WhereMac   = "mac"
	WherePhone = "phone"
	WhereBoth  = "both"
)

var (
	ErrNotFound       = errors.New("page not found")
	ErrInvalidSession = errors.New("invalid session id")
	ErrInvalidPage    = errors.New("invalid page id")
)

// Page is one `agent-deck open` of a session.
type Page struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Name      string    `json:"name"`
	Path      string    `json:"path,omitempty"`
	URL       string    `json:"url,omitempty"`
	OpenedAt  time.Time `json:"opened_at"`
	EventID   string    `json:"event_id,omitempty"`
}

// idPattern bounds both session and page ids: they become file names and
// URL path segments.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidSessionID reports whether id can name a session file.
func ValidSessionID(id string) bool { return idPattern.MatchString(id) && !strings.Contains(id, "..") }

// ValidPageID reports whether id is a page id as NewID makes them.
func ValidPageID(id string) bool { return pageIDPattern.MatchString(id) }

var pageIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// NewID returns a random 16-hex page id.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Dir is "<data dir>/pages/<profile>". It is not created.
func Dir(profile string) (string, error) {
	if profile == "" {
		profile = "default"
	}
	if !filepath.IsLocal(profile) || filepath.Base(profile) != profile {
		return "", fmt.Errorf("openpages: invalid profile %q", profile)
	}
	root, err := agentpaths.EffectiveDataPath("pages", "pages")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, profile), nil
}

// KindOf names the kind of a local file by its extension.
func KindOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm", ".xhtml":
		return KindHTML
	case ".md", ".markdown", ".mdown", ".mkd":
		return KindMarkdown
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp":
		return KindImage
	}
	return KindText
}

var mu sync.Mutex

// Record appends p to its session's list (newest last), dropping the oldest
// beyond MaxPerSession. p.ID, p.Name and p.OpenedAt are filled when empty.
func Record(profile string, p Page) (Page, error) {
	if !ValidSessionID(p.SessionID) {
		return p, ErrInvalidSession
	}
	if p.OpenedAt.IsZero() {
		p.OpenedAt = time.Now().UTC()
	}
	if p.Name == "" {
		if p.Path != "" {
			p.Name = filepath.Base(p.Path)
		} else {
			p.Name = p.URL
		}
	}
	if p.Title == "" {
		p.Title = p.Name
	}
	dir, err := Dir(profile)
	if err != nil {
		return p, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return p, fmt.Errorf("creating pages directory: %w", err)
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return p, err
	}
	defer unlock()
	pages, err := readSession(dir, p.SessionID)
	if err != nil {
		return p, err
	}
	// One entry per file or URL: opening it again moves it to the top with
	// the new time and keeps its id, so a viewer showing it can refresh.
	kept := pages[:0]
	for _, old := range pages {
		if old.Path == p.Path && old.URL == p.URL {
			p.ID = old.ID
			continue
		}
		kept = append(kept, old)
	}
	if p.ID == "" {
		p.ID = NewID()
	}
	pages = append(kept, p)
	if len(pages) > MaxPerSession {
		pages = pages[len(pages)-MaxPerSession:]
	}
	data, err := json.MarshalIndent(pages, "", "  ")
	if err != nil {
		return p, err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, p.SessionID+".json"), data, 0o600); err != nil {
		return p, fmt.Errorf("writing pages of session %s: %w", p.SessionID, err)
	}
	return p, nil
}

// List returns a session's pages, newest first. A session with no pages
// returns an empty list and no error.
func List(profile, sessionID string) ([]Page, error) {
	if !ValidSessionID(sessionID) {
		return nil, ErrInvalidSession
	}
	dir, err := Dir(profile)
	if err != nil {
		return nil, err
	}
	pages, err := readSession(dir, sessionID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].OpenedAt.After(pages[j].OpenedAt) })
	return pages, nil
}

// Get returns one page of a session.
func Get(profile, sessionID, pageID string) (Page, error) {
	if !ValidPageID(pageID) {
		return Page{}, ErrInvalidPage
	}
	pages, err := List(profile, sessionID)
	if err != nil {
		return Page{}, err
	}
	for _, p := range pages {
		if p.ID == pageID {
			return p, nil
		}
	}
	return Page{}, ErrNotFound
}

// Last returns the newest page of a session.
func Last(profile, sessionID string) (Page, error) {
	pages, err := List(profile, sessionID)
	if err != nil {
		return Page{}, err
	}
	if len(pages) == 0 {
		return Page{}, ErrNotFound
	}
	return pages[0], nil
}

func readSession(dir, sessionID string) ([]Page, error) {
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Page{}, nil
		}
		return nil, fmt.Errorf("reading pages of session %s: %w", sessionID, err)
	}
	defer f.Close()
	var pages []Page
	if err := json.NewDecoder(f).Decode(&pages); err != nil {
		// A damaged index is not worth failing an open over: start again.
		return []Page{}, nil
	}
	out := pages[:0]
	for _, p := range pages {
		if p.SessionID == sessionID && ValidPageID(p.ID) {
			out = append(out, p)
		}
	}
	return out, nil
}

const lockTimeout = 5 * time.Second

func lockDir(dir string) (func(), error) {
	mu.Lock()
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, fmt.Errorf("opening pages lock: %w", err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			mu.Unlock()
			return nil, fmt.Errorf("locking pages directory: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		mu.Unlock()
	}, nil
}
