package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Incremental remote talkback (issue #2469 family, PR3). The #1948 drain
// re-shipped a remote's whole completion ledger and every inbox on every run
// and never woke the conductor. A conductor now keeps one cursor per
// (remote, conductor): the newest turn-journal seq it holds per remote child,
// the completion-ledger entry it holds per child and its read position in the
// remote's _unowned ledger. The remote answers only what is newer, and the
// cursor advances only once every record of the batch durably landed
// (inserted or already present), so a failed write refetches the batch and
// the inbox dedup absorbs the overlap.
//
// The cursor stays bounded: it names only children active within
// remoteTalkbackHorizon whose journal still exists (the rm sweep removes a
// removed session's journal), and it travels on the export's stdin
// (`--after -`), never as one argv string.

// RemoteCursor is the drain position against one remote. On the wire it is
// one flat JSON object:
//
//	{"<child_id>": <seq>, "_ts": "<RFC3339>",
//	 "_ledger": {"<child_id>": "<mark>"}, "_unowned": {"n": 12, "last": "<mark>"}}
//
// The parts:
//
//   - Seqs: per child, the newest turn-journal seq already received.
//   - Ledger: per child, a mark of the completion-ledger entry already
//     received (its FinishedAt plus a hash of its status and summary). The
//     ledger is one last-wins file per child, so "differs from what I hold" is
//     exact, a rewrite with the same stamp but a new outcome still crosses,
//     and it does not depend on producers stamping records in write order
//     (they do not: a completion is stamped with the hook's UpdatedAt, a
//     worker writes from another process, clocks step).
//   - Unowned: how many _unowned records were already examined, plus a mark of
//     the last one. The file is append-only, so the records past N are new; a
//     rewrite (operator purge) breaks the mark and the export starts over.
//   - TS: the newest ledger / unowned stamp received. Informational only; no
//     filter depends on it.
//   - Legacy marks a cursor saved after a drain of a remote that predates
//     --after: it carries no position, it only keeps the conductor enrolled for
//     scheduled talkback.
//
// Unknown "_"-prefixed keys are ignored, so a later producer can add metadata.
type RemoteCursor struct {
	Seqs    map[string]int64
	TS      time.Time
	Ledger  map[string]string
	Unowned RemoteUnownedMark
	Legacy  bool
	// Comms is the position on the remote's Comms Ledger (`_comms`, P3):
	// set only by a puller whose own ledger is on; a remote that does not
	// know the key ignores it, and one that does answers Export.Comms.
	Comms *RemoteCommsCursor
}

// RemoteUnownedMark is the _unowned position: N records examined, Last the
// mark of record N-1.
type RemoteUnownedMark struct {
	N    int    `json:"n"`
	Last string `json:"last,omitempty"`
}

const (
	remoteCursorTSKey      = "_ts"
	remoteCursorLedgerKey  = "_ledger"
	remoteCursorUnownedKey = "_unowned"
	remoteCursorLegacyKey  = "_legacy"
	remoteCursorCommsKey   = "_comms"
)

// MarshalJSON writes the flat wire form.
func (c RemoteCursor) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(c.Seqs)+4)
	for k, v := range c.Seqs {
		m[k] = v
	}
	if !c.TS.IsZero() {
		m[remoteCursorTSKey] = c.TS.UTC().Format(time.RFC3339Nano)
	}
	if len(c.Ledger) > 0 {
		m[remoteCursorLedgerKey] = c.Ledger
	}
	if c.Unowned.N > 0 {
		m[remoteCursorUnownedKey] = c.Unowned
	}
	if c.Legacy {
		m[remoteCursorLegacyKey] = true
	}
	if c.Comms != nil {
		m[remoteCursorCommsKey] = c.Comms
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads the flat wire form.
func (c *RemoteCursor) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := emptyRemoteCursor()
	for k, v := range raw {
		switch k {
		case remoteCursorTSKey:
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return fmt.Errorf("cursor %s: %w", k, err)
			}
			if s != "" {
				ts, err := time.Parse(time.RFC3339Nano, s)
				if err != nil {
					return fmt.Errorf("cursor %s: %w", k, err)
				}
				out.TS = ts
			}
		case remoteCursorLedgerKey:
			var m map[string]string
			if err := json.Unmarshal(v, &m); err != nil {
				return fmt.Errorf("cursor %s: %w", k, err)
			}
			out.Ledger = m
		case remoteCursorUnownedKey:
			if err := json.Unmarshal(v, &out.Unowned); err != nil {
				return fmt.Errorf("cursor %s: %w", k, err)
			}
		case remoteCursorLegacyKey:
			_ = json.Unmarshal(v, &out.Legacy)
		case remoteCursorCommsKey:
			var cc RemoteCommsCursor
			if err := json.Unmarshal(v, &cc); err == nil {
				out.Comms = &cc
			}
		default:
			var seq int64
			if err := json.Unmarshal(v, &seq); err != nil {
				if strings.HasPrefix(k, "_") {
					continue
				}
				return fmt.Errorf("cursor seq for %q: %w", k, err)
			}
			out.Seqs[k] = seq
		}
	}
	*c = out
	return nil
}

