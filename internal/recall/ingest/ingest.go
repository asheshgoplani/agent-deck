// Package ingest is the only writer of recall.db. It owns the source
// ledger, the resume cursors, the single writer connection and the byte
// budget. Readers hand it decoded events; it never parses a transcript
// itself.
package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
	"github.com/asheshgoplani/agent-deck/internal/recall/enrich"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
)

// ingestLog carries the one line a failed or quarantined source pass
// leaves in the log; Result.Errors and Result.Quarantined count them.
var ingestLog = logging.ForComponent(logging.CompRecall)

// Defaults for the interactive budget (the sweep that runs before a search)
// and the batch transaction bound.
const (
	InteractiveDeadline = 150 * time.Millisecond
	InteractiveBytes    = 32 << 20
	// BatchTextBytes bounds one transaction in decoded text so a checkpoint
	// can land between batches and the WAL never grows toward the whole
	// write set.
	BatchTextBytes = 8 << 20
	// sigBytes is the prefix / tail signature window.
	sigBytes = 4096
	// previewChars is the card preview taken from the first prompt.
	previewChars = 200
	// maxReportedDeferred caps the paths listed in a Result.
	maxReportedDeferred = 20
	// TextTierClipped and TextTierFull are the shipped body tiers.
	TextTierClipped = "clipped"
	TextTierFull    = "full"
)

// Registry is the read side of state.db the ingester needs: which deck
// session a harness conversation is bound to (authoritative links only) and
// the hints and tags that feed the card's FTS columns. nil means none.
//
// recall.db is machine-global while state.db is per profile, so every call
// names the profile whose transcript root the conversation was found under:
// the implementation looks there first, then in the other profiles (a deck
// session in profile A can run under account B and write its transcript
// under B's config dir). A sweep under one profile must never overwrite
// another profile's cards with its own empty answer.
type Registry interface {
	DeckID(profile, harness, nativeID string) string
	Hints(profile, harness, nativeID string) (hints, tags string)
	// ChangedSince lists the conversations whose hints, tags or link were
	// written at or after since, so their cards are re-projected even when
	// their transcripts did not move.
	ChangedSince(since time.Time) []Ref
}

// Ref names one harness conversation.
type Ref struct {
	Harness  string
	NativeID string
}

// metaLastSweep is the meta key holding the start time of the last
// completed sweep (unix seconds).
const metaLastSweep = "last_sweep"

// metaMigEmptyCompact marks an index whose zero-character compaction rows
// (written before the Codex reader stopped storing them) were removed.
const metaMigEmptyCompact = "mig_empty_compact"

// UsageSink receives the usage records of a source whose conversation is
// bound to a deck session, so cost events are written from the same pass
// that indexed the text. profile is the transcript's profile; deckID was
// resolved by Registry.DeckID with the same arguments, so the sink can
// write to the state.db that owns the link.
type UsageSink interface {
	Usage(profile, deckID string, events []reader.Usage) error
}

// Options configures one Ingester.
type Options struct {
	Roots    []reader.Root
	Readers  []reader.Reader // default: reader.Registry()
	Registry Registry
	Usage    UsageSink
	// Budget bounds one Sweep (nil: unlimited).
	Budget *reader.Budget
	// PerSourceBytes caps how much of one file a single pass parses; the
	// rest continues next pass (0: unlimited).
	PerSourceBytes int64
	Gate           *Gate
	// TextTier is "clipped" (default, 8 KiB bodies) or "full".
	TextTier string
	// BatchBytes bounds one transaction in decoded text (0: BatchTextBytes).
	BatchBytes int64
	// Since skips sources last modified before it (zero: all).
	Since time.Time
	// NewestFirst parses recently modified sources first so value appears
	// immediately during a backfill.
	NewestFirst bool
	// Verify re-checks every ledgered source's signatures even when size
	// and mtime are unchanged (recall sweep --full).
	Verify bool
	// QueuePath is the hook queue (recall.QueuePath()); a Sweep drains it
	// and parses the listed files before the rest ("" or absent: nothing).
	QueuePath string
	// Progress, when set, is called after every source.
	Progress func(p Progress)
	// Now overrides the clock in tests.
	Now func() time.Time
}

// Progress is one line of backfill feedback.
type Progress struct {
	Path      string
	Bytes     int64
	Messages  int
	Done      int
	Total     int
	Deferred  bool
	Skipped   string
	BytesRead int64
}

// Result summarises one Sweep.
type Result struct {
	Discovered    int      `json:"discovered"`
	Queued        int      `json:"queued,omitempty"`
	Unchanged     int      `json:"unchanged"`
	Parsed        int      `json:"parsed"`
	Deferred      int      `json:"deferred"`
	DeferredBytes int64    `json:"deferred_bytes"`
	DeferredPaths []string `json:"deferred_paths,omitempty"`
	Missing       int      `json:"missing"`
	Quarantined   int      `json:"quarantined"`
	Errors        int      `json:"errors"`
	BytesRead     int64    `json:"bytes_read"`
	Messages      int      `json:"messages"`
	Sessions      int      `json:"sessions"`
	// Enriched counts the artifacts the in-sweep drain wrote;
	// EnrichDeferred the queue rows it left for the next pass.
	Enriched       int   `json:"enriched"`
	EnrichDeferred int   `json:"enrich_deferred,omitempty"`
	ElapsedMS      int64 `json:"elapsed_ms"`
}

// Ingester runs sweeps against one open store.
type Ingester struct {
	st   *store.Store
	opts Options
}

