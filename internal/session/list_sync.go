package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// `agent-deck list --json --sync` (wire contract v1) answers a remote poller
// with only what changed since its cursor, instead of every session with
// every field. The server keeps a journal next to the profile's state.db
// (RemoteSyncJournalName) that gives every listed row a content hash and the
// change counter (seq) of its last change, plus tombstones for deleted rows.
// A full pass computes every row, then merges it into the journal under a
// file lock held only for the merge; the reply is built from the merged
// journal and the rows of this pass.
//
// Cursor "<epoch>.<seq>": a delta lists the in-scope rows whose seq is above
// it and, in removed, the tombstones above it plus the rows above it that are
// now out of the requested scope. A cursor from another epoch (the journal was
// lost or reset), below the journal's floor (a tombstone it would need was
// pruned), above its seq, or absent gets a full resync (full=true). A full
// resync is paged with a token "<epoch>.<base>.<after>": base is the journal
// seq when its first page was taken, after the seq of the last row already
// sent, and the final page's cursor is "<epoch>.<base>", so anything that
// changed while the pages were fetched comes in the next delta. A page token
// that no longer fits the journal restarts the resync with a new base: the
// reply then names a base other than the token's, and the caller discards the
// pages it collected.

const (
	// RemoteSyncVersion is the envelope's "sync" value.
	RemoteSyncVersion = 1
	// RemoteSyncFlag is the `list --json` flag that selects the envelope.
	// A binary that predates it rejects it with "flag provided but not
	// defined: -sync" and exit status 1.
	RemoteSyncFlag = "--sync"
	// RemoteSyncJournalName is the journal's file name in the profile
	// directory, beside state.db. It is not a state.db* file, so writing it
	// never moves the stamp the remote agent's change feed watches.
	RemoteSyncJournalName = "list-sync.json"
	// RemoteSyncTombstoneCap is how many deletions the journal remembers.
	// Pruning older ones raises the floor: a cursor below it resyncs in full.
	RemoteSyncTombstoneCap = 4096

	// RemoteSyncScopeActive, RemoteSyncScopeArchived and RemoteSyncScopeAll
	// are the values of --scope: not archived, archived, or both.
	RemoteSyncScopeActive   = "active"
	RemoteSyncScopeArchived = "archived"
	RemoteSyncScopeAll      = "all"

	remoteSyncJournalVersion = 1
	remoteSyncLockWait       = 10 * time.Second
)

var listSyncLog = logging.ForComponent(logging.CompSession)

// RemoteSyncRow is one session in a sync reply: exactly the fields a legacy
// `list --json` decode keeps (RemoteSessionInfo), with the same values, plus
// the instance's sort order so a delta can carry a reorder.
type RemoteSyncRow struct {
	RemoteSessionInfo
	Order int `json:"order"`
}

// RemoteSyncCounts is how many listed sessions the profile has per scope,
// whatever scope was asked for.
type RemoteSyncCounts struct {
	Active   int `json:"active"`
	Archived int `json:"archived"`
}

// RemoteSyncEnvelope is the one-line JSON reply of `list --json --sync`.
//
//   - Full: Sessions is every in-scope row (or one page of them); the caller
//     replaces what it holds for this scope once the last page is in.
//   - Delta (Full false): Sessions are rows to upsert, Removed ids to drop.
//   - More: a delta page; ask again with Cursor for the rest.
//   - Next: a full-resync page token for --page; "" on the last page. While
//     Next is set, Cursor is "".
//   - A focus reply (--focus) is Full false with the caller's cursor echoed:
//     the rows asked for plus the hot set, and in Removed the named ids that
//     are gone or out of scope.
type RemoteSyncEnvelope struct {
	Sync     int              `json:"sync"`
	Epoch    string           `json:"epoch"`
	Cursor   string           `json:"cursor"`
	Full     bool             `json:"full"`
	More     bool             `json:"more"`
	Next     string           `json:"next"`
	Scope    string           `json:"scope"`
	Sessions []RemoteSyncRow  `json:"sessions"`
	Removed  []string         `json:"removed"`
	Counts   RemoteSyncCounts `json:"counts"`
}

// RemoteSyncRequest is a parsed `list --json --sync` request.
type RemoteSyncRequest struct {
	Scope  string
	Cursor string
	Page   string
	// Limit caps the entries (rows plus removed ids) of one reply; 0 is no cap.
	Limit int
	// FocusIDs are the ids --focus named (a focus pass also covers the hot
	// set, which the caller adds to the rows).
	FocusIDs []string
}