// emptyRemoteCursor is the "from scratch" position, with a non-nil Seqs map.
func emptyRemoteCursor() RemoteCursor {
	return RemoteCursor{Seqs: map[string]int64{}}
}

// ParseRemoteCursor parses the --after argument (or the stdin it names).
// Empty means "from scratch".
func ParseRemoteCursor(s string) (RemoteCursor, error) {
	if strings.TrimSpace(s) == "" {
		return emptyRemoteCursor(), nil
	}
	var c RemoteCursor
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return RemoteCursor{}, fmt.Errorf("invalid cursor: %w", err)
	}
	return c, nil
}

// RemoteExport is the `inbox export --json --after` reply. Writer is set when
// the caller asked for --with-writer, so export and liveness travel in one
// ssh round trip.
type RemoteExport struct {
	Records    []TransitionNotificationEvent `json:"records"`
	CursorNext RemoteCursor                  `json:"cursor_next"`
	Writer     *WriterStatus                 `json:"writer,omitempty"`
	// Comms answers the cursor's `_comms` position (P3): this host's ledger
	// records for sessions it does not host. Absent when not asked.
	Comms *RemoteCommsExport `json:"comms,omitempty"`
}

// remoteExportNewChildLines bounds what a child unknown to the cursor ships
// on its first drain.
const remoteExportNewChildLines = 64

// remoteTalkbackHorizon is how far back the incremental export looks. It is
// the receiver's consumed-turn horizon: WriteInboxEventIfUnseen answers
// AlreadyPresent for any record stamped before it, so shipping or tracking an
// older one could never deliver anything. A child (journal) or ledger entry
// untouched for that long drops out of the export and out of the cursor, so
// the cursor is bounded by recent activity, not by history.
const remoteTalkbackHorizon = consumedTurnsTTL

// exportJournal is one child's retained turn journal, as the export read it.
type exportJournal struct {
	child   string
	profile string
	lines   []TurnJournalEntry
}

// readExportJournals reads every turn journal written at or after since.
func readExportJournals(since time.Time) ([]exportJournal, error) {
	dir := TurnJournalDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var out []exportJournal
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(since) {
			continue // idle past the horizon: nothing in it can still land
		}
		turnJournalMu.Lock()
		lines, err := readTurnJournalLocked(filepath.Join(dir, e.Name()))
		turnJournalMu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("export: unreadable turn journal %s: %w", e.Name(), err)
		}
		if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1].Child) == "" {
			continue
		}
		tail := lines[len(lines)-1]
		out = append(out, exportJournal{child: tail.Child, profile: tail.Profile, lines: lines})
	}
	return out, nil
}