// New returns an Ingester over st.
func New(st *store.Store, opts Options) *Ingester {
	if opts.Readers == nil {
		opts.Readers = reader.Registry()
	}
	if opts.TextTier == "" {
		opts.TextTier = TextTierClipped
	}
	if opts.BatchBytes <= 0 {
		opts.BatchBytes = BatchTextBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Ingester{st: st, opts: opts}
}

// ledgerRow is one source row as the sweep needs it.
type ledgerRow struct {
	srcID     int64
	path      string
	dev, ino  uint64
	size      int64
	mtimeNS   int64
	parsedTo  int64
	prefixSig string
	tailSig   string
	readerVer int
	state     int
	sessID    int64
	seen      bool
}

type candidate struct {
	ref reader.SourceRef
	row *ledgerRow // nil for a new source
	rd  reader.Reader
}

// cursor is the reader's cursor kind: how the ledger resumes this source.
func (c candidate) cursor() reader.CursorKind { return reader.CursorOf(c.rd) }

// Sweep discovers every source, parses what changed within the budget,
// marks what vanished, and projects cards for every touched session.
func (in *Ingester) Sweep(ctx context.Context) (Result, error) {
	start := in.opts.Now()
	var res Result
	if err := in.opts.Gate.Check(); err != nil {
		return res, err
	}
	if err := in.migrateEmptyCompactRows(); err != nil {
		return res, err
	}
	ledger, byKey, byPath, err := in.loadLedger()
	if err != nil {
		return res, err
	}
	cands, err := in.discover(ctx, &res, byKey, byPath)
	if err != nil {
		return res, err
	}
	in.prioritizeQueued(&res, cands)
	touched := map[int64]bool{}
	for i, c := range cands {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if in.opts.Budget.Exhausted() {
			in.deferCandidate(&res, c)
			continue
		}
		out, err := in.ingestSource(ctx, c)
		res.BytesRead += out.bytes
		res.Messages += out.messages
		if out.sessID != 0 {
			touched[out.sessID] = true
		}
		in.recordOutcome(&res, c, out, err)
		if passFailed(err) && ctx.Err() != nil {
			return res, err
		}
		if in.opts.Progress != nil {
			in.opts.Progress(progressOf(c, out, err, i+1, len(cands), res.BytesRead))
		}
	}
	for _, row := range ledger {
		if row.seen || row.state == recall.SourceMissing {
			continue
		}
		if err := in.markMissing(row); err != nil {
			return res, err
		}
		res.Missing++
	}
	if in.opts.Verify {
		// --full also re-projects every card so hints, tags and deck ids
		// changed in state.db since the last pass reach the ranking feed.
		for _, row := range ledger {
			if row.sessID != 0 {
				touched[row.sessID] = true
			}
		}
	} else if err := in.touchAnnotated(touched); err != nil {
		return res, err
	}
	if err := in.projectCards(touched); err != nil {
		return res, err
	}
	if _, err := in.st.W.Exec(`INSERT INTO meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		metaLastSweep, strconv.FormatInt(start.Unix(), 10)); err != nil {
		return res, err
	}
	res.Sessions = len(touched)
	// The cheap classifiers run inside the sweep (design 9, T1): the
	// queue rows the re-projected cards left are drained under what is
	// left of the budget, so an interactive pass never overruns on them
	// and a backfill leaves every indexed session classified.
	if !in.opts.Budget.Exhausted() {
		er, err := enrich.New(in.st, enrich.Options{Budget: in.opts.Budget, Now: in.opts.Now}).Drain(ctx)
		res.Enriched, res.EnrichDeferred = er.Written, er.Deferred
		if err != nil && ctx.Err() != nil {
			return res, err
		}
		if err != nil {
			return res, fmt.Errorf("recall: enrich: %w", err)
		}
	}
	res.ElapsedMS = in.opts.Now().Sub(start).Milliseconds()
	return res, nil
}

// prioritizeQueued drains the hook queue and moves the files it names to
// the front of the candidate list, so the transcripts hooks just reported
// are parsed inside the interactive budget before the rest of the walk.
func (in *Ingester) prioritizeQueued(res *Result, cands []candidate) {
	if in.opts.QueuePath == "" {
		return
	}
	entries, err := recall.Drain(in.opts.QueuePath)
	if err != nil || len(entries) == 0 {
		return
	}
	res.Queued = len(entries)
	rank := make(map[string]int, len(entries))
	for i, e := range entries {
		if real, err := filepath.EvalSymlinks(e.Path); err == nil {
			rank[real] = i + 1
		} else {
			rank[filepath.Clean(e.Path)] = i + 1
		}
	}
	// Queued files first, in queue order (newest first); the rest keep the
	// walk's order.
	sort.SliceStable(cands, func(i, j int) bool {
		ri, rj := rank[cands[i].ref.Path], rank[cands[j].ref.Path]
		if ri == 0 || rj == 0 {
			return ri != 0
		}
		return ri < rj
	})
}

// SweepFiles indexes exactly the given transcript paths (a Stop hook's
// file, a queue entry): each is located under the roots by its harness's
// reader, parsed within the budget and its card re-projected. Nothing is
// walked and nothing is marked missing; a path outside every root is
// counted as an error and never opened.
func (in *Ingester) SweepFiles(ctx context.Context, paths []string) (Result, error) {
	start := in.opts.Now()
	var res Result
	if err := in.opts.Gate.Check(); err != nil {
		return res, err
	}
	_, byKey, byPath, err := in.loadLedger()
	if err != nil {
		return res, err
	}
	touched := map[int64]bool{}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		ref, rd, ok := reader.Locate(p, in.opts.Roots)
		if !ok || !in.hasReader(rd) {
			res.Errors++
			continue
		}
		res.Discovered++
		row := byKey[[2]uint64{ref.Dev, ref.Ino}]
		if row == nil {
			row = byPath[ref.Path]
		}
		c := candidate{ref: ref, row: row, rd: rd}
		if row != nil && !in.changed(row, ref) {
			res.Unchanged++
			if row.sessID != 0 {
				touched[row.sessID] = true
			}
			continue
		}
		if in.opts.Budget.Exhausted() {
			in.deferCandidate(&res, c)
			continue
		}
		out, err := in.ingestSource(ctx, c)
		res.BytesRead += out.bytes
		res.Messages += out.messages
		if out.sessID != 0 {
			touched[out.sessID] = true
		}
		in.recordOutcome(&res, c, out, err)
		if passFailed(err) && ctx.Err() != nil {
			return res, err
		}
	}
	if err := in.projectCards(touched); err != nil {
		return res, err
	}
	res.Sessions = len(touched)
	res.ElapsedMS = in.opts.Now().Sub(start).Milliseconds()
	return res, nil
}

// hasReader reports whether rd is one of this ingester's readers.
func (in *Ingester) hasReader(rd reader.Reader) bool {
	for _, r := range in.opts.Readers {
		if r.Harness() == rd.Harness() {
			return true
		}
	}
	return false
}

// migrateEmptyCompactRows drops, once per index, the zero-character
// compact_summary rows an earlier reader stored for Codex compactions
// (the edge and the superseded flags they left stand). A rebuilt index
// never has them; an existing one is cleaned on its next sweep.
func (in *Ingester) migrateEmptyCompactRows() error {
	var v string
	if err := in.st.W.QueryRow(`SELECT v FROM meta WHERE k=?`, metaMigEmptyCompact).Scan(&v); err == nil {
		return nil
	}
	tx, err := in.st.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	where := `class=? AND nchars=0`
	if _, err := tx.Exec(`DELETE FROM msg_fts WHERE rowid IN (SELECT msg_id FROM msg WHERE `+where+`)`, int(classify.CompactSummary)); err != nil {
		return fmt.Errorf("recall: migrate empty compactions (fts): %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM msg WHERE `+where, int(classify.CompactSummary)); err != nil {
		return fmt.Errorf("recall: migrate empty compactions: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO meta(k, v) VALUES (?, '1') ON CONFLICT(k) DO NOTHING`, metaMigEmptyCompact); err != nil {
		return err
	}
	return tx.Commit()
}

// touchAnnotated adds the sessions whose state.db rows changed since the
// last sweep, so an annotation reaches the card without a transcript
// change. One second of slack covers the second-granular timestamps.
func (in *Ingester) touchAnnotated(touched map[int64]bool) error {
	if in.opts.Registry == nil {
		return nil
	}
	var v string
	since := int64(0)
	if err := in.st.W.QueryRow(`SELECT v FROM meta WHERE k=?`, metaLastSweep).Scan(&v); err == nil {
		since, _ = strconv.ParseInt(v, 10, 64)
	}
	for _, ref := range in.opts.Registry.ChangedSince(time.Unix(since-1, 0)) {
		if err := in.touchConversation(touched, ref); err != nil {
			return err
		}
	}
	return nil
}

// touchConversation marks every session indexed for ref. The same
// conversation id may be indexed under several profiles (and a
// conversation-scoped hint names no harness): all of them are touched.
func (in *Ingester) touchConversation(touched map[int64]bool, ref Ref) error {
	rows, err := in.st.W.Query(`SELECT sess_id FROM session WHERE host_uid=? AND native_id=? AND (harness=? OR ?='')`,
		store.LocalHostUID, ref.NativeID, ref.Harness, ref.Harness)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		touched[id] = true
	}
	return rows.Err()
}

// discover runs every reader over its roots and returns the sources that
// need a pass: new files (subject to Since) and ledgered files that changed.
// Unchanged ledger rows are marked seen so they are not reported missing.
func (in *Ingester) discover(ctx context.Context, res *Result, byKey map[[2]uint64]*ledgerRow, byPath map[string]*ledgerRow) ([]candidate, error) {
	var cands []candidate
	for _, rd := range in.opts.Readers {
		roots := rootsFor(in.opts.Roots, rd.Harness())
		if len(roots) == 0 {
			continue
		}
		err := rd.Discover(ctx, roots, func(ref reader.SourceRef) error {
			res.Discovered++
			row := byKey[[2]uint64{ref.Dev, ref.Ino}]
			if row == nil {
				row = byPath[ref.Path]
			}
			if row != nil {
				row.seen = true
				if !in.changed(row, ref) {
					res.Unchanged++
					return nil
				}
			} else if !in.opts.Since.IsZero() && ref.MtimeNS < in.opts.Since.UnixNano() {
				return nil
			}
			cands = append(cands, candidate{ref: ref, row: row, rd: rd})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if in.opts.NewestFirst {
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].ref.MtimeNS > cands[j].ref.MtimeNS })
	}
	return cands, nil
}

// recordOutcome tallies one source pass into the sweep result.
func (in *Ingester) recordOutcome(res *Result, c candidate, out sourceOutcome, err error) {
	switch {
	case errors.Is(err, reader.ErrBudget):
		res.Parsed++
		res.Deferred++
		res.DeferredBytes += c.ref.Size - out.parsedTo
		if len(res.DeferredPaths) < maxReportedDeferred {
			res.DeferredPaths = append(res.DeferredPaths, c.ref.Path)
		}
	case errors.Is(err, errQuarantined):
		res.Quarantined++
		ingestLog.Info("recall_source_quarantined", slog.String("path", c.ref.Path), slog.String("harness", c.ref.Harness))
	case err != nil:
		res.Errors++
		ingestLog.Warn("recall_source_failed", slog.String("path", c.ref.Path), slog.String("harness", c.ref.Harness),
			slog.Int64("parsed_to", out.parsedTo), slog.String("error", err.Error()))
	default:
		res.Parsed++
	}
}

func progressOf(c candidate, out sourceOutcome, err error, done, total int, bytesRead int64) Progress {
	p := Progress{Path: c.ref.Path, Bytes: out.bytes, Messages: out.messages, Done: done, Total: total,
		Deferred: errors.Is(err, reader.ErrBudget), BytesRead: bytesRead}
	switch {
	case errors.Is(err, errQuarantined):
		p.Skipped = "quarantined"
	case passFailed(err):
		p.Skipped = err.Error()
	}
	return p
}

// passFailed reports a real error from one source pass, as opposed to the
// two expected early stops (budget exhausted, source quarantined).
func passFailed(err error) bool {
	return err != nil && !errors.Is(err, reader.ErrBudget) && !errors.Is(err, errQuarantined)
}

func (in *Ingester) deferCandidate(res *Result, c candidate) {
	res.Deferred++
	pending := c.ref.Size
	if c.row != nil && c.row.parsedTo <= c.ref.Size {
		pending -= c.row.parsedTo
	}
	res.DeferredBytes += pending
	if len(res.DeferredPaths) < maxReportedDeferred {
		res.DeferredPaths = append(res.DeferredPaths, c.ref.Path)
	}
}

func rootsFor(roots []reader.Root, harness string) []reader.Root {
	var out []reader.Root
	for _, r := range roots {
		if r.Harness == harness {
			out = append(out, r)
		}
	}
	return out
}

// changed reports whether a ledgered source needs a pass: size or mtime
// moved, the reader version moved, the last pass was cut short or failed
// (a read error is retried next sweep, from its last checkpoint), or the
// file is back after a missing pass.
func (in *Ingester) changed(row *ledgerRow, ref reader.SourceRef) bool {
	if in.opts.Verify {
		return true
	}
	if row.state == recall.SourceQuarantined {
		return row.size != ref.Size || row.mtimeNS != ref.MtimeNS
	}
	if row.state != recall.SourceOK || row.readerVer != recall.ReaderVersion {
		return true
	}
	return row.size != ref.Size || row.mtimeNS != ref.MtimeNS
}

func (in *Ingester) loadLedger() ([]*ledgerRow, map[[2]uint64]*ledgerRow, map[string]*ledgerRow, error) {
	rows, err := in.st.W.Query(`SELECT src_id, path, dev, ino, size, mtime_ns, parsed_to, prefix_sig, tail_sig, reader_ver, state, sess_id
		FROM source WHERE host_uid=?`, store.LocalHostUID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("recall: load ledger: %w", err)
	}
	defer rows.Close()
	var all []*ledgerRow
	byKey := map[[2]uint64]*ledgerRow{}
	byPath := map[string]*ledgerRow{}
	for rows.Next() {
		r := &ledgerRow{}
		var dev, ino int64
		if err := rows.Scan(&r.srcID, &r.path, &dev, &ino, &r.size, &r.mtimeNS, &r.parsedTo, &r.prefixSig, &r.tailSig, &r.readerVer, &r.state, &r.sessID); err != nil {
			return nil, nil, nil, err
		}
		r.dev, r.ino = uint64(dev), uint64(ino) //nolint:gosec // dev/ino are opaque identifier bits stored as int64 for SQLite, not magnitudes
		all = append(all, r)
		if r.ino != 0 {
			byKey[[2]uint64{r.dev, r.ino}] = r
		}
		byPath[r.path] = r
	}
	return all, byKey, byPath, rows.Err()
}

var errQuarantined = errors.New("recall: source quarantined")

type sourceOutcome struct {
	bytes    int64
	messages int
	sessID   int64
	parsedTo int64
}

// ingestSource runs one pass over one file: identity and append checks,
// then the reader feeding a transactional sink, then the ledger update.
func (in *Ingester) ingestSource(ctx context.Context, c candidate) (sourceOutcome, error) {
	var out sourceOutcome
	ref := c.ref
	kind := c.cursor()
	prefix := ""
	if kind != reader.CursorOpaque {
		var err error
		if prefix, err = hashRange(ref.Path, 0, sigBytes); err != nil {
			return out, err
		}
	}
	row := c.row
	from := int64(0)
	var err error
	if row == nil {
		row, err = in.insertSource(ref, prefix)
		if err != nil {
			return out, err
		}
	} else if from, err = in.resumeOffset(row, ref, prefix, kind); err != nil {
		return out, err
	}
	budget := in.sourceBudget()
	sink := newSink(in, row, ref, from, budget)
	parsedTo, rerr := c.rd.Ingest(ctx, ref, from, sink, budget)
	if in.opts.Budget != nil {
		in.opts.Budget.Consume(budget.Consumed())
	}
	out.bytes = budget.Consumed()
	out.parsedTo = parsedTo
	if sink.fatal != nil {
		_ = sink.rollback()
		return out, sink.fatal
	}
	if sink.quarantineWhy != "" {
		_ = sink.rollback()
		if err := in.finishSource(row, ref, 0, prefix, recall.SourceQuarantined, sink.quarantineWhy, kind); err != nil {
			return out, err
		}
		return out, errQuarantined
	}
	if rerr != nil && !errors.Is(rerr, reader.ErrBudget) {
		_ = sink.rollback()
		if ctx.Err() != nil {
			return out, rerr
		}
		// Rows up to the last mid-pass checkpoint are committed and stay;
		// the cursor rewinds to that checkpoint, never to the pass start,
		// so the next pass does not insert them again.
		out.parsedTo = sink.checkpoint
		if err := in.finishSource(row, ref, sink.checkpoint, prefix, recall.SourceError, rerr.Error(), kind); err != nil {
			return out, err
		}
		return out, rerr
	}
	if err := sink.commit(parsedTo); err != nil {
		return out, err
	}
	out.messages = sink.messages
	out.sessID = sink.sessID
	if out.sessID == 0 {
		out.sessID = row.sessID // nothing new to parse: still re-project the card
	}
	state := recall.SourceOK
	if errors.Is(rerr, reader.ErrBudget) {
		state = recall.SourcePartial
	}
	ledgered := ref
	if state == recall.SourceOK && kind == reader.CursorBytes && parsedTo < ref.Size {
		// The reader stopped short of the end on its own: a torn trailing
		// line, or bytes the harness has not committed yet (Codex's
		// projection cursor). The ledger records the size it parsed to,
		// not the file's, so changed() re-examines the tail next sweep
		// even when the file is never written again.
		ledgered.Size = parsedTo
	}
	if err := in.finishSource(row, ledgered, parsedTo, prefix, state, "", kind); err != nil {
		return out, err
	}
	if sink.sessID != 0 {
		if _, err := in.st.W.Exec(`UPDATE source SET sess_id=? WHERE src_id=?`, sink.sessID, row.srcID); err != nil {
			return out, err
		}
	}
	return out, rerr
}

// resumeOffset decides where the next pass over a ledgered source starts:
// the old cursor when the file only grew, 0 after a full reparse (the
// inode holds a different file, the file was truncated, the bytes under
// the cursor were rewritten, or the reader's cursor kind is CursorNone and
// every change is a rewrite). A quarantined row stays quarantined. The
// path and profile are refreshed when the file moved.
func (in *Ingester) resumeOffset(row *ledgerRow, ref reader.SourceRef, prefix string, kind reader.CursorKind) (int64, error) {
	fullReparse := false
	switch {
	case row.state == recall.SourceQuarantined:
		// A quarantined copy that later diverged is still a copy.
		if err := in.finishSource(row, ref, row.parsedTo, prefix, recall.SourceQuarantined, "copy of an indexed source", kind); err != nil {
			return 0, err
		}
		return 0, errQuarantined
	case row.state == recall.SourceMissing:
		// Back after a missing pass: its rows were dropped with the
		// tombstone, so every byte is new again.
		fullReparse = true
		if _, err := in.st.W.Exec(`DELETE FROM tombstone WHERE src_id=?`, row.srcID); err != nil {
			return 0, err
		}
	case kind == reader.CursorNone:
		fullReparse = true // rewritten wholesale: nothing to resume
	case kind == reader.CursorOpaque:
		// A row-id cursor over a store that is not byte-addressable: no
		// signatures; a cursor above the reported size means the store
		// was reset.
		fullReparse = row.parsedTo > ref.Size
	case row.prefixSig != "" && row.prefixSig != prefix:
		fullReparse = true // the inode now holds a different file
	case row.parsedTo > ref.Size:
		fullReparse = true // truncated
	case row.parsedTo > 0:
		tail, err := hashRange(ref.Path, row.parsedTo-sigBytes, row.parsedTo)
		if err != nil {
			return 0, err
		}
		if tail != row.tailSig {
			fullReparse = true // same-length rewrite: the spans are invalid
		}
	}
	if fullReparse {
		if err := in.dropSourceRows(row.srcID); err != nil {
			return 0, err
		}
		row.parsedTo = 0
	}
	if row.path != ref.Path {
		if _, err := in.st.W.Exec(`UPDATE source SET path=?, profile=? WHERE src_id=?`, ref.Path, ref.Profile, row.srcID); err != nil {
			return 0, err
		}
		row.path = ref.Path
	}
	return row.parsedTo, nil
}

// handOffUsage gives the usage records accumulated since the last
// checkpoint to the cost sink, keyed by the transcript's profile, when the
// conversation (or, for a subagent, its parent) is bound to a deck session.
func (in *Ingester) handOffUsage(ref reader.SourceRef, sink *sink) error {
	if in.opts.Usage == nil || len(sink.usage) == 0 || in.opts.Registry == nil {
		return nil
	}
	native := ref.NativeID
	if sink.nativeID != "" {
		native = sink.nativeID
	}
	if ref.IsSidechain && ref.ParentNativeID != "" {
		native = ref.ParentNativeID
	}
	deck := in.opts.Registry.DeckID(ref.Profile, ref.Harness, native)
	if deck == "" {
		return nil
	}
	if err := in.opts.Usage.Usage(ref.Profile, deck, sink.usage); err != nil {
		return err
	}
	sink.usage = sink.usage[:0]
	return nil
}

// sourceBudget derives the per-pass budget for one source from the sweep
// budget and the per-source cap.
func (in *Ingester) sourceBudget() *reader.Budget {
	var d time.Duration
	bytes := in.opts.PerSourceBytes
	if b := in.opts.Budget; b != nil {
		if !b.Deadline.IsZero() {
			d = time.Until(b.Deadline)
			if d <= 0 {
				d = time.Nanosecond
			}
		}
		if b.BytesLeft > 0 && (bytes == 0 || b.BytesLeft < bytes) {
			bytes = b.BytesLeft
		}
	}
	return reader.NewBudget(d, bytes)
}

func (in *Ingester) insertSource(ref reader.SourceRef, prefix string) (*ledgerRow, error) {
	res, err := in.st.W.Exec(`INSERT INTO source(host_uid, harness, profile, path, dev, ino, size, mtime_ns, parsed_to, prefix_sig, reader_ver, retention_d, state, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		store.LocalHostUID, ref.Harness, ref.Profile, ref.Path, int64(ref.Dev), int64(ref.Ino), ref.Size, ref.MtimeNS, prefix, //nolint:gosec // dev/ino are opaque identifier bits stored as int64 for SQLite, not magnitudes
		recall.ReaderVersion, ref.RetentionDays, recall.SourceOK, in.opts.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("recall: insert source: %w", err)
	}
	id, _ := res.LastInsertId()
	return &ledgerRow{srcID: id, path: ref.Path, dev: ref.Dev, ino: ref.Ino, prefixSig: prefix, readerVer: recall.ReaderVersion}, nil
}

func (in *Ingester) finishSource(row *ledgerRow, ref reader.SourceRef, parsedTo int64, prefix string, state int, lastErr string, kind reader.CursorKind) error {
	tail := ""
	if parsedTo > 0 && kind == reader.CursorBytes {
		var err error
		if tail, err = hashRange(ref.Path, parsedTo-sigBytes, parsedTo); err != nil {
			return err
		}
	}
	_, err := in.st.W.Exec(`UPDATE source SET size=?, mtime_ns=?, parsed_to=?, prefix_sig=?, tail_sig=?, reader_ver=?, retention_d=?, state=?, last_error=?, last_seen=?, dev=?, ino=?
		WHERE src_id=?`, ref.Size, ref.MtimeNS, parsedTo, prefix, tail, recall.ReaderVersion, ref.RetentionDays, state, lastErr, in.opts.Now().Unix(),
		int64(ref.Dev), int64(ref.Ino), row.srcID) //nolint:gosec // dev/ino are opaque identifier bits stored as int64 for SQLite, not magnitudes
	if err != nil {
		return fmt.Errorf("recall: update source: %w", err)
	}
	row.size, row.mtimeNS, row.parsedTo, row.prefixSig, row.tailSig, row.state = ref.Size, ref.MtimeNS, parsedTo, prefix, tail, state
	return nil
}

// dropSourceRows deletes everything derived from one source's bytes: its
// messages (and their contentless FTS rowids: this is what
// contentless_delete=1 is for), tool calls and file touches. The session
// row survives; its counters are recomputed from the reparse.
func (in *Ingester) dropSourceRows(srcID int64) error {
	tx, err := in.st.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`DELETE FROM msg_fts WHERE rowid IN (SELECT msg_id FROM msg WHERE src_id=?)`,
		`DELETE FROM msg WHERE src_id=?`,
		`DELETE FROM tool_call WHERE src_id=?`,
		`DELETE FROM file_touch WHERE sess_id IN (SELECT sess_id FROM source WHERE src_id=?)`,
		`UPDATE session SET turns=0, tool_calls=0, errors=0, interrupts=0, compacts=0, in_tok=0, out_tok=0, cache_r=0, cache_w=0, derived_rev=derived_rev+1
		   WHERE sess_id IN (SELECT sess_id FROM source WHERE src_id=?)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s, srcID); err != nil {
			return fmt.Errorf("recall: drop source rows: %w", err)
		}
	}
	return tx.Commit()
}

