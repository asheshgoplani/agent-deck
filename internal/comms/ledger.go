package comms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

const (
	ledgerDirName = "comms"
	// DefaultRetentionDays bounds how long sealed ledger segments are kept.
	DefaultRetentionDays = 90
	// retainSegments is generous on purpose: with 8 MiB segments and ~100
	// KB/h for a busy child, the age bound (not the count) is what prunes.
	retainSegments = 1024
	// recentKeys bounds the in-memory idempotency window per writer. A
	// producer re-observes a turn within seconds (hook re-fires, polls),
	// not thousands of records later.
	recentKeys = 4096
	// DefaultMaxBytes bounds one profile's ledger on disk (sealed segments
	// plus the active file). Past it Commit returns events.ErrQuota: the
	// spool keeps the entries (bounded by its own cap), the daemon logs the
	// overload, and nothing is silently dropped.
	DefaultMaxBytes = 2 << 30
	// warmTimeout bounds the dedup rebuild at open. The rebuild reads up to
	// the newest parseable frame (a malformed last line spent a cursor no
	// frame carries), so it ends as soon as that frame is read; the bound
	// only matters for a log the reader cannot read at all, and then Open
	// fails visibly instead of starting with a partial window or holding
	// the daemon's poll loop.
	warmTimeout = 10 * time.Second
	// scanTimeout bounds one ReadAfter / Export pass the same way.
	scanTimeout = 10 * time.Second
)

// Dir returns "<data>/comms/<profile>", the ledger directory for a profile.
// The profile is validated as a single local path element.
func Dir(profile string) (string, error) {
	if profile == "" {
		profile = "default"
	}
	if !filepath.IsLocal(profile) || filepath.Base(profile) != profile {
		return "", fmt.Errorf("comms: invalid profile %q", profile)
	}
	root, err := agentpaths.EffectiveDataPath(ledgerDirName, ledgerDirName)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, profile), nil
}

// Ledger is the writer handle the notify daemon holds: one per profile. It
// wraps the events bus with the record schema, the idempotency window and
// the per-From sequence.
type Ledger struct {
	profile string
	bus     *events.Bus

	mu     sync.Mutex
	keys   map[string]Record // dedup key -> the record committed under it (the stored receipt)
	order  []string
	seq    map[string]int64
	last   map[string]Record // newest turn record per From (the tier rule's "previous turn")
	status map[string]Record // newest status record per From
	store  StoreIdentity
	closed bool
}

// StoreIdentity names one ledger across host renames and restores: a
// random id minted when the directory is first written and an epoch that a
// reset or a restore from backup bumps, so a stale cursor from another
// epoch is recognisable as such instead of being mistaken for progress.
// Kept in <ledger>/store.json.
type StoreIdentity struct {
	ID      string `json:"id"`
	Epoch   int64  `json:"epoch"`
	Created int64  `json:"created"` // Unix ms
	// HWM is the highest cursor this ledger has held, persisted at close and
	// every hwmEvery commits. A ledger that opens with a cursor below it was
	// restored from an older copy: the epoch is bumped so consumer states
	// from before the restore are recognised as stale.
	HWM uint64 `json:"hwm,omitempty"`
}

const hwmEvery = 256

const storeFileName = "store.json"