// ExportRecordsAfter is the incremental, read-only remote export:
//
//   - turn-journal lines newer than the cursor's seq for each child (a child
//     the cursor does not know, or whose journal restarted below the cursor,
//     ships its last 64 lines);
//   - completion-ledger entries that differ from the entry the cursor holds
//     for that child (skipped when a journal line of this batch already
//     carries the same completion);
//   - _unowned records appended since the cursor's position, except a
//     journaled turn the journal already delivers (same seq, same transcript
//     turn, same stale flag). A turn the journal no longer holds (trimmed
//     past the cursor, or a failed journal append) ships from here, and so
//     does the producer's own record of a turn the render could not judge.
//
// A journal line is a record only when the producer committed one: a repeat
// its notifier dropped as a duplicate (see renderJournalTurns) still holds a
// journal line but never ships.
//
// Only activity within remoteTalkbackHorizon is read or named in the next
// cursor; a removed session's journal goes with it (rm sweep), so the cursor
// tracks the live fleet, not history. Other parents' inboxes are never
// exported here: the drain's --into parent decides where records land. The
// no_notify opt-out filter applies as in ExportPendingRecords, and a top-level
// conductor's own journal never ships: the producer drops its turns on
// purpose (self_conductor), so the legacy export never carries them either.
// Nor does a child whose parent is in this host's registry: it belongs to that
// parent (whose own inbox already holds its records), so none of its journal
// lines, ledger completions or _unowned records cross to the --into conductor.
// Only children whose parent this host cannot resolve (the cross-host
// conductor) and orphans cross. Both skipped journals still enter the cursor,
// so a later reparent to the cross-host conductor ships only new turns.
func ExportRecordsAfter(cursor RemoteCursor) (RemoteExport, error) {
	horizon := time.Now().Add(-remoteTalkbackHorizon)
	next := RemoteCursor{Seqs: map[string]int64{}, TS: cursor.TS}

	journals, err := readExportJournals(horizon)
	if err != nil {
		return RemoteExport{}, err
	}
	ledger, err := exportLedgerRecordsSince(horizon)
	if err != nil {
		return RemoteExport{}, err
	}
	unowned, err := ReadInboxEvents(UnownedInboxID)
	if err != nil {
		return RemoteExport{}, fmt.Errorf("export: unreadable inbox %s: %w", UnownedInboxID, err)
	}
	// The _unowned records this batch considers: appended since the cursor's
	// position and inside the horizon.
	start := 0
	if n := cursor.Unowned.N; n > 0 && n <= len(unowned) && unownedMark(unowned[n-1]) == cursor.Unowned.Last {
		start = n
	}
	var pendingUnowned []TransitionNotificationEvent
	for _, ev := range unowned[start:] {
		if !ev.Timestamp.Before(horizon) { // older: the receiver would only answer AlreadyPresent
			pendingUnowned = append(pendingUnowned, ev)
		}
	}
	// Only the profiles these records name are opened, so an old record of a
	// deleted profile never recreates its store.
	profiles := map[string]struct{}{}
	for _, j := range journals {
		profiles[j.profile] = struct{}{}
	}
	for _, ev := range ledger {
		profiles[ev.Profile] = struct{}{}
	}
	for _, ev := range pendingUnowned {
		profiles[ev.Profile] = struct{}{}
	}
	reg, err := loadExportRegistry(profiles)
	if err != nil {
		return RemoteExport{}, err
	}
	// The producer's committed transitions per child, in commit order: they
	// seed the duplicate walk at a journal's first retained line.
	committed := map[string][]TransitionNotificationEvent{}
	for _, ev := range unowned {
		if ev.Kind != transitionKindFinished {
			child := strings.TrimSpace(ev.ChildSessionID)
			committed[child] = append(committed[child], ev)
		}
	}
	noteTS := func(ts time.Time) {
		if ts.After(next.TS) {
			next.TS = ts
		}
	}
	// delivered[child] holds the turn identity of every journal line this
	// cursor has received or this batch ships, by seq: an _unowned copy of
	// that same record is redundant, any other one is news (a different turn,
	// or a stale repeat the render judged differently from the producer).
	// journalDone holds the completions those lines carry, so their ledger
	// mirror stays home.
	delivered := map[string]map[int64]string{}
	journalDone := map[string]bool{}
	var out []TransitionNotificationEvent
	for _, j := range journals {
		child, lines := j.child, j.lines
		last := lines[len(lines)-1].Seq
		next.Seqs[child] = last
		if key := exportRegistryKey(j.profile, child); reg.selfConductor[key] || reg.localParent[key] {
			// self_conductor: the producer committed none of these. A child of
			// a parent on this host: they are that parent's turns.
			continue
		}
		since, known := cursor.Seqs[child]
		rendered, dropped := renderJournalTurns(lines, lastCommittedBefore(committed[child], lines[0].TS))
		fresh := !known || last < since
		start := 0
		if fresh {
			// Unknown child, or a journal that was removed and recreated (its
			// seqs restarted): ship the recent tail, dedup absorbs repeats.
			start = max(0, len(lines)-remoteExportNewChildLines)
			since = 0
		} else if first := lines[0].Seq; first > since+1 {
			// Trimmed past the cursor: the missing turns cross from _unowned.
			commsLog.Warn("remote_export_journal_gap", "child", child,
				"cursor_seq", since, "first_retained_seq", first)
		}
		seen := map[int64]string{}
		for i := start; i < len(lines); i++ {
			l := lines[i]
			if dropped[i] {
				continue // the producer's notifier dropped it: never a record
			}
			if l.Seq > since {
				if fresh && l.TS.Before(horizon) {
					continue // the receiver would only answer AlreadyPresent
				}
				out = append(out, rendered[i])
			}
			seen[l.Seq] = journalTurnKey(rendered[i])
			if rendered[i].Kind == transitionKindFinished {
				journalDone[completionKey(rendered[i])] = true
			}
		}
		delivered[child] = seen
	}

	if len(ledger) > 0 {
		next.Ledger = make(map[string]string, len(ledger))
	}
	for _, ev := range ledger {
		child := ev.ChildSessionID
		mark := ledgerMark(ev)
		next.Ledger[child] = mark
		if held, ok := cursor.Ledger[child]; ok && held == mark {
			continue // this entry already crossed
		}
		noteTS(ev.Timestamp)
		// A journaled child's completion rides its journal line, which carries
		// the turn signal its ledger mirror lacks; the ledger copy still ships
		// when it is the only record of it, e.g. a run-task exit without a
		// sentinel.
		if !journalDone[completionKey(ev)] {
			out = append(out, ev)
		}
	}
	if len(unowned) > 0 {
		next.Unowned = RemoteUnownedMark{N: len(unowned), Last: unownedMark(unowned[len(unowned)-1])}
	}
	for _, ev := range pendingUnowned {
		noteTS(ev.Timestamp)
		// The journal delivers its own turns. Everything else crosses from
		// here: a flip the producer could not classify (Seq 0), a turn the
		// journal trimmed or never got (failed append), and a record that
		// differs from the journal line of the same seq (another turn, or a
		// stale repeat the render could not see as one).
		if key, ok := delivered[strings.TrimSpace(ev.ChildSessionID)][ev.Seq]; ok && ev.Seq > 0 &&
			key == journalTurnKey(ev) {
			continue
		}
		out = append(out, ev)
	}

	out = reg.dropOptedOut(reg.dropLocallyParented(dedupByEventFingerprint(out)))
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ChildSessionID < out[j].ChildSessionID
	})
	if out == nil {
		out = []TransitionNotificationEvent{}
	}
	exp := RemoteExport{Records: out, CursorNext: next}
	if cursor.Comms != nil {
		cx := exportCommsAfter(*cursor.Comms, time.Now())
		exp.Comms = &cx
		exp.CursorNext.Comms = &RemoteCommsCursor{Store: cx.Store, Epoch: cx.Epoch, After: cx.Through}
	}
	return exp, nil
}

