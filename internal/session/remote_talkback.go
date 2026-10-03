package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Incremental remote talkback (issue #2469 family, PR3). The #1948 drain
// re-shipped a remote's whole completion ledger and every inbox on every run
// and never woke the conductor. A conductor now keeps one cursor per
// (remote, conductor): the newest turn-journal seq it holds per remote child
// plus the timestamp of the newest ledger record. The remote answers only
// what is newer, and the cursor advances only once every record of the batch
// durably landed (inserted or already present), so a failed write refetches
// the batch and the inbox dedup absorbs the overlap.

// RemoteCursor is the drain position against one remote: per child the newest
// turn-journal seq already received, and TS, the newest ledger / unowned
// record already received from children that have no journal. On the wire it
// is one flat JSON object: {"<child_id>": <seq>, "_ts": "<RFC3339>"}.
type RemoteCursor struct {
	Seqs map[string]int64
	TS   time.Time
}

const remoteCursorTSKey = "_ts"

// MarshalJSON writes the flat wire form.
func (c RemoteCursor) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(c.Seqs)+1)
	for k, v := range c.Seqs {
		m[k] = v
	}
	if !c.TS.IsZero() {
		m[remoteCursorTSKey] = c.TS.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads the flat wire form. Unknown "_"-prefixed keys are
// ignored so a later producer can add metadata without breaking this reader.
func (c *RemoteCursor) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := RemoteCursor{Seqs: map[string]int64{}}
	for k, v := range raw {
		if k == remoteCursorTSKey {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return fmt.Errorf("cursor %s: %w", remoteCursorTSKey, err)
			}
			if s != "" {
				ts, err := time.Parse(time.RFC3339Nano, s)
				if err != nil {
					return fmt.Errorf("cursor %s: %w", remoteCursorTSKey, err)
				}
				out.TS = ts
			}
			continue
		}
		var seq int64
		if err := json.Unmarshal(v, &seq); err != nil {
			if strings.HasPrefix(k, "_") {
				continue
			}
			return fmt.Errorf("cursor seq for %q: %w", k, err)
		}
		out.Seqs[k] = seq
	}
	*c = out
	return nil
}

// ParseRemoteCursor parses the --after argument. Empty means "from scratch".
func ParseRemoteCursor(s string) (RemoteCursor, error) {
	var c RemoteCursor
	if strings.TrimSpace(s) == "" {
		return RemoteCursor{Seqs: map[string]int64{}}, nil
	}
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
}

// remoteExportNewChildLines bounds what a child unknown to the cursor ships
// on its first drain.
const remoteExportNewChildLines = 64

