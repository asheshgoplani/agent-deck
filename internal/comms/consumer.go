package comms

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Consumer side of the ledger (P2, docs/comms.md "Reading by consumer").
//
// A consumer is whoever records are addressed to: a session id in a
// record's To, or a named reader such as human:<conductor>. Its state is one
// file, <ledger>/cursors/<consumer>.json, holding the ConsumerState of the
// P0 contract (contiguous watermark plus sparse acknowledgements, bound to
// the ledger store and epoch) and the bookkeeping of the last pass. Every
// read-modify-write of that file happens under <consumer>.lock (flock), so
// the prompt hook, the Stop hook, `msg read|ack` and the daemon never lose
// each other's acknowledgements. The state files are not the ledger: the
// daemon stays its only writer.
//
// Pending flags: the daemon keeps <ledger>/pending/<consumer>.json, the
// first and newest cursor of a deliverable record addressed to the
// consumer, so a hook with nothing pending stats one small file and never
// opens the log. The flag is a cache: the daemon rewrites it at every
// commit and rebuilds it from its dedup window at open, and a reader that
// sees it trusts it only to skip work, never to skip a record (a pass reads
// the log from its watermark).

const (
	cursorsDirName  = "cursors"
	pendingDirName  = "pending"
	consumerLockFor = 5 * time.Second
	// maxGapsKept bounds the gap history a consumer file carries.
	maxGapsKept = 8
	// activeConsumerFor is how recently a consumer must have read for its
	// watermark to hold compaction (RetainFrom). A reader gone for longer is
	// no longer protected: audit retention applies and its return is an
	// explicit Gap, never a silent restart.
	activeConsumerFor = DefaultRetentionDays * 24 * time.Hour
)

var consumerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,199}$`)

// ValidConsumer checks a consumer name before it becomes a file name: one
// path element of letters, digits and ._:@- (session ids, human:<name>).
func ValidConsumer(name string) error {
	if !consumerNameRE.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("comms: invalid consumer name %q", name)
	}
	return nil
}

// Deliverable reports whether r is a message for consumer: a record of a
// kind a consumer reads, addressed to it, that is not noise. Measurement
// records (wake, call) are never delivered. A send record's first recipient
// is its target, which the send's own transport already reached; only the
// observers after it (a parent following its children's exchange) read it
// from the ledger.
func Deliverable(r Record, consumer string) bool {
	if r.Tier == TierNoise {
		return false
	}
	to := r.To
	switch r.Kind {
	case KindTurn, KindStatus, KindDelivery, KindHuman, KindError:
	case KindSend:
		if len(to) > 0 {
			to = to[1:]
		}
	default:
		return false
	}
	for _, t := range to {
		if t == consumer {
			return true
		}
	}
	return false
}

// ConsumerFile is the on-disk state of one consumer.
type ConsumerFile struct {
	ConsumerState
	Created int64 `json:"created,omitempty"` // Unix ms
	Updated int64 `json:"updated,omitempty"` // Unix ms of the last pass
	// Through is the last ledger cursor the last pass covered; Pending how
	// many deliverable records it left unacknowledged. Together with the
	// pending flag they let a caller skip a pass that would find nothing.
	Through events.Cursor `json:"through,omitempty"`
	Pending int           `json:"pending"`
	// Gaps are the explicit losses this consumer was told about (newest
	// last, bounded): records compacted before it read them, or a ledger
	// reset or restore that invalidated its position.
	Gaps []GapNote `json:"gaps,omitempty"`
}

// GapNote is one recorded loss: records From..To (cursors of the epoch the
// consumer was reading) it will never be shown, and where it resumed.
type GapNote struct {
	Gap
	Reason  string        `json:"reason"`  // compacted | epoch
	Resumed events.Cursor `json:"resumed"` // the watermark the consumer resumed at
	At      int64         `json:"at"`      // Unix ms
}

// PendingFlag is the daemon's per-consumer hint: the first and the newest
// cursor of a deliverable record addressed to the consumer, in Epoch.
type PendingFlag struct {
	First events.Cursor `json:"first"`
	Last  events.Cursor `json:"last"`
	Epoch int64         `json:"epoch"`
}

// ConsumerPath is <dir>/cursors/<consumer>.json.
func ConsumerPath(dir, consumer string) string {
	return filepath.Join(dir, cursorsDirName, consumer+".json")
}

// FlagPath is <dir>/pending/<consumer>.json.
func FlagPath(dir, consumer string) string {
	return filepath.Join(dir, pendingDirName, consumer+".json")
}

// ReadFlag returns the consumer's pending flag, found false when the daemon
// never addressed it a record.
func ReadFlag(dir, consumer string) (PendingFlag, bool) {
	if ValidConsumer(consumer) != nil {
		return PendingFlag{}, false
	}
	data, err := os.ReadFile(FlagPath(dir, consumer)) // #nosec G304 -- validated name under the ledger dir
	if err != nil {
		return PendingFlag{}, false
	}
	var f PendingFlag
	if json.Unmarshal(data, &f) != nil {
		return PendingFlag{}, false
	}
	return f, true
}

// writeFlag replaces a flag (tmp + rename, no fsync: it is a cache rebuilt
// at open).
func writeFlag(dir, consumer string, f PendingFlag) error {
	path := FlagPath(dir, consumer)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadConsumer returns a consumer's state file without locking (listings,
// stats, RetainFrom). found is false when the consumer never read.
func ReadConsumer(dir, consumer string) (ConsumerFile, bool, error) {
	if err := ValidConsumer(consumer); err != nil {
		return ConsumerFile{}, false, err
	}
	data, err := os.ReadFile(ConsumerPath(dir, consumer)) // #nosec G304 -- validated name under the ledger dir
	if errors.Is(err, os.ErrNotExist) {
		return ConsumerFile{}, false, nil
	}
	if err != nil {
		return ConsumerFile{}, false, err
	}
	var f ConsumerFile
	if err := json.Unmarshal(data, &f); err != nil {
		return ConsumerFile{}, false, fmt.Errorf("comms: consumer state %s: %w", consumer, err)
	}
	f.Normalize()
	return f, true, nil
}

// ListConsumers returns the names of every consumer with a state file.
func ListConsumers(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, cursorsDirName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		name = name[:len(name)-len(".json")]
		if ValidConsumer(name) == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ConsumersRetainFrom is the pending-delivery bound for compaction: one
// above the lowest watermark of every consumer that read within
// activeConsumerFor (RetainFrom of the P0 contract), 0 when none holds
// anything.
func ConsumersRetainFrom(dir string, now time.Time) events.Cursor {
	names, err := ListConsumers(dir)
	if err != nil {
		return 0
	}
	var states []ConsumerState
	for _, name := range names {
		f, ok, err := ReadConsumer(dir, name)
		if err != nil || !ok {
			continue
		}
		if f.Updated > 0 && now.Sub(time.UnixMilli(f.Updated)) > activeConsumerFor {
			continue
		}
		states = append(states, f.ConsumerState)
	}
	return RetainFrom(states)
}

// ReadStore reads <dir>/store.json without creating it (readers).
func ReadStore(dir string) (StoreIdentity, error) {
	data, err := os.ReadFile(filepath.Join(dir, storeFileName)) // #nosec G304 -- ledger dir under the data dir
	if err != nil {
		return StoreIdentity{}, err
	}
	var id StoreIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return StoreIdentity{}, err
	}
	return id, nil
}

// Reader is a consumer-side handle on one profile's ledger: the read-only
// bus plus the directory its state files live in.
type Reader struct {
	Dir   string
	Bus   *events.Bus
	Store StoreIdentity
	Now   func() time.Time
}

// OpenReaderAt opens a consumer handle on the ledger at dir.
func OpenReaderAt(dir string) (*Reader, error) {
	bus, err := OpenReaderDir(dir)
	if err != nil {
		return nil, err
	}
	store, err := ReadStore(dir)
	if err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("comms: ledger store identity: %w", err)
	}
	return &Reader{Dir: dir, Bus: bus, Store: store, Now: time.Now}, nil
}

// Close releases the bus.
func (r *Reader) Close() error {
	if r == nil || r.Bus == nil {
		return nil
	}
	return r.Bus.Close()
}

func (r *Reader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Pass is what one consumer pass found.
type Pass struct {
	Consumer string
	// Pending is every deliverable, unacknowledged record above the
	// watermark, oldest first.
	Pending []Exported
	// Through is the last cursor the pass covered.
	Through events.Cursor
	// Gap is set when this pass discovered a loss (compaction or an epoch
	// change); it is also kept in the consumer file.
	Gap *GapNote
}

// HasNothingPending is the fast path a hook takes before opening the log:
// true when the daemon's flag shows no record newer than what the
// consumer's last pass covered and that pass left nothing pending. A
// missing flag means nothing was ever addressed to the consumer. Any doubt
// (no state yet, an unreadable file) answers false.
func HasNothingPending(dir, consumer string) bool {
	flag, ok := ReadFlag(dir, consumer)
	if !ok {
		return true
	}
	f, found, err := ReadConsumer(dir, consumer)
	if err != nil || !found {
		return false
	}
	return f.Pending == 0 && f.Through >= flag.Last && f.Epoch == flag.Epoch
}

// Do runs one consumer pass under the consumer's lock: it loads (or
// creates) the state, reads every record above the watermark, moves the
// watermark over cursors that are not this consumer's news (spent, not
// addressed to it, noise, measurement records, already acknowledged), and
// hands the pending records to decide, which returns the cursors to
// acknowledge. The state is saved after decide returns without error, so a
// pass whose output could not be shown acknowledges nothing.
//
// A new consumer starts just before the first record ever addressed to it
// (its pending flag) or, if none was, at the end of the log. A state from
// another ledger store or epoch is rebuilt the same way and the reset is
// recorded as a gap; so is a watermark that compaction overtook.
func (r *Reader) Do(consumer string, decide func(p Pass) ([]events.Cursor, error)) (ConsumerFile, error) {
	if err := ValidConsumer(consumer); err != nil {
		return ConsumerFile{}, err
	}
	unlock, err := lockConsumer(r.Dir, consumer)
	if err != nil {
		return ConsumerFile{}, err
	}
	defer unlock()

	now := r.now()
	f, found, err := ReadConsumer(r.Dir, consumer)
	if err != nil {
		return f, err
	}
	end, _, err := r.Bus.Ends()
	if err != nil {
		return f, err
	}
	pass := Pass{Consumer: consumer}
	switch {
	case !found:
		f = ConsumerFile{ConsumerState: ConsumerState{Consumer: consumer, Generation: 1}, Created: now.UnixMilli()}
		f.bind(r, end)
	case f.Check(r.Store) != nil:
		// The ledger was reset or restored under this consumer: its old
		// position means nothing in the new epoch. Rebuilt, and said so.
		note := GapNote{Gap: Gap{Consumer: consumer, From: f.Watermark + 1, To: f.Through}, Reason: "epoch", At: now.UnixMilli()}
		f.ConsumerState = ConsumerState{Consumer: consumer, Generation: f.Generation + 1}
		f.bind(r, end)
		// What the restored copy holds may or may not have been shown
		// before the reset; only the new epoch's records are certainly news.
		if start := events.Cursor(r.Store.EpochStart); f.Watermark < start && start <= end {
			f.Watermark = start
		}
		note.Resumed = f.Watermark
		f.addGap(note)
		pass.Gap = &note
	}

	// Compaction may have overtaken the watermark (a reader idle longer
	// than the audit retention): say what was lost, then read on.
	read, through, err := []Exported(nil), events.Cursor(0), error(nil)
	for attempt := 0; attempt < 2; attempt++ {
		oldest, oerr := r.Bus.Oldest()
		if oerr != nil {
			return f, oerr
		}
		if gap, lost := GapFor(f.ConsumerState, oldest); lost {
			f.Watermark = oldest - 1
			f.Normalize()
			note := GapNote{Gap: gap, Reason: "compacted", Resumed: f.Watermark, At: now.UnixMilli()}
			f.addGap(note)
			pass.Gap = &note
		}
		read, through, err = Export(r.Bus, f.Watermark, 0)
		if !errors.Is(err, events.ErrCursorTooOld) {
			break // compaction racing this pass: check the bound again
		}
	}
	if err != nil {
		return f, err
	}
	deliverable := func(rec Record) bool { return Deliverable(rec, consumer) }
	after := f.Watermark
	f.AdvanceOver(after, read, through, deliverable)
	for _, e := range read {
		if deliverable(e.Record) && !f.IsAcked(e.Cursor) {
			pass.Pending = append(pass.Pending, e)
		}
	}
	pass.Through = through

	acks, err := decide(pass)
	if err != nil {
		return f, err
	}
	acked := map[events.Cursor]bool{}
	for _, c := range acks {
		if err := f.Ack(c); err != nil {
			return f, err
		}
		acked[c] = true
	}
	// Acknowledging the oldest pending records may have freed the prefix.
	f.AdvanceOver(after, read, through, deliverable)
	left := 0
	for _, e := range pass.Pending {
		if !acked[e.Cursor] && !f.IsAcked(e.Cursor) {
			left++
		}
	}
	f.Through, f.Pending, f.Updated = through, left, now.UnixMilli()
	return f, writeConsumer(r.Dir, f)
}

// bind starts a fresh state on this ledger: the store, epoch and a
// watermark just before the first record ever addressed to the consumer in
// this epoch, else the end of the log.
func (f *ConsumerFile) bind(r *Reader, end events.Cursor) {
	f.Store, f.Epoch = r.Store.ID, r.Store.Epoch
	f.Watermark = end
	if flag, ok := ReadFlag(r.Dir, f.Consumer); ok && flag.Epoch == r.Store.Epoch && flag.First > 0 && flag.First-1 < end {
		f.Watermark = flag.First - 1
	}
	f.Acked = nil
}

func (f *ConsumerFile) addGap(g GapNote) {
	f.Gaps = append(f.Gaps, g)
	if len(f.Gaps) > maxGapsKept {
		f.Gaps = f.Gaps[len(f.Gaps)-maxGapsKept:]
	}
}

// AdvanceOver moves the watermark over every cursor up to through that is
// not a pending record of this consumer: spent cursors (no record), records
// not deliverable to it, and acknowledged ones. read must be every record
// one Export pass returned for (after, through]; a pass that started above
// the watermark says nothing about the cursors below its start, so it
// changes nothing. SkipSpent is the case where every record is news.
func (c *ConsumerState) AdvanceOver(after events.Cursor, read []Exported, through events.Cursor, deliverable func(Record) bool) {
	if after > c.Watermark {
		return
	}
	pending := make(map[events.Cursor]bool, len(read))
	for _, e := range read {
		if deliverable == nil || deliverable(e.Record) {
			pending[e.Cursor] = true
		}
	}
	for c.Watermark < through {
		next := c.Watermark + 1
		if pending[next] && !c.IsAcked(next) {
			break
		}
		c.Watermark = next
	}
	c.normalize()
}

// writeConsumer persists a consumer file durably (tmp, fsync, rename).
func writeConsumer(dir string, f ConsumerFile) error {
	path := ConsumerPath(dir, f.Consumer)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrConsumerBusy is returned when another process holds the consumer's
// lock for longer than consumerLockFor.
var ErrConsumerBusy = errors.New("comms: consumer state is locked by another reader")

// lockConsumer takes <dir>/cursors/<consumer>.lock exclusively, waiting up
// to consumerLockFor.
func lockConsumer(dir, consumer string) (func(), error) {
	path := filepath.Join(dir, cursorsDirName, consumer+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- validated name under the ledger dir
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(consumerLockFor)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, ErrConsumerBusy
			}
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