// unownedMark identifies one _unowned record for the cursor's position check:
// its logical identity plus its stamp, hashed to a fixed size.
func unownedMark(ev TransitionNotificationEvent) string {
	sum := sha256.Sum256([]byte(EventFingerprint(ev) + "@" + strconv.FormatInt(ev.Timestamp.UnixNano(), 10)))
	return hex.EncodeToString(sum[:8])
}

// journalTurnKey is a journaled turn's identity as both the journal render
// and the producer's record of it carry it: seq, the transcript turn and
// whether it was committed as a stale repeat (#2184), which keys it on its
// emit instant instead of the turn.
func journalTurnKey(ev TransitionNotificationEvent) string {
	return strconv.FormatInt(ev.Seq, 10) + "|" + ev.TurnUUID + "|" + ev.TextHash + "|" + strconv.FormatBool(ev.OutputHashStale)
}

// completionKey matches a completion across its journal line and its ledger
// mirror (noteDoneEmitted stamps the ledger with the turn's own time); only
// the journal line carries the turn signal, so TurnFingerprint differs.
func completionKey(ev TransitionNotificationEvent) string {
	return strings.TrimSpace(ev.ChildSessionID) + "|" + strings.ToLower(strings.TrimSpace(ev.DoneStatus)) + "|" +
		strings.TrimSpace(ev.DoneSummary) + "|" + strconv.FormatInt(ev.Timestamp.UnixNano(), 10)
}

// ledgerMark identifies one completion-ledger entry: its stamp, readable,
// plus a hash of its outcome, so a rewrite that keeps the stamp but changes
// the status or summary (a #1186 rescan that finds a later sentinel) differs.
func ledgerMark(ev TransitionNotificationEvent) string {
	return ev.Timestamp.UTC().Format(time.RFC3339Nano) + "#" + EventFingerprint(ev)[:16]
}

// committedTurn is the producer notifier's memory of a child's last
// committed transition (transitionNotifyRecord): its target status, its turn
// signal and its stamp in unix seconds. isDuplicate compares a new flip with
// it.
type committedTurn struct {
	to  string
	sig string
	at  int64
}

