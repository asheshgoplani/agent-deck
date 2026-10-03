package web

import (
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Recall annotations (docs/recall.md) on the web read path.
//
// Hints and tags live in state.db's session_hints / session_tags, written by
// `agent-deck session annotate` (or add/launch --hint) — usually by an agent
// or conductor describing its own work. They are not *session.Instance
// fields, so BuildMenuSnapshot cannot see them. Like the hook overlay in
// snapshot_hook_refresh.go they are applied by the GET handlers instead of
// the TUI publish pipeline: publishWebMenuSnapshot sits on the TUI list hot
// path, while handlers already re-read per request (and the SSE stream every
// menuEventsPollInterval), so a fresh annotation reaches the browser within
// one poll without the TUI doing anything.

// annotationLoader returns every instance's annotations for a profile.
type annotationLoader func(profile string) (map[string]*statedb.InstanceAnnotations, error)

// applySnapshotAnnotations fills MenuSession.Hints/Tags in place. A load
// failure leaves the snapshot without annotations rather than failing the
// request: they are decoration, not state the client depends on.
func applySnapshotAnnotations(snapshot *MenuSnapshot, loader annotationLoader) {
	if snapshot == nil || loader == nil {
		return
	}
	byID, err := loader(snapshot.Profile)
	if err != nil {
		logging.ForComponent(logging.CompWeb).Debug("annotations_load_failed",
			slog.String("profile", snapshot.Profile),
			slog.String("error", err.Error()))
		return
	}
	if len(byID) == 0 {
		return
	}
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		if a := byID[item.Session.ID]; a != nil {
			item.Session.Hints = a.Hints
			item.Session.Tags = a.Tags
		}
	}
}

// annotationCacheTTL is how long one bulk read is shared between callers.
// Every SSE menu stream re-applies annotations each menuEventsPollInterval,
// so without it N open tabs would run N identical scans per poll.
const annotationCacheTTL = time.Second

// stateDBAnnotationReader keeps one read-only handle on the profile's
// state.db and reuses it across requests. The handle is query_only, so the
// web server never writes to a database the TUI and CLI also own.
//
// The file is re-stat'ed before each read: a state.db replaced on disk
// (profile restore or migration writing a new file over the path) gets a
// fresh handle instead of the cached one still reading the old file.
type stateDBAnnotationReader struct {
	mu   sync.Mutex
	path string
	db   *statedb.StateDB
	fi   os.FileInfo // state.db as it was when db was opened
	now  func() time.Time

	cached   map[string]*statedb.InstanceAnnotations
	cachedAt time.Time
}

// load returns the bulk annotations for profile. The returned map is shared
// between callers inside the cache window and must not be mutated.
func (r *stateDBAnnotationReader) load(profile string) (map[string]*statedb.InstanceAnnotations, error) {
	path, err := session.GetDBPathForProfile(profile)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	t := now()
	if r.db != nil && r.path == path && r.cached != nil && t.Sub(r.cachedAt) < annotationCacheTTL {
		return r.cached, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		r.closeLocked()
		return nil, err
	}
	if r.db == nil || r.path != path || !os.SameFile(r.fi, fi) || !r.fi.ModTime().Equal(fi.ModTime()) {
		r.closeLocked()
		db, err := statedb.OpenReadOnlyLive(path)
		if err != nil {
			return nil, err
		}
		r.db, r.path, r.fi = db, path, fi
	}
	byID, err := r.db.ListInstanceAnnotations()
	if err != nil {
		return nil, err
	}
	r.cached, r.cachedAt = byID, t
	return byID, nil
}

func (r *stateDBAnnotationReader) closeLocked() {
	if r.db != nil {
		_ = r.db.Close()
	}
	r.db, r.fi, r.cached = nil, nil, nil
}

func (r *stateDBAnnotationReader) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked()
}