// ParseRemoteSyncScope validates a --scope value; "" means all.
func ParseRemoteSyncScope(s string) (string, error) {
	switch s {
	case "", RemoteSyncScopeAll:
		return RemoteSyncScopeAll, nil
	case RemoteSyncScopeActive, RemoteSyncScopeArchived:
		return s, nil
	}
	return "", fmt.Errorf("invalid scope %q (want active, archived or all)", s)
}

// RemoteSyncScopeHas reports whether a row with the given archived flag is
// in scope.
func RemoteSyncScopeHas(scope string, archived bool) bool {
	switch scope {
	case RemoteSyncScopeActive:
		return !archived
	case RemoteSyncScopeArchived:
		return archived
	}
	return true
}

// RemoteSyncJournalPath is the journal beside the profile's state.db.
func RemoteSyncJournalPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), RemoteSyncJournalName)
}

// remoteSyncJournal is the on-disk journal. Rows hold every row of the last
// full pass; Gone the newest RemoteSyncTombstoneCap deletions.
type remoteSyncJournal struct {
	V     int                        `json:"v"`
	Epoch string                     `json:"epoch"`
	Seq   uint64                     `json:"seq"`
	Floor uint64                     `json:"floor"`
	Rows  map[string]remoteSyncEntry `json:"rows"`
	Gone  map[string]uint64          `json:"gone"`
	// dirty: changed since load; fresh: created because none was usable.
	dirty bool
	fresh bool
}

// remoteSyncEntry is one row: content hash, seq of its last change, its
// archived flag, and T, the seq at which its archived flag last flipped (it
// left one scope for the other), 0 when it never did. A row created out of a
// scope was never in it, so T stays 0 for it.
type remoteSyncEntry struct {
	H string `json:"h"`
	S uint64 `json:"s"`
	A bool   `json:"a"`
	T uint64 `json:"t,omitempty"`
}

func newRemoteSyncEpoch() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a clock-based
		// epoch still differs from any earlier one.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

func newRemoteSyncJournal() *remoteSyncJournal {
	return &remoteSyncJournal{
		V:     remoteSyncJournalVersion,
		Epoch: newRemoteSyncEpoch(),
		Rows:  map[string]remoteSyncEntry{},
		Gone:  map[string]uint64{},
		dirty: true,
		fresh: true,
	}
}

// loadRemoteSyncJournal reads the journal; a missing or unusable one comes
// back as a new journal with a new epoch, so every cursor resyncs in full.
func loadRemoteSyncJournal(path string) *remoteSyncJournal {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			listSyncLog.Warn("list_sync_journal_unreadable", slog.String("path", path), slog.String("error", err.Error()))
		}
		return newRemoteSyncJournal()
	}
	var j remoteSyncJournal
	if err := json.Unmarshal(data, &j); err != nil || j.V != remoteSyncJournalVersion || j.Epoch == "" || strings.Contains(j.Epoch, ".") || j.Floor > j.Seq {
		reason := "bad header"
		if err != nil {
			reason = err.Error()
		}
		listSyncLog.Warn("list_sync_journal_corrupt", slog.String("path", path), slog.String("error", reason))
		return newRemoteSyncJournal()
	}
	if j.Rows == nil {
		j.Rows = map[string]remoteSyncEntry{}
	}
	if j.Gone == nil {
		j.Gone = map[string]uint64{}
	}
	return &j
}

func (j *remoteSyncJournal) save(path string) error {
	if !j.dirty {
		return nil
	}
	data, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("encoding list sync journal: %w", err)
	}
	if err := atomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing list sync journal %s: %w", path, err)
	}
	return nil
}

// upsert records row's hash; a new or changed row gets the next seq.
func (j *remoteSyncJournal) upsert(id, hash string, archived bool) {
	e, ok := j.Rows[id]
	if ok && e.H == hash && e.A == archived {
		return
	}
	j.Seq++
	moved := e.T
	if ok && e.A != archived {
		moved = j.Seq
	}
	j.Rows[id] = remoteSyncEntry{H: hash, S: j.Seq, A: archived, T: moved}
	delete(j.Gone, id)
	j.dirty = true
}