// lastCommittedBefore is the notifier's memory just before a journal's first
// retained line, recovered from the producer's own _unowned records of that
// child (commit order): the last transition committed before it. Nil when
// there is none, e.g. a child with a local parent on the remote.
func lastCommittedBefore(records []TransitionNotificationEvent, before time.Time) *committedTurn {
	var last *committedTurn
	for _, ev := range records {
		if !ev.Timestamp.Before(before) {
			continue
		}
		last = &committedTurn{
			to:  ev.ToStatus,
			sig: TurnFacts{UUID: ev.TurnUUID, TextHash: ev.TextHash}.Signal(),
			at:  ev.Timestamp.Unix(),
		}
	}
	return last
}

// renderJournalTurns renders a child's retained journal lines as the records
// the producer committed for them, replaying its notifier from last (the
// commit before lines[0], or nil). A transition line whose turn signal repeats
// the last committed one:
//
//   - within defaultOutputHashDedupTTL and the same attention class was
//     dropped by isDuplicate (the snapshot edge of a flip recordTerminalTurns
//     already emitted journals a forced-urgent repeat line, issue #2469), so
//     dropped[i] is set: it was never a record and must not cross, or one
//     turn would arrive twice and an info turn would wake as urgent;
//   - otherwise was committed OutputHashStale (issue #2184) and keyed on its
//     emit instant, so it renders the same way instead of collapsing into the
//     previous turn.
//
// A completion line goes through NotifyFinished, which neither dedups nor
// updates that memory.
func renderJournalTurns(lines []TurnJournalEntry, last *committedTurn) (out []TransitionNotificationEvent, dropped []bool) {
	out = make([]TransitionNotificationEvent, len(lines))
	dropped = make([]bool, len(lines))
	ttl := int64(defaultOutputHashDedupTTL.Seconds())
	for i, l := range lines {
		if l.DoneStatus != "" {
			out[i] = journalTurnEvent(l, false)
			continue
		}
		sig := TurnFacts{UUID: l.UUID, TextHash: l.TextHash}.Signal()
		stale := false
		if last != nil && sig != "" && sig == last.sig {
			if attentionClass(last.to) == attentionClass(l.Status) && l.TS.Unix()-last.at <= ttl {
				dropped[i] = true
				out[i] = journalTurnEvent(l, false)
				continue
			}
			stale = true
		}
		out[i] = journalTurnEvent(l, stale)
		last = &committedTurn{to: l.Status, sig: sig, at: l.TS.Unix()}
	}
	return out, dropped
}

// journalTurnEvent renders one journaled turn as the record the producer
// committed for it: same tier, text and turn signal, so the receiving
// consumed-turn ledger recognises a turn it already got through the legacy
// export. stale renders a #2184 stale-signal turn as the producer stored it.
func journalTurnEvent(e TurnJournalEntry, stale bool) TransitionNotificationEvent {
	ev := TransitionNotificationEvent{
		ChildSessionID: e.Child,
		Profile:        e.Profile,
		FromStatus:     string(StatusRunning),
		ToStatus:       e.Status,
		Timestamp:      e.TS,
		Tier:           e.Tier,
		Trigger:        e.Trigger,
		TurnUUID:       e.UUID,
		TextHash:       e.TextHash,
		Text:           e.Text,
		Question:       e.Question,
		Seq:            e.Seq,
		FromID:         e.FromID,
		LastOutputHash: TurnFacts{UUID: e.UUID, TextHash: e.TextHash}.Signal(),
	}
	if e.DoneStatus != "" {
		ev.Kind = transitionKindFinished
		ev.DoneStatus = e.DoneStatus
		ev.DoneSummary = capDoneSummary(e.DoneSummary)
	}
	if stale {
		ev.OutputHashStale = true
		ev.LastOutputHash = emitInstantSignal(ev.Timestamp)
	}
	ev.TurnFingerprint = TurnFingerprint(ev)
	return ev
}

// RemoteCursorFile is one persisted cursor, as `inbox cursor --json` lists it.
type RemoteCursorFile struct {
	Remote    string       `json:"remote"`
	Parent    string       `json:"parent"`
	UpdatedAt time.Time    `json:"updated_at"`
	Cursor    RemoteCursor `json:"cursor"`
}

// RemoteCursorDir holds runtime/remote-cursors/<remote>.<parent>.json.
func RemoteCursorDir() string {
	dir, err := runtimeDataPath("remote-cursors")
	if err != nil {
		return tempAgentDeckPath("runtime", "remote-cursors")
	}
	return dir
}