// markMissing records a vanished source: state=3, its message rows gone
// (the FTS rowids with them), the cursor and tail signature cleared so a
// return under the same path is parsed from byte 0, a tombstone written.
// session, card and artifact rows stay so the hit is still listed,
// labelled as missing.
func (in *Ingester) markMissing(row *ledgerRow) error {
	if err := in.dropSourceRows(row.srcID); err != nil {
		return err
	}
	tx, err := in.st.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := in.opts.Now().Unix()
	if _, err := tx.Exec(`UPDATE source SET state=?, parsed_to=0, tail_sig='', last_error='source file missing', last_seen=? WHERE src_id=?`, recall.SourceMissing, now, row.srcID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO tombstone(src_id, sess_id, reason, at) VALUES (?, ?, 'missing', ?)`, row.srcID, row.sessID, now); err != nil {
		return err
	}
	row.state, row.parsedTo, row.tailSig = recall.SourceMissing, 0, ""
	return tx.Commit()
}

// hashRange returns the hex sha256 of file bytes [from, to), clamped to the
// file; "" when the range is empty.
func hashRange(path string, from, to int64) (string, error) {
	if from < 0 {
		from = 0
	}
	if to <= from {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, to-from)
	n, err := f.ReadAt(buf, from)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	sum := sha256.Sum256(buf[:n])
	return hex.EncodeToString(sum[:]), nil
}

// ---- the transactional sink ---------------------------------------------

type sink struct {
	in       *Ingester
	row      *ledgerRow
	ref      reader.SourceRef
	budget   *reader.Budget
	tx       *sql.Tx
	insMsg   *sql.Stmt
	insFTS   *sql.Stmt
	insCall  *sql.Stmt
	insTouch *sql.Stmt

	sessID   int64
	nativeID string
	seq      int
	messages int
	// checkpoint is the offset every committed row is valid up to: the
	// pass start, then each mid-pass commit.
	checkpoint    int64
	textBytes     int64
	usage         []reader.Usage
	quarantineWhy string
	fatal         error
	sess          sessionAgg
}

// sessionAgg accumulates counters for the session row until commit.
type sessionAgg struct {
	cwd, branch, title, titleSrc, model            string
	firstTS, lastTS                                int64
	turns, toolCalls, errors, interrupts, compacts int64
	inTok, outTok, cacheR, cacheW                  int64
}

func newSink(in *Ingester, row *ledgerRow, ref reader.SourceRef, from int64, b *reader.Budget) *sink {
	return &sink{in: in, row: row, ref: ref, budget: b, checkpoint: from}
}

func (s *sink) begin() error {
	if s.tx != nil {
		return nil
	}
	tx, err := s.in.st.W.Begin()
	if err != nil {
		return err
	}
	s.tx = tx
	if s.insMsg, err = tx.Prepare(`INSERT INTO msg(sess_id, src_id, seq, role, class, ts, rec_off, rec_len, nchars, tool_name, is_error, is_interrupt, body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}
	if s.insFTS, err = tx.Prepare(`INSERT INTO msg_fts(rowid, body) VALUES (?, ?)`); err != nil {
		return err
	}
	if s.insCall, err = tx.Prepare(`INSERT INTO tool_call(sess_id, src_id, name, ts, duration_ms, is_error, arg_digest) VALUES (?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}
	if s.insTouch, err = tx.Prepare(`INSERT INTO file_touch(sess_id, path, op, n, first_ts, last_ts) VALUES (?, ?, ?, 1, ?, ?)
		ON CONFLICT(sess_id, path, op) DO UPDATE SET n=n+1, last_ts=excluded.last_ts`); err != nil {
		return err
	}
	return nil
}

func (s *sink) rollback() error {
	if s.tx == nil {
		return nil
	}
	err := s.tx.Rollback()
	s.tx = nil
	return err
}

// commit hands the usage read since the last checkpoint to the cost sink,
// writes the session aggregates and the checkpoint, then ends the
// transaction; parsedTo is the offset every row so far is valid up to.
// Usage travels with the checkpoint: a pass that fails after one keeps
// the usage of the committed prefix, and the rolled-back tail's usage is
// dropped with its rows and read again by the retry, so each record is
// folded once. A hand-off that fails leaves the batch uncommitted; the
// importer dedups on record uuid, so folding it again is harmless.
func (s *sink) commit(parsedTo int64) error {
	if err := s.in.handOffUsage(s.ref, s); err != nil {
		return err
	}
	if s.tx == nil {
		return nil
	}
	if s.sessID != 0 {
		a := &s.sess
		if _, err := s.tx.Exec(`UPDATE session SET
			cwd = CASE WHEN ?<>'' THEN ? ELSE cwd END,
			project_key = CASE WHEN ?<>'' THEN ? ELSE project_key END,
			branch = CASE WHEN ?<>'' THEN ? ELSE branch END,
			title = CASE WHEN ?<>'' THEN ? ELSE title END,
			title_src = CASE WHEN ?<>'' THEN ? ELSE title_src END,
			model = CASE WHEN ?<>'' THEN ? ELSE model END,
			started_at = CASE WHEN started_at IS NULL OR started_at=0 THEN ? WHEN ?>0 AND ?<started_at THEN ? ELSE started_at END,
			ended_at = CASE WHEN ended_at IS NULL OR ?>ended_at THEN ? ELSE ended_at END,
			turns=turns+?, tool_calls=tool_calls+?, errors=errors+?, interrupts=interrupts+?, compacts=compacts+?,
			in_tok=in_tok+?, out_tok=out_tok+?, cache_r=cache_r+?, cache_w=cache_w+?,
			derived_rev=derived_rev+1
			WHERE sess_id=?`,
			a.cwd, a.cwd, a.cwd, a.cwd, a.branch, a.branch, a.title, a.title, a.titleSrc, a.titleSrc, a.model, a.model,
			a.firstTS, a.firstTS, a.firstTS, a.firstTS, a.lastTS, a.lastTS,
			a.turns, a.toolCalls, a.errors, a.interrupts, a.compacts, a.inTok, a.outTok, a.cacheR, a.cacheW,
			s.sessID); err != nil {
			return fmt.Errorf("recall: update session: %w", err)
		}
		// derived_rev moved, so the artifacts are stale from this commit
		// on: queue the classifiers in the same transaction, never in a
		// later one a cancel could skip.
		if err := enrich.Enqueue(s.tx, s.sessID); err != nil {
			return err
		}
		s.sess = sessionAgg{}
		if _, err := s.tx.Exec(`UPDATE source SET parsed_to=?, sess_id=? WHERE src_id=?`, parsedTo, s.sessID, s.row.srcID); err != nil {
			return err
		}
	}
	err := s.tx.Commit()
	s.tx = nil
	s.textBytes = 0
	if err == nil {
		s.checkpoint = parsedTo
	}
	return err
}

// Session resolves or creates the session row on the first message and
// merges later metadata (titles, model).
func (s *sink) Session(info reader.Session) {
	if s.stop() != nil {
		return
	}
	if info.CWD != "" {
		s.sess.cwd = info.CWD
	}
	if info.Branch != "" {
		s.sess.branch = info.Branch
	}
	if info.Model != "" {
		s.sess.model = info.Model
	}
	if info.Title != "" {
		s.sess.title, s.sess.titleSrc = info.Title, info.TitleSrc
	}
	if s.sessID != 0 {
		return
	}
	if info.NativeID == "" {
		// A title or compact record before any message of this pass: on a
		// resumed source the session is already known, so open it now
		// rather than lose the record when no message follows.
		if s.row.sessID != 0 && s.checkpoint > 0 {
			s.adopt(s.row.sessID)
		}
		return
	}
	if err := s.begin(); err != nil {
		s.fatal = err
		return
	}
	native := info.NativeID
	if s.ref.IsSidechain {
		native = s.ref.NativeID
	}
	s.nativeID = native
	sessID, owner, err := s.resolveSession(native)
	if err != nil {
		s.fatal = err
		return
	}
	if owner != 0 && owner != s.row.srcID {
		// Another live source already owns this conversation: this file is
		// a copy (fork, session-share import, switch-account). Quarantine
		// rather than merge two diverging histories.
		s.quarantineWhy = fmt.Sprintf("copy of source %d (same conversation %s)", owner, native)
		return
	}
	s.adopt(sessID)
	if s.ref.IsSidechain && s.ref.ParentNativeID != "" {
		s.linkParent(sessID)
	}
	if info.ForkOf != "" && info.ForkOf != native {
		s.linkEdge(sessID, info.ForkOf, "fork_of")
	}
}

// adopt makes sessID the pass's session and continues its sequence.
func (s *sink) adopt(sessID int64) {
	if err := s.begin(); err != nil {
		s.fatal = err
		return
	}
	s.sessID = sessID
	var maxSeq sql.NullInt64
	if err := s.tx.QueryRow(`SELECT max(seq) FROM msg WHERE sess_id=?`, sessID).Scan(&maxSeq); err == nil && maxSeq.Valid {
		s.seq = int(maxSeq.Int64)
	}
}

// resolveSession returns the session id for a native id and the src_id of
// the live source that owns it (0 when new or unowned).
func (s *sink) resolveSession(native string) (sessID, ownerSrc int64, err error) {
	if _, err = s.tx.Exec(`INSERT INTO session(host_uid, harness, profile, native_id, is_sidechain, text_tier) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(host_uid, harness, profile, native_id) DO NOTHING`,
		store.LocalHostUID, s.ref.Harness, s.ref.Profile, native, boolInt(s.ref.IsSidechain), s.in.opts.TextTier); err != nil {
		return 0, 0, err
	}
	if err = s.tx.QueryRow(`SELECT sess_id FROM session WHERE host_uid=? AND harness=? AND profile=? AND native_id=?`,
		store.LocalHostUID, s.ref.Harness, s.ref.Profile, native).Scan(&sessID); err != nil {
		return 0, 0, err
	}
	var owner sql.NullInt64
	err = s.tx.QueryRow(`SELECT src_id FROM source WHERE sess_id=? AND src_id<>? AND state NOT IN (?, ?) LIMIT 1`,
		sessID, s.row.srcID, recall.SourceMissing, recall.SourceQuarantined).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	return sessID, owner.Int64, nil
}

// linkParent writes the subagent_of edge to the owning session.
func (s *sink) linkParent(sessID int64) {
	s.linkEdge(sessID, s.ref.ParentNativeID, "subagent_of")
}

// linkEdge writes a conv_edge from sessID to the session with the given
// native id (same harness and profile), creating that session's row if
// its file has not been parsed yet.
func (s *sink) linkEdge(sessID int64, toNative, kind string) {
	var to int64
	err := s.tx.QueryRow(`SELECT sess_id FROM session WHERE host_uid=? AND harness=? AND profile=? AND native_id=?`,
		store.LocalHostUID, s.ref.Harness, s.ref.Profile, toNative).Scan(&to)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := s.tx.Exec(`INSERT INTO session(host_uid, harness, profile, native_id, text_tier) VALUES (?, ?, ?, ?, ?)`,
			store.LocalHostUID, s.ref.Harness, s.ref.Profile, toNative, s.in.opts.TextTier)
		if err != nil {
			return
		}
		to, _ = res.LastInsertId()
	} else if err != nil {
		return
	}
	_, _ = s.tx.Exec(`INSERT OR IGNORE INTO conv_edge(from_sess, to_sess, kind, created_at) VALUES (?, ?, ?, ?)`,
		sessID, to, kind, s.in.opts.Now().Unix())
}

// supersede marks every message before a compaction summary superseded
// and records the boundary as a compacted_into edge on the session
// itself (Codex keeps the compacted thread in the same rollout, so the
// edge is a self-edge whose weight counts the compactions).
func (s *sink) supersede() error {
	if _, err := s.tx.Exec(`UPDATE msg SET superseded=1 WHERE sess_id=? AND superseded=0`, s.sessID); err != nil {
		return err
	}
	_, err := s.tx.Exec(`INSERT INTO conv_edge(from_sess, to_sess, kind, weight, created_at) VALUES (?, ?, 'compacted_into', 1, ?)
		ON CONFLICT(from_sess, to_sess, kind) DO UPDATE SET weight=weight+1`, s.sessID, s.sessID, s.in.opts.Now().Unix())
	return err
}

// stop reports the error that ends the pass, if any.
func (s *sink) stop() error {
	if s.fatal != nil {
		return s.fatal
	}
	if s.quarantineWhy != "" {
		return errQuarantined
	}
	return nil
}

func (s *sink) Msg(m reader.Msg) error {
	if err := s.stop(); err != nil {
		return err
	}
	if s.sessID == 0 {
		return nil // no session yet (a title record before any message)
	}
	if m.IsCompact && strings.TrimSpace(m.Text) == "" {
		// A compaction with no readable summary (every real Codex one):
		// the history before it is superseded and the edge recorded, but
		// no empty row stands in for the summary.
		if m.SupersedesPrior {
			if err := s.supersede(); err != nil {
				return fmt.Errorf("recall: supersede: %w", err)
			}
		}
		return nil
	}
	cls := classify.Message(m.Text, classify.Signals{
		Assistant: m.Role == recall.RoleAssistant, ToolResult: m.IsToolResult, IsError: m.IsError,
		IsMeta: m.IsMeta, CompactSummary: m.IsCompact,
	})
	if classify.Countable(cls) {
		s.sess.turns++
	}
	if m.IsInterrupt {
		s.sess.interrupts++
	}
	if m.TS > 0 {
		if s.sess.firstTS == 0 || m.TS < s.sess.firstTS {
			s.sess.firstTS = m.TS
		}
		if m.TS > s.sess.lastTS {
			s.sess.lastTS = m.TS
		}
	}
	if m.SupersedesPrior {
		if err := s.supersede(); err != nil {
			return fmt.Errorf("recall: supersede: %w", err)
		}
	}
	s.seq++
	body := m.Text
	if s.in.opts.TextTier != TextTierFull {
		body = recall.ClipBytes(body, recall.TextClipBytes)
	}
	res, err := s.insMsg.Exec(s.sessID, s.row.srcID, s.seq, m.Role, int(cls), m.TS, m.RecOff, m.RecLen, len(m.Text),
		strings.Join(m.ToolNames, ","), boolInt(m.IsError), boolInt(m.IsInterrupt), recall.CompressBody([]byte(body)))
	if err != nil {
		return fmt.Errorf("recall: insert msg: %w", err)
	}
	id, _ := res.LastInsertId()
	if _, err := s.insFTS.Exec(id, m.Text); err != nil {
		return fmt.Errorf("recall: insert fts: %w", err)
	}
	s.messages++
	s.textBytes += int64(len(m.Text))
	if s.textBytes >= s.in.opts.BatchBytes {
		// Checkpoint: every row so far is valid up to the end of this record.
		if err := s.commit(m.RecOff + m.RecLen); err != nil {
			return err
		}
		return s.begin()
	}
	return nil
}

func (s *sink) ToolCall(tc reader.ToolCall) error {
	if err := s.stop(); err != nil {
		return err
	}
	if s.sessID == 0 {
		return nil
	}
	if tc.Name != "" {
		s.sess.toolCalls++
	}
	if tc.IsError {
		s.sess.errors++
	}
	if _, err := s.insCall.Exec(s.sessID, s.row.srcID, tc.Name, tc.TS, tc.DurationMS, boolInt(tc.IsError), tc.ArgDigest); err != nil {
		return fmt.Errorf("recall: insert tool_call: %w", err)
	}
	for _, t := range tc.Touches {
		if _, err := s.insTouch.Exec(s.sessID, t.Path, t.Op, tc.TS, tc.TS); err != nil {
			return fmt.Errorf("recall: insert file_touch: %w", err)
		}
	}
	return nil
}

func (s *sink) Usage(u reader.Usage) error {
	if err := s.stop(); err != nil {
		return err
	}
	s.sess.inTok += u.In
	s.sess.outTok += u.Out
	s.sess.cacheR += u.CacheR
	s.sess.cacheW += u.CacheW
	if s.in.opts.Usage != nil {
		s.usage = append(s.usage, u)
	}
	return nil
}

func (s *sink) Count(c reader.Counter, n int64) {
	switch c {
	case reader.CountCompact:
		s.sess.compacts += n
	case reader.CountInterrupt:
		s.sess.interrupts += n
	case reader.CountAPIError:
		s.sess.errors += n
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- cards ---------------------------------------------------------------

// projectCards rewrites the card of every touched session: harness title,
// hints and tags from state.db (the ranking feed; filters join state.db
// live at query time), and the first prompt as preview.
func (in *Ingester) projectCards(touched map[int64]bool) error {
	if len(touched) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	tx, err := in.st.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if err := in.projectCard(tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (in *Ingester) projectCard(tx *sql.Tx, sessID int64) error {
	var profile, harness, native, title string
	if err := tx.QueryRow(`SELECT profile, harness, native_id, COALESCE(title,'') FROM session WHERE sess_id=?`, sessID).Scan(&profile, &harness, &native, &title); err != nil {
		return err
	}
	var hints, tags, deck string
	if in.opts.Registry != nil {
		hints, tags = in.opts.Registry.Hints(profile, harness, native)
		deck = in.opts.Registry.DeckID(profile, harness, native)
	}
	if deck != "" {
		if _, err := tx.Exec(`UPDATE session SET deck_id=? WHERE sess_id=? AND deck_id<>?`, deck, sessID, deck); err != nil {
			return err
		}
	}
	preview := ""
	var body []byte
	err := tx.QueryRow(`SELECT body FROM msg WHERE sess_id=? AND class=? ORDER BY seq LIMIT 1`, sessID, int(classify.Prompt)).Scan(&body)
	if err == nil {
		if text, err := recall.DecompressBody(body); err == nil {
			preview = recall.ClipBytes(strings.TrimSpace(string(text)), previewChars)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO card(sess_id, title, hints, tags, summary, preview) VALUES (?, ?, ?, ?, '', ?)
		ON CONFLICT(sess_id) DO UPDATE SET title=excluded.title, hints=excluded.hints, tags=excluded.tags, preview=excluded.preview`,
		sessID, title, hints, tags, preview); err != nil {
		return err
	}
	// A hint change reaches here without a derived_rev bump (the sink's
	// commit queues content changes), so the classifiers are queued again.
	return enrich.Enqueue(tx, sessID)
}

// RefreshCards reprojects every local card (hints changed in state.db, or
// a rebuild): cheap, about 75 us per card. A card pulled from another
// machine (digest_only=1) is left as imported: it has no messages here to
// project a preview from and nothing for the classifiers to read.
func (in *Ingester) RefreshCards() (int, error) {
	rows, err := in.st.W.Query(`SELECT sess_id FROM session WHERE digest_only=0`)
	if err != nil {
		return 0, err
	}
	touched := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		touched[id] = true
	}
	rows.Close()
	return len(touched), in.projectCards(touched)
}