// pruneGone keeps the newest limit tombstones and raises the floor to the
// newest one dropped.
func (j *remoteSyncJournal) pruneGone(limit int) {
	if len(j.Gone) <= limit {
		return
	}
	type tomb struct {
		id string
		s  uint64
	}
	all := make([]tomb, 0, len(j.Gone))
	for id, s := range j.Gone {
		all = append(all, tomb{id, s})
	}
	sort.Slice(all, func(a, b int) bool { return all[a].s < all[b].s })
	for _, t := range all[:len(all)-limit] {
		delete(j.Gone, t.id)
		if t.s > j.Floor {
			j.Floor = t.s
		}
	}
	j.dirty = true
}

func remoteSyncRowHash(row *RemoteSyncRow) (string, error) {
	data, err := json.Marshal(row)
	if err != nil {
		return "", fmt.Errorf("encoding sync row %s: %w", row.ID, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]), nil
}

func remoteSyncHashes(rows []RemoteSyncRow) ([]string, error) {
	hashes := make([]string, len(rows))
	for i := range rows {
		h, err := remoteSyncRowHash(&rows[i])
		if err != nil {
			return nil, err
		}
		hashes[i] = h
	}
	return hashes, nil
}

// withRemoteSyncJournal runs fn on the journal under its file lock and saves
// it if fn changed it.
func withRemoteSyncJournal(path string, fn func(*remoteSyncJournal)) error {
	lock, err := AcquireConfigFileLockTimeout(path, remoteSyncLockWait)
	if err != nil {
		return fmt.Errorf("locking list sync journal: %w", err)
	}
	defer lock.Release()
	j := loadRemoteSyncJournal(path)
	fn(j)
	return j.save(path)
}

func formatRemoteSyncCursor(epoch string, seq uint64) string {
	return epoch + "." + strconv.FormatUint(seq, 10)
}

// parseRemoteSyncToken splits "<epoch>.<n>[.<n>...]" with want numbers.
func parseRemoteSyncToken(tok string, want int) (string, []uint64, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != want+1 || parts[0] == "" {
		return "", nil, false
	}
	nums := make([]uint64, want)
	for i, p := range parts[1:] {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return "", nil, false
		}
		nums[i] = n
	}
	return parts[0], nums, true
}

// RemoteSyncAnswer is the full pass: rows are every listed session of the
// profile (each in-scope or not), computed before the call. It merges them
// into the journal at journalPath (new and changed rows get a new seq, rows
// that are gone a tombstone) and answers req with a delta or a full resync.
// Counts is left for the caller.
func RemoteSyncAnswer(journalPath string, rows []RemoteSyncRow, req RemoteSyncRequest) (RemoteSyncEnvelope, error) {
	scope, err := ParseRemoteSyncScope(req.Scope)
	if err != nil {
		return RemoteSyncEnvelope{}, err
	}
	hashes, err := remoteSyncHashes(rows)
	if err != nil {
		return RemoteSyncEnvelope{}, err
	}
	var j remoteSyncJournal
	err = withRemoteSyncJournal(journalPath, func(cur *remoteSyncJournal) {
		seen := make(map[string]bool, len(rows))
		for i := range rows {
			seen[rows[i].ID] = true
			cur.upsert(rows[i].ID, hashes[i], rows[i].Archived)
		}
		gone := make([]string, 0)
		for id := range cur.Rows {
			if !seen[id] {
				gone = append(gone, id)
			}
		}
		sort.Strings(gone)
		for _, id := range gone {
			delete(cur.Rows, id)
			cur.Seq++
			cur.Gone[id] = cur.Seq
			cur.dirty = true
		}
		cur.pruneGone(RemoteSyncTombstoneCap)
		j = *cur
	})
	if err != nil {
		return RemoteSyncEnvelope{}, err
	}
	env := RemoteSyncEnvelope{Sync: RemoteSyncVersion, Epoch: j.Epoch, Scope: scope, Sessions: []RemoteSyncRow{}, Removed: []string{}}
	byID := make(map[string]*RemoteSyncRow, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}

	// Continue a full resync.
	if req.Page != "" {
		if epoch, n, ok := parseRemoteSyncToken(req.Page, 2); ok && epoch == j.Epoch && n[0] >= j.Floor && n[0] <= j.Seq && n[1] <= j.Seq {
			remoteSyncFullPage(&env, &j, byID, scope, n[0], n[1], req.Limit)
			return env, nil
		}
		remoteSyncFullPage(&env, &j, byID, scope, j.Seq, 0, req.Limit)
		return env, nil
	}
	if epoch, n, ok := parseRemoteSyncToken(req.Cursor, 1); ok && epoch == j.Epoch && n[0] >= j.Floor && n[0] <= j.Seq {
		remoteSyncDelta(&env, &j, byID, scope, n[0], req.Limit)
		return env, nil
	}
	remoteSyncFullPage(&env, &j, byID, scope, j.Seq, 0, req.Limit)
	return env, nil
}