// RemoteCursorPath is the cursor file for one (remote, parent) pair. Remote
// names cannot contain '.', so the name splits unambiguously.
func RemoteCursorPath(remote, parent string) string {
	return filepath.Join(RemoteCursorDir(), sanitizeInboxName(remote)+"."+sanitizeInboxName(parent)+".json")
}

// LoadRemoteCursor returns the saved cursor; found is false when none exists.
func LoadRemoteCursor(remote, parent string) (RemoteCursor, bool, error) {
	data, err := os.ReadFile(RemoteCursorPath(remote, parent))
	if errors.Is(err, fs.ErrNotExist) {
		return emptyRemoteCursor(), false, nil
	}
	if err != nil {
		return emptyRemoteCursor(), false, err
	}
	var f RemoteCursorFile
	if err := json.Unmarshal(data, &f); err != nil {
		return emptyRemoteCursor(), false, err
	}
	if f.Cursor.Seqs == nil {
		f.Cursor.Seqs = map[string]int64{}
	}
	return f.Cursor, true, nil
}

// SaveRemoteCursor persists a cursor durably (temp file, fsync, rename) under
// a file lock: a CLI drain and the daemon's scheduled drain of the same
// (remote, parent) must not interleave on the shared temp file.
func SaveRemoteCursor(remote, parent string, c RemoteCursor) error {
	data, err := json.MarshalIndent(RemoteCursorFile{Remote: remote, Parent: parent, UpdatedAt: time.Now().UTC(), Cursor: c}, "", "  ")
	if err != nil {
		return err
	}
	path := RemoteCursorPath(remote, parent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := AcquireConfigFileLockTimeout(path, inboxLockWait)
	if err != nil {
		return fmt.Errorf("lock cursor %s: %w", filepath.Base(path), err)
	}
	defer lock.Release()
	return writeFileDurable(path, append(data, '\n'), 0o644)
}

// ListRemoteCursors returns every saved cursor, or those of one remote,
// sorted by remote then parent. An unreadable file is skipped.
func ListRemoteCursors(remote string) ([]RemoteCursorFile, error) {
	dir := RemoteCursorDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []RemoteCursorFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var f RemoteCursorFile
		if json.Unmarshal(data, &f) != nil || f.Remote == "" {
			continue
		}
		if remote != "" && f.Remote != remote {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Remote != out[j].Remote {
			return out[i].Remote < out[j].Remote
		}
		return out[i].Parent < out[j].Parent
	})
	return out, nil
}

// remoteIngestWrite is the per-record write; a test seam for partial batches.
var remoteIngestWrite = WriteInboxEventIfUnseen

// RemoteIngestResult counts what a pulled batch did to the local inbox.
// Stored is every record as stored; Fresh only those this call inserted.
type RemoteIngestResult struct {
	Stored          []TransitionNotificationEvent
	Fresh           []TransitionNotificationEvent
	Written         int
	Duplicates      int
	Unknown         int
	RestoreDetected bool
}

// IngestRemoteRecords writes pulled records into targetID's inbox, scoping
// each child id to `<remote>:<child>` (two hosts can mint the same id) and
// re-deriving the turn fingerprint from the scoped record. Tier and text are
// kept as the remote classified them.
func IngestRemoteRecords(remoteName, targetID string, records []TransitionNotificationEvent) (RemoteIngestResult, error) {
	res := RemoteIngestResult{Stored: make([]TransitionNotificationEvent, 0, len(records))}
	for _, ev := range records {
		ev.SourceRemote = remoteName
		ev.ChildSessionID = RemoteScopedChildID(remoteName, ev.ChildSessionID)
		ev.TurnFingerprint = TurnFingerprint(ev)
		ev.TargetSessionID = targetID
		ev.TargetKind = "parent"
		res.Stored = append(res.Stored, ev)

		presence, err := remoteIngestWrite(targetID, ev)
		if err != nil {
			return res, err
		}
		switch presence {
		case InboxEventInserted:
			res.Written++
			res.Fresh = append(res.Fresh, ev)
		case InboxEventAlreadyPresent:
			res.Duplicates++
		case InboxEventPresenceUnknownAfterLedgerRestore:
			res.Unknown++
			res.RestoreDetected = true
		default:
			res.Unknown++
		}
	}
	return res, nil
}

// ErrRemoteCursorUnsupported marks a remote whose binary predates
// `inbox export --after`; the drain falls back to the full export.
var ErrRemoteCursorUnsupported = errors.New("remote does not support incremental export (--after)")