// ExportRecordsAfter is the incremental, read-only remote export:
//
//   - turn-journal lines newer than the cursor's seq for each child (a child
//     the cursor does not know, or whose journal restarted below the cursor,
//     ships its last 64 lines);
//   - completion-ledger entries newer than the cursor's _ts (skipped when a
//     journal line of this batch already carries the same completion), and
//     for children with NO journal (older producers, non-transcript tools)
//     their _unowned transitions newer than _ts.
//
// Other parents' inboxes are never exported here: the drain's --into parent
// decides where records land. The no_notify opt-out filter applies as in
// ExportPendingRecords.
func ExportRecordsAfter(cursor RemoteCursor) (RemoteExport, error) {
	next := RemoteCursor{Seqs: map[string]int64{}, TS: cursor.TS}
	journaled := map[string]bool{}
	var out []TransitionNotificationEvent

	dir := TurnJournalDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return RemoteExport{}, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		turnJournalMu.Lock()
		lines, err := readTurnJournalLocked(filepath.Join(dir, e.Name()))
		turnJournalMu.Unlock()
		if err != nil {
			return RemoteExport{}, fmt.Errorf("export: unreadable turn journal %s: %w", e.Name(), err)
		}
		if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1].Child) == "" {
			continue
		}
		child := lines[len(lines)-1].Child
		journaled[child] = true
		last := lines[len(lines)-1].Seq
		next.Seqs[child] = last
		since, known := cursor.Seqs[child]
		if !known || last < since {
			// Unknown child, or a journal that was removed and recreated (its
			// seqs restarted): ship the recent tail, dedup absorbs repeats.
			if len(lines) > remoteExportNewChildLines {
				lines = lines[len(lines)-remoteExportNewChildLines:]
			}
			since = 0
		}
		for _, l := range lines {
			if l.Seq > since {
				out = append(out, journalTurnEvent(l))
			}
		}
	}

	ledger, err := exportLedgerRecords()
	if err != nil {
		return RemoteExport{}, err
	}
	unowned, err := ReadInboxEvents(UnownedInboxID)
	if err != nil {
		return RemoteExport{}, fmt.Errorf("export: unreadable inbox %s: %w", UnownedInboxID, err)
	}
	shipped := make(map[string]bool, len(out))
	for _, ev := range out {
		shipped[ev.TurnFingerprint] = true
	}
	for _, ev := range ledger {
		// A journaled child's completion normally rides its journal line (same
		// turn fingerprint); the ledger copy still ships when it is the only
		// record of it, e.g. a run-task exit without a sentinel.
		// Either way it counts as received, so _ts moves past it.
		if !ev.Timestamp.After(cursor.TS) {
			continue
		}
		if ev.Timestamp.After(next.TS) {
			next.TS = ev.Timestamp
		}
		if !shipped[ev.TurnFingerprint] {
			out = append(out, ev)
		}
	}
	for _, ev := range unowned {
		if journaled[strings.TrimSpace(ev.ChildSessionID)] || !ev.Timestamp.After(cursor.TS) {
			continue
		}
		out = append(out, ev)
		if ev.Timestamp.After(next.TS) {
			next.TS = ev.Timestamp
		}
	}

	out = dedupByEventFingerprint(out)
	out, err = dropSuppressedChildren(out)
	if err != nil {
		return RemoteExport{}, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ChildSessionID < out[j].ChildSessionID
	})
	if out == nil {
		out = []TransitionNotificationEvent{}
	}
	return RemoteExport{Records: out, CursorNext: next}, nil
}

// journalTurnEvent renders one journaled turn as the record the producer
// committed for it: same tier, text and turn signal, so the receiving
// consumed-turn ledger recognises a turn it already got through the legacy
// export.
func journalTurnEvent(e TurnJournalEntry) TransitionNotificationEvent {
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
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RemoteCursor{Seqs: map[string]int64{}}, false, nil
		}
		return RemoteCursor{Seqs: map[string]int64{}}, false, err
	}
	var f RemoteCursorFile
	if err := json.Unmarshal(data, &f); err != nil {
		return RemoteCursor{Seqs: map[string]int64{}}, false, err
	}
	if f.Cursor.Seqs == nil {
		f.Cursor.Seqs = map[string]int64{}
	}
	return f.Cursor, true, nil
}

// SaveRemoteCursor persists a cursor durably (temp file, fsync, rename).
func SaveRemoteCursor(remote, parent string, c RemoteCursor) error {
	data, err := json.MarshalIndent(RemoteCursorFile{Remote: remote, Parent: parent, UpdatedAt: time.Now().UTC(), Cursor: c}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileDurable(RemoteCursorPath(remote, parent), append(data, '\n'), 0o644)
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
	cursor, _, cerr := LoadRemoteCursor(remote, targetID)
	if cerr != nil {
		// A corrupt cursor only costs a larger refetch; dedup absorbs it.
		commsLog.Warn("remote_cursor_unreadable", "remote", remote, "parent", targetID, "error", cerr.Error())
	}

	var records []TransitionNotificationEvent
	var next RemoteCursor
	legacy := deps.FetchAfter == nil
	if !legacy {
		exp, err := deps.FetchAfter(ctx, cursor)
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

	ingest, err := IngestRemoteRecords(remote, targetID, records)
	res.RemoteIngestResult = ingest
	if err != nil {
		return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageIngest, Err: err}
	}
	if !res.Legacy && ingest.Unknown == 0 {
		if err := SaveRemoteCursor(remote, targetID, next); err != nil {
			return res, &RemoteTalkbackError{Stage: RemoteTalkbackStageIngest, Err: fmt.Errorf("save cursor: %w", err)}
		}
		res.CursorAfter = &next
	}
	res.Woke = wakeForRemoteRecords(targetID, ingest.Fresh, deps)
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