// remoteSyncFullPage fills env with the in-scope rows whose seq is above
// after, in seq order, at most limit of them.
func remoteSyncFullPage(env *RemoteSyncEnvelope, j *remoteSyncJournal, byID map[string]*RemoteSyncRow, scope string, base, after uint64, limit int) {
	env.Full = true
	type item struct {
		s   uint64
		row *RemoteSyncRow
	}
	items := make([]item, 0, len(byID))
	for id, row := range byID {
		e := j.Rows[id]
		if e.S > after && RemoteSyncScopeHas(scope, row.Archived) {
			items = append(items, item{e.S, row})
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].s < items[b].s })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
		env.Next = j.Epoch + "." + strconv.FormatUint(base, 10) + "." + strconv.FormatUint(items[len(items)-1].s, 10)
	} else {
		env.Cursor = formatRemoteSyncCursor(j.Epoch, base)
	}
	for _, it := range items {
		env.Sessions = append(env.Sessions, *it.row)
	}
}

// remoteSyncDelta fills env with what changed after cursor, in seq order:
// in-scope rows to upsert, and tombstones and the rows that left the scope
// after cursor to drop. A row that changed while it was out of the scope
// the whole time is not sent: the caller never had it.
func remoteSyncDelta(env *RemoteSyncEnvelope, j *remoteSyncJournal, byID map[string]*RemoteSyncRow, scope string, cursor uint64, limit int) {
	type item struct {
		s       uint64
		row     *RemoteSyncRow
		removed string
	}
	var items []item
	for id, e := range j.Rows {
		if e.S <= cursor {
			continue
		}
		row := byID[id]
		switch {
		case row != nil && RemoteSyncScopeHas(scope, row.Archived):
			items = append(items, item{s: e.S, row: row})
		case e.T > cursor:
			items = append(items, item{s: e.S, removed: id})
		}
	}
	for id, s := range j.Gone {
		if s > cursor {
			items = append(items, item{s: s, removed: id})
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].s < items[b].s })
	env.Cursor = formatRemoteSyncCursor(j.Epoch, j.Seq)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
		env.More = true
		env.Cursor = formatRemoteSyncCursor(j.Epoch, items[len(items)-1].s)
	}
	for _, it := range items {
		if it.row != nil {
			env.Sessions = append(env.Sessions, *it.row)
		} else {
			env.Removed = append(env.Removed, it.removed)
		}
	}
}

// RemoteSyncFocusAnswer is the focus pass: rows are the focused sessions
// only (the ids asked for plus the hot set, already scoped by the caller).
// Their journal entries are updated, nothing else is touched (no tombstones,
// and no journal is created when there is none: the next full pass does
// that), and the reply echoes the caller's cursor. Ids named in req.FocusIDs
// that are not among rows come back in Removed. Counts is left for the
// caller.
func RemoteSyncFocusAnswer(journalPath string, rows []RemoteSyncRow, req RemoteSyncRequest) (RemoteSyncEnvelope, error) {
	scope, err := ParseRemoteSyncScope(req.Scope)
	if err != nil {
		return RemoteSyncEnvelope{}, err
	}
	hashes, err := remoteSyncHashes(rows)
	if err != nil {
		return RemoteSyncEnvelope{}, err
	}
	epoch := ""
	err = withRemoteSyncJournal(journalPath, func(j *remoteSyncJournal) {
		if j.fresh {
			j.dirty = false
			return
		}
		epoch = j.Epoch
		for i := range rows {
			j.upsert(rows[i].ID, hashes[i], rows[i].Archived)
		}
	})
	if err != nil {
		return RemoteSyncEnvelope{}, err
	}
	env := RemoteSyncEnvelope{Sync: RemoteSyncVersion, Epoch: epoch, Cursor: req.Cursor, Scope: scope, Sessions: []RemoteSyncRow{}, Removed: []string{}}
	have := make(map[string]bool, len(rows))
	for _, r := range rows {
		have[r.ID] = true
		env.Sessions = append(env.Sessions, r)
	}
	for _, id := range req.FocusIDs {
		if !have[id] {
			have[id] = true
			env.Removed = append(env.Removed, id)
		}
	}
	return env, nil
}