// Remote talkback failure stages, so the CLI can keep its distinct messages
// and exit codes.
const (
	RemoteTalkbackStageFetch   = "fetch"
	RemoteTalkbackStageWriter  = "writer"
	RemoteTalkbackStageStalled = "stalled"
	RemoteTalkbackStageIngest  = "ingest"
)

// RemoteTalkbackError is a failed drain with the stage it failed at.
type RemoteTalkbackError struct {
	Stage  string
	Writer *WriterStatus
	Err    error
}

func (e *RemoteTalkbackError) Error() string {
	if e.Err == nil {
		return "remote talkback " + e.Stage + " failed"
	}
	return e.Err.Error()
}

func (e *RemoteTalkbackError) Unwrap() error { return e.Err }

// RemoteTalkbackDeps are the transport and wake seams of one drain. FetchAfter
// may be nil (legacy only). Parent resolves the receiving parent for the wake
// lazily, so a drain with nothing fresh never loads the registry.
type RemoteTalkbackDeps struct {
	FetchAfter  func(ctx context.Context, cursor RemoteCursor) (RemoteExport, error)
	FetchAll    func(ctx context.Context) ([]TransitionNotificationEvent, error)
	WriterProbe func(ctx context.Context) (WriterStatus, error)
	Parent      func() (*Instance, string)
	Wake        func(parent *Instance, profile string, ev TransitionNotificationEvent)
}

// RemoteTalkbackResult is one drain's outcome. CursorBefore/After are nil
// when the remote only speaks the legacy full export.
type RemoteTalkbackResult struct {
	RemoteIngestResult
	Writer       *WriterStatus
	Legacy       bool
	CursorBefore *RemoteCursor
	CursorAfter  *RemoteCursor
	Woke         bool
}

// SSHTalkbackDeps wires the real ssh transport for one remote.
func SSHTalkbackDeps(name string, rc RemoteConfig) RemoteTalkbackDeps {
	runner := NewSSHRunner(name, rc)
	return RemoteTalkbackDeps{
		FetchAfter:  runner.FetchRecordsAfter,
		FetchAll:    runner.FetchPendingRecords,
		WriterProbe: runner.FetchWriterStatus,
	}
}

// RunRemoteTalkback performs one incremental drain of remote into targetID:
// fetch after the saved cursor (or the full export from an old remote),
// require a live remote writer, ingest, advance the cursor only when every
// record landed, then wake the parent once for the newest fresh record whose
// tier wakes it. Shared by `remote drain` and the notify-daemon scheduler.
func RunRemoteTalkback(ctx context.Context, remote, targetID string, deps RemoteTalkbackDeps) (RemoteTalkbackResult, error) {
	var res RemoteTalkbackResult
	cursor, cursorFound, cerr := LoadRemoteCursor(remote, targetID)
	if cerr != nil {
		// A corrupt cursor only costs a larger refetch; dedup absorbs it.
		commsLog.Warn("remote_cursor_unreadable", "remote", remote, "parent", targetID, "error", cerr.Error())
	}

	var records []TransitionNotificationEvent
	var next RemoteCursor
	legacy := deps.FetchAfter == nil
	// Comms Ledger (P3): ask for the remote's ledger records on the same
	// round trip, from the position this profile's ledger holds; the
	// round trip's two local times measure the clock offset.
	commsProfile := ""
	if CommsLedgerEnabled() && !legacy {
		commsProfile = talkbackProfile(deps)
		cc := loadRemoteCommsCursor(remote, commsProfile)
		cursor.Comms = &cc
	}
	var commsExp *RemoteCommsExport
	var t0, t1 time.Time
	if !legacy {
		t0 = time.Now()
		exp, err := deps.FetchAfter(ctx, cursor)
		t1 = time.Now()
		commsExp = exp.Comms
		exp.CursorNext.Comms = nil // kept per (remote, profile), not per parent
		switch {
		case errors.Is(err, ErrRemoteCursorUnsupported):
			legacy = true
		case err != nil:
			return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageFetch, Err: err}
		default:
			records, next, res.Writer = exp.Records, exp.CursorNext, exp.Writer
			before := cursor
			res.CursorBefore, res.CursorAfter = &before, &before
		}
	}
	if legacy {
		res.Legacy = true
		var err error
		if records, err = deps.FetchAll(ctx); err != nil {
			return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageFetch, Err: err}
		}
	}
	if res.Writer == nil {
		ws, err := deps.WriterProbe(ctx)
		if err != nil {
			return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageWriter, Err: err}
		}
		res.Writer = &ws
	}
	if !res.Writer.Running {
		return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageStalled, Writer: res.Writer,
			Err: fmt.Errorf("remote %s is not recording session transitions: %s", remote, res.Writer.Detail)}
	}

	if commsExp != nil {
		if err := acceptRemoteComms(remote, commsProfile, commsExp, t0, t1); err != nil {
			// The position stays; the next round trip repeats the batch.
			commsLog.Warn("remote_comms_accept_failed", "remote", remote, "error", err.Error())
		}
	}
	if !res.Legacy && cursor.Comms != nil {
		// The profile cursor may advance independently of the inbox cursor.
		// Report its durable position even if inbox ingest later fails, without
		// changing CursorBefore or persisting it in the per-parent cursor.
		after := cursor
		cc := loadRemoteCommsCursor(remote, commsProfile)
		after.Comms = &cc
		res.CursorAfter = &after
	}

	ingest, err := IngestRemoteRecords(remote, targetID, records)
	res.RemoteIngestResult = ingest
	// Records inserted before a later write failed are fresh now and only
	// AlreadyPresent on the retry, so they wake now or never.
	res.Woke = wakeForRemoteRecords(targetID, ingest.Fresh, deps)
	if err != nil {
		return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageIngest, Err: err}
	}
	switch {
	case !res.Legacy && ingest.Unknown == 0:
		if err := SaveRemoteCursor(remote, targetID, next); err != nil {
			return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageIngest, Err: fmt.Errorf("save cursor: %w", err)}
		}
		reported := next
		reported.Comms = res.CursorAfter.Comms
		res.CursorAfter = &reported
	case res.Legacy && !cursorFound:
		// No position to keep, but the file keeps this conductor enrolled
		// for scheduled talkback once it consumes the ingested records.
		if err := SaveRemoteCursor(remote, targetID, RemoteCursor{Legacy: true}); err != nil {
			commsLog.Warn("remote_cursor_legacy_mark_failed", "remote", remote, "parent", targetID, "error", err.Error())
		}
	}
	return res, nil
}