// loadOrCreateStoreIdentity reads store.json, creating it on a fresh
// ledger. A ledger directory with history but no store.json (never written
// by this version) gets epoch 1.
func loadOrCreateStoreIdentity(dir string) (StoreIdentity, error) {
	path := filepath.Join(dir, storeFileName)
	data, err := os.ReadFile(path) // #nosec G304 -- ledger dir under the data dir
	if err == nil {
		var id StoreIdentity
		if json.Unmarshal(data, &id) == nil && id.ID != "" && id.Epoch > 0 {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return StoreIdentity{}, err
	}
	now := time.Now()
	id := StoreIdentity{ID: NewID(now), Epoch: 1, Created: now.UnixMilli()}
	return id, writeStoreIdentity(dir, id)
}

// writeStoreIdentity persists store.json durably (tmp, fsync, rename, dir sync).
func writeStoreIdentity(dir string, id StoreIdentity) error {
	path := filepath.Join(dir, storeFileName)
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Store returns the ledger's identity.
func (l *Ledger) Store() StoreIdentity {
	if l == nil {
		return StoreIdentity{}
	}
	return l.store
}

// Open opens (creating) the profile's ledger for writing. Only the daemon
// calls this. Open scans the retained tail once so idempotency and per-From
// sequences survive a daemon restart.
func Open(profile string) (*Ledger, error) {
	dir, err := Dir(profile)
	if err != nil {
		return nil, err
	}
	return OpenDir(profile, dir)
}

// OpenDir is Open at an explicit directory. The directory is created owner
// only: it holds assistant text.
func OpenDir(profile, dir string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	bus, err := events.OpenAt(dir, events.Options{RetentionDays: DefaultRetentionDays, RetainSegments: retainSegments,
		Private: true, KeepCorrupt: true, MaxBytes: DefaultMaxBytes})
	if err != nil {
		return nil, err
	}
	store, err := loadOrCreateStoreIdentity(dir)
	if err != nil {
		_ = bus.Close()
		return nil, err
	}
	if cursor := uint64(bus.Cursor()); cursor < store.HWM {
		// The log is shorter than this store has been: restored from an
		// older copy. New epoch, so stale consumer states are rejected.
		store.Epoch++
		store.HWM = cursor
		if err := writeStoreIdentity(dir, store); err != nil {
			_ = bus.Close()
			return nil, err
		}
		slog.Warn("comms_store_restored", "dir", dir, "epoch", store.Epoch, "cursor", cursor)
	}
	l := &Ledger{profile: profile, bus: bus, keys: map[string]Record{}, seq: map[string]int64{},
		last: map[string]Record{}, status: map[string]Record{}, store: store}
	if err := l.warm(); err != nil {
		_ = bus.Close()
		return nil, err
	}
	return l, nil
}

// ErrTailUnreadable is returned by OpenDir when the dedup rebuild could not
// reach the ledger's newest frame within warmTimeout: the log needs repair
// and the daemon must not write with a partial idempotency window.
var ErrTailUnreadable = errors.New("comms: ledger tail unreadable; dedup window could not be rebuilt")

// warm reloads the idempotency window and sequences from the newest frames.
// Bounded: it stops at the newest parseable frame (cursors after it were
// spent by malformed lines or rolled-back commits and carry nothing) or at
// warmTimeout, and the second outcome is an error.
func (l *Ledger) warm() error {
	_, last, err := l.bus.Ends()
	if err != nil {
		return err
	}
	if last == 0 {
		return nil
	}
	after := events.Cursor(0)
	if uint64(last) > recentKeys {
		after = last - recentKeys
	}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
		sub, err := l.bus.Subscribe(ctx, after)
		if err != nil {
			cancel()
			return err
		}
		reached := events.Cursor(0)
		for f := range sub.Frames() {
			var r Record
			if json.Unmarshal(f.Data, &r) == nil {
				l.remember(r)
			}
			reached = f.Cursor
			if f.Cursor >= last {
				break
			}
		}
		cancel()
		if reached >= last {
			return nil
		}
		if errors.Is(sub.Err(), events.ErrCursorTooOld) && after != 0 {
			// Compaction removed the start of the window: warm from the
			// oldest retained frame instead of restoring nothing.
			after = 0
			continue
		}
		return ErrTailUnreadable
	}
	return ErrTailUnreadable
}

func (l *Ledger) remember(r Record) {
	if key := r.DedupKey(); key != "" {
		if _, dup := l.keys[key]; !dup {
			l.keys[key] = r
			l.order = append(l.order, key)
			if len(l.order) > recentKeys {
				delete(l.keys, l.order[0])
				l.order = l.order[1:]
			}
		}
	}
	if r.Seq > l.seq[r.From] {
		l.seq[r.From] = r.Seq
	}
	switch r.Kind {
	case KindTurn:
		l.last[r.From] = r
	case KindStatus:
		l.status[r.From] = r
	}
}

// LastStatus returns the newest status record committed for from, if any
// is within the warm window.
func (l *Ledger) LastStatus(from string) (Record, bool) {
	if l == nil {
		return Record{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.status[from]
	return r, ok
}

// LastTurn returns the newest turn record committed for from, if any is
// within the warm window. The daemon tiers a new turn against it.
func (l *Ledger) LastTurn(from string) (Record, bool) {
	if l == nil {
		return Record{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.last[from]
	return r, ok
}

// ErrDuplicate is returned by Commit when the record's key was committed
// within the idempotency window with the same content. The record returned
// with it is the one already committed (the stored receipt), so a retried
// send with the same request id gets its receipt back and never a second
// delivery.
var ErrDuplicate = errors.New("comms: duplicate key")

// ErrConflict is returned by Commit when the record's key was committed
// within the window with DIFFERENT content: a producer reused an identity
// for something else. The stored record is returned with it; the caller
// keeps its input aside (the spool's conflict directory) and logs it.
var ErrConflict = errors.New("comms: same key, different content")

// Lookup returns the record committed under key within the window.
func (l *Ledger) Lookup(key string) (Record, bool) {
	if l == nil {
		return Record{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.keys[key]
	return r, ok
}

// Commit stamps the record (id, t_record, hash, bytes, latency, seq) and
// appends it synchronously. A record whose Key was already committed within
// the window returns ErrDuplicate and is not appended. The committed record
// (with the cursor it was assigned) is returned.
//
// Seq counts a From's records as this writer has seen them: it is restored
// from the warm window (the newest recentKeys frames) at open, so it is
// monotonic across restarts for any From active in that window and restarts
// at 1 for a From silent for longer. Ordering is the bus cursor and the id;
// seq is a per-sender counter for readers, not an identity.
func (l *Ledger) Commit(r Record) (Record, events.Cursor, error) {
	if l == nil || l.bus == nil {
		return r, 0, errors.New("comms: ledger not open")
	}
	if r.Kind == "" || r.From == "" {
		return r, 0, errors.New("comms: record needs kind and from")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return r, 0, errors.New("comms: ledger closed")
	}
	if key := r.DedupKey(); key != "" {
		if stored, dup := l.keys[key]; dup {
			if stored.ContentHash() != r.ContentHash() {
				return stored, 0, ErrConflict
			}
			return stored, 0, ErrDuplicate
		}
	}
	if r.Profile == "" {
		r.Profile = l.profile
	}
	if r.ToProfile == "" {
		r.ToProfile = l.profile
	}
	r.ToStore = l.store.ID
	if r.Host == "" {
		r.Host = localHost()
	}
	if r.Store == "" {
		// First commit anywhere: this ledger is the record's store of
		// origin. An imported record keeps the origin's store and epoch.
		r.Store, r.Epoch = l.store.ID, l.store.Epoch
	}
	if r.Seq == 0 && r.Origin == "" {
		r.Seq = l.seq[r.From] + 1 // an imported record keeps the origin's sequence
	}
	r.Stamp(time.Now())
	f, err := l.bus.Commit(r.Kind, r.From, r)
	if err != nil {
		return r, 0, err
	}
	l.remember(r)
	if uint64(f.Cursor)%hwmEvery == 0 {
		l.persistHWM(uint64(f.Cursor))
	}
	return r, f.Cursor, nil
}

// persistHWM records the high-water cursor in store.json (best effort; a
// failure only delays restore detection to the next mark).
func (l *Ledger) persistHWM(cursor uint64) {
	if cursor <= l.store.HWM {
		return
	}
	l.store.HWM = cursor
	if dir := l.bus.Stats().Dir; dir != "" {
		_ = writeStoreIdentity(dir, l.store)
	}
}

// Cursor returns the ledger's last committed cursor.
func (l *Ledger) Cursor() events.Cursor {
	if l == nil || l.bus == nil {
		return 0
	}
	return l.bus.Cursor()
}

// Close releases the writer.
func (l *Ledger) Close() error {
	if l == nil || l.bus == nil {
		return nil
	}
	l.mu.Lock()
	l.closed = true
	l.persistHWM(uint64(l.bus.Cursor()))
	l.mu.Unlock()
	return l.bus.Close()
}

// OpenReader opens the profile's ledger read-only (followers, `msg`,
// `events follow --bus comms`). ErrNoLedger when nothing was written yet.
func OpenReader(profile string) (*events.Bus, error) {
	dir, err := Dir(profile)
	if err != nil {
		return nil, err
	}
	return OpenReaderDir(dir)
}

// ErrNoLedger means the profile has no ledger directory: the daemon never
// wrote one (is [comms] ledger on?).
var ErrNoLedger = errors.New("comms: no ledger for this profile yet (is [comms] ledger = true and the notify daemon running?)")

// OpenReaderDir is OpenReader at an explicit directory.
func OpenReaderDir(dir string) (*events.Bus, error) {
	// KeepCorrupt as the writer: a malformed line is skipped (its cursor
	// spent) instead of hiding the rest of the ledger, and the active file
	// is read under the writer lock so only committed frames are seen.
	bus, err := events.OpenAt(dir, events.Options{ReadOnly: true, KeepCorrupt: true})
	if errors.Is(err, events.ErrNoBus) {
		return nil, ErrNoLedger
	}
	return bus, err
}

// Decode parses a ledger frame back into its record.
func Decode(f events.Frame) (Record, error) {
	var r Record
	if len(f.Data) == 0 {
		return r, errors.New("comms: frame has no data")
	}
	err := json.Unmarshal(f.Data, &r)
	return r, err
}

// ReadAfter returns every record with cursor > after, oldest first, plus
// the last cursor read. It stops at the end of the retained log (it does not
// follow); a pass that reaches the newest frame returns the ledger's cursor,
// past any spent cursor after it. ErrCursorTooOld surfaces unchanged so a
// consumer can reset.
func ReadAfter(bus *events.Bus, after events.Cursor, limit int) ([]Record, events.Cursor, error) {
	var out []Record
	last, err := scan(bus, after, limit, func(_ events.Cursor, r Record) {
		out = append(out, r)
	})
	return out, last, err
}

// Exported is one record with its cursor on the ledger it was read from:
// the unit `msg export` ships to another host and Import commits.
type Exported struct {
	Cursor events.Cursor `json:"cursor"`
	Record Record        `json:"record"`
}

// Export returns records with cursor > after as Exported pairs, oldest
// first, bounded by limit (0 = all retained), and the last cursor the pass
// covered (as ReadAfter). It is ReadAfter with the cursors kept, for the
// remote path (the puller advances its cursor for this origin only to a
// cursor it committed) and for ConsumerState.SkipSpent.
func Export(bus *events.Bus, after events.Cursor, limit int) ([]Exported, events.Cursor, error) {
	var out []Exported
	last, err := scan(bus, after, limit, func(c events.Cursor, r Record) {
		out = append(out, Exported{Cursor: c, Record: r})
	})
	return out, last, err
}

// scan hands every decodable record with cursor > after to visit, oldest
// first, until the end of the retained log or limit records (0 = no limit).
// It returns the last cursor the pass covered: the last frame read when the
// limit stopped it, the ledger cursor when it reached the newest frame
// (cursors after that frame are spent and carry nothing), after when there
// was nothing past it. A frame that does not decode is skipped but still
// advances the cursor. Every record in (after, returned cursor] was handed
// to visit.
func scan(bus *events.Bus, after events.Cursor, limit int, visit func(events.Cursor, Record)) (events.Cursor, error) {
	if bus == nil {
		return after, errors.New("comms: no ledger")
	}
	end, lastFrame, err := bus.Ends()
	if err != nil {
		return after, err
	}
	if lastFrame <= after {
		return max(after, end), nil
	}
	// Bounded: a log the reader cannot read ends the pass with
	// ErrTailUnreadable instead of waiting forever.
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()
	sub, err := bus.Subscribe(ctx, after)
	if err != nil {
		return after, err
	}
	last := after
	n := 0
	done := false
	for f := range sub.Frames() {
		if r, err := Decode(f); err == nil {
			visit(f.Cursor, r)
			n++
		}
		last = f.Cursor
		if f.Cursor >= lastFrame {
			done = true
			last = max(last, end)
			break
		}
		if limit > 0 && n >= limit {
			done = true
			break // frames already buffered past the limit are not ours to take
		}
	}
	if err := sub.Err(); err != nil {
		return last, err
	}
	if !done {
		return last, ErrTailUnreadable
	}
	return last, nil
}

// Import commits records exported from another host's ledger under the
// given origin (the configured remote name). Each record keeps its id, key,
// host and timestamps; Origin and SrcCursor are stamped here. Idempotent:
// a record already committed for this origin is skipped. It returns the
// highest source cursor that is now durable locally (every exported record
// up to it was committed or was a duplicate), which is what the puller
// stores as its cursor for this origin; on an error the cursor stops just
// before the failed record so the next pull retries from it.
func (l *Ledger) Import(origin string, exported []Exported) (events.Cursor, error) {
	if strings.TrimSpace(origin) == "" {
		return 0, errors.New("comms: import needs an origin")
	}
	var done events.Cursor
	for _, e := range exported {
		r := e.Record
		if r.Store != "" && r.Store == l.store.ID {
			// Our own record coming back through another host: an echo,
			// not news. Treated as durable so the puller's cursor moves on.
			done = e.Cursor
			continue
		}
		if r.Origin == "" {
			// A record that was itself imported on the origin keeps its first
			// origin; the hop is not the source.
			r.Origin = origin
			r.SrcCursor = uint64(e.Cursor)
		}
		if r.Key == "" {
			// A keyless record still needs a stable identity across pulls.
			r.Key = Key(r.Kind, r.From, r.ID)
		}
		if _, _, err := l.Commit(r); err != nil && !errors.Is(err, ErrDuplicate) {
			return done, err
		}
		done = e.Cursor
	}
	return done, nil
}

// localHost is the short hostname stamped on locally produced records.
func localHost() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

// Exists reports whether the profile has a ledger directory.
func Exists(profile string) bool {
	dir, err := Dir(profile)
	if err != nil {
		return false
	}
	_, err = os.Stat(dir)
	return err == nil
}