// ---- gc and rebuild ------------------------------------------------------

// GCResult reports what gc reclaimed.
type GCResult struct {
	TombstonesDropped int   `json:"tombstones_dropped"`
	SourcesDropped    int   `json:"sources_dropped"`
	FreePagesBefore   int64 `json:"free_pages_before"`
	FreePagesAfter    int64 `json:"free_pages_after"`
	BytesBefore       int64 `json:"bytes_before"`
	BytesAfter        int64 `json:"bytes_after"`
}

// GC drops tombstones and missing-source ledger rows older than keep,
// checkpoints the WAL and returns freed pages to the filesystem.
func (in *Ingester) GC(keep time.Duration) (GCResult, error) {
	var r GCResult
	r.BytesBefore = store.FileSize(in.st.Path)
	_ = in.st.W.QueryRow(`PRAGMA freelist_count`).Scan(&r.FreePagesBefore)
	cutoff := in.opts.Now().Add(-keep).Unix()
	res, err := in.st.W.Exec(`DELETE FROM source WHERE state=? AND last_seen<=?`, recall.SourceMissing, cutoff)
	if err != nil {
		return r, err
	}
	n, _ := res.RowsAffected()
	r.SourcesDropped = int(n)
	res, err = in.st.W.Exec(`DELETE FROM tombstone WHERE at<=?`, cutoff)
	if err != nil {
		return r, err
	}
	n, _ = res.RowsAffected()
	r.TombstonesDropped = int(n)
	if _, err := in.st.W.Exec(`INSERT INTO msg_fts(msg_fts) VALUES('optimize')`); err != nil {
		return r, err
	}
	if _, err := in.st.W.Exec(`PRAGMA incremental_vacuum`); err != nil {
		return r, err
	}
	if _, err := in.st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return r, err
	}
	_ = in.st.W.QueryRow(`PRAGMA freelist_count`).Scan(&r.FreePagesAfter)
	r.BytesAfter = store.FileSize(in.st.Path)
	return r, nil
}

// Rebuild empties the database and runs one unbudgeted sweep. Hints and
// links live in state.db, so nothing a human wrote is lost.
func (in *Ingester) Rebuild(ctx context.Context) (Result, error) {
	if err := in.st.Reset(); err != nil {
		return Result{}, err
	}
	return in.Sweep(ctx)
}