// wakeForRemoteRecords applies the local wake rule to freshly ingested
// records: only tiers in the parent's [inbox] wake_on wake it, and a batch
// wakes it once, naming the newest such record.
func wakeForRemoteRecords(parentID string, fresh []TransitionNotificationEvent, deps RemoteTalkbackDeps) bool {
	if len(fresh) == 0 || deps.Parent == nil {
		return false
	}
	parent, profile := deps.Parent()
	if parent == nil {
		return false
	}
	cfg := ResolveInboxConfig(parent.Title)
	var pick *TransitionNotificationEvent
	urgent, suppressed := 0, 0
	for i := range fresh {
		if cfg.WakesFor(fresh[i].Tier) {
			urgent++
			pick = &fresh[i]
		} else {
			suppressed++
		}
	}
	_ = BumpInboxStats(parentID, func(s *InboxStats) {
		s.WakeupsUrgent += int64(urgent)
		s.WakeupsSuppressed += int64(suppressed)
	})
	if pick == nil {
		return false
	}
	wake := deps.Wake
	if wake == nil {
		wake = WakeParentForRecord
	}
	wake(parent, profile, *pick)
	return true
}

// remoteWakeWiring builds the wiring WakeParentForRecord fires through. A CLI
// drain exits right after it returns, so the production send is synchronous
// rather than the daemon's fire-and-forget goroutine. Tests swap in a spy.
var remoteWakeWiring = func() *wakeNudgeWiring {
	w := defaultWakeNudgeWiring()
	w.send = func(parent *Instance, profile, message string) error {
		return sendWakeNudgeNoWait(profile, parent.ID, message)
	}
	return w
}

// WakeParentForRecord wakes an idle conductor for one ingested record through
// the same gate, headline and send as a locally committed record.
// profile is the PARENT's local profile (the record's own profile names the
// remote's).
func WakeParentForRecord(parent *Instance, profile string, ev TransitionNotificationEvent) {
	ev.Profile = profile
	(&TransitionNotifier{wake: remoteWakeWiring()}).fireWakeNudge(parent, ev)
}

// talkbackProfile is the local profile a drain's records belong to: the
// receiving parent's, else the process profile.
func talkbackProfile(deps RemoteTalkbackDeps) string {
	if deps.Parent != nil {
		if _, profile := deps.Parent(); profile != "" {
			return profile
		}
	}
	return events.CurrentProfile()
}
