package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Conductor -> human tier (issue #2469). A conductor's news for the human
// lands in a durable per-conductor outbox the bridge polls: urgent items are
// forwarded at once and acked only after the platform accepted them; info
// items wait and leave as one digest at most every [conductor]
// human_digest_minutes, or right after the next urgent message (always as
// their own digest message, so one refused message never blocks another). The same
// file records each digest flush so the window survives a bridge restart.
//
// Layout under <data>/runtime/human-outbox/:
//
//	<conductor>.jsonl      outbox records + digest-flush markers
//	<conductor>.need.json  urgent-line retire counts as of the last delivered
//	                       reply, plus the pending (sent, unacked) one; replaces
//	                       the bridge's in-memory filter_need_lines counters
//	<conductor>.lock       cross-process lock (notify, bridge ack, tier-filter)

// HumanOutboxRecord is one item for the human. Tier is "urgent" or "info";
// a record with Tier "digest" is a flush marker and never listed.
type HumanOutboxRecord struct {
	ID       string    `json:"id"`
	TS       time.Time `json:"ts"`
	Tier     string    `json:"tier"`
	Text     string    `json:"text,omitempty"`
	TextHash string    `json:"th,omitempty"`
	Acked    bool      `json:"acked"`
}

const (
	// MaxHumanOutboxTextBytes caps one outbox item's text.
	MaxHumanOutboxTextBytes = 4000
	// NeedRetireCyclesDefault is the default [conductor] need_retire_cycles.
	NeedRetireCyclesDefault = 3
	// humanOutboxDedupWindow: the same text twice within it is one record.
	humanOutboxDedupWindow = 24 * time.Hour
	humanDigestMarkerTier  = "digest"
	// Retention for news nobody delivered (no bridge, no channel, a refused
	// send): an unacked item older than this is pruned, and at most
	// humanOutboxMaxItems records are kept per conductor, so a deck-only
	// conductor that calls notify from every turn cannot grow the file (or
	// the per-append dedup read) without bound.
	humanOutboxUnackedRetention = 72 * time.Hour
	humanOutboxMaxItems         = 200
	// The outbox holds what a conductor tells the human: owner-only.
	humanOutboxDirMode  = 0o700
	humanOutboxFileMode = 0o600
)

var humanOutboxMu sync.Mutex

// HumanOutboxDir is the outbox root; a data-path failure degrades to temp.
func HumanOutboxDir() string {
	dir, err := runtimeDataPath("human-outbox")
	if err != nil {
		return tempAgentDeckPath("runtime", "human-outbox")
	}
	return dir
}

// HumanOutboxPath is the outbox file for one conductor.
func HumanOutboxPath(conductor string) string {
	return filepath.Join(HumanOutboxDir(), sanitizeInboxName(conductor)+".jsonl")
}

func humanNeedLedgerPath(conductor string) string {
	return filepath.Join(HumanOutboxDir(), sanitizeInboxName(conductor)+".need.json")
}

// withHumanOutboxLock serializes fn against other goroutines and processes
// (the conductor's notify, the bridge's ack and tier-filter calls).
func withHumanOutboxLock(conductor string, fn func() error) error {
	if strings.TrimSpace(conductor) == "" {
		return errors.New("human outbox: empty conductor name")
	}
	humanOutboxMu.Lock()
	defer humanOutboxMu.Unlock()
	dir := HumanOutboxDir()
	if err := os.MkdirAll(dir, humanOutboxDirMode); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, sanitizeInboxName(conductor)+".lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, humanOutboxFileMode) // #nosec G304 -- sanitized name under the data dir
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock human outbox: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func readHumanOutboxLocked(conductor string) ([]HumanOutboxRecord, error) {
	f, err := os.Open(HumanOutboxPath(conductor))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []HumanOutboxRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		var r HumanOutboxRecord
		if len(line) == 0 || json.Unmarshal(line, &r) != nil {
			continue // a torn line is skipped, never fatal
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// AppendHumanOutbox queues text for the human. The same text (by hash) within
// 24 h returns the existing record with created=false instead of a second one,
// except that urgent never dedups into info: a pending info record with that
// text is upgraded to urgent in place, and one already delivered (as part of
// a digest) gets a new urgent record. Both return created=true.
func AppendHumanOutbox(conductor, tier, text string) (rec HumanOutboxRecord, created bool, err error) {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier != TurnTierUrgent && tier != TurnTierInfo {
		return rec, false, fmt.Errorf("human outbox: tier must be %q or %q, got %q", TurnTierUrgent, TurnTierInfo, tier)
	}
	text = capTextBytes(strings.TrimSpace(text), MaxHumanOutboxTextBytes)
	if text == "" {
		return rec, false, errors.New("human outbox: empty text")
	}
	err = withHumanOutboxLock(conductor, func() error {
		var lerr error
		rec, created, lerr = appendHumanOutboxLocked(conductor, tier, text, time.Now())
		return lerr
	})
	return rec, created, err
}

func appendHumanOutboxLocked(conductor, tier, text string, now time.Time) (HumanOutboxRecord, bool, error) {
	th := turnTextHash(text)
	existing, err := readHumanOutboxLocked(conductor)
	if err != nil {
		return HumanOutboxRecord{}, false, err
	}
	for i := len(existing) - 1; i >= 0; i-- {
		r := existing[i]
		if r.TextHash != th || now.Sub(r.TS) >= humanOutboxDedupWindow {
			continue
		}
		if tier != TurnTierUrgent || r.Tier != TurnTierInfo {
			return r, false, nil
		}
		if r.Acked {
			break // delivered only as info: queue it again as urgent
		}
		existing[i].Tier = TurnTierUrgent
		return existing[i], true, rewriteHumanOutboxLocked(conductor, existing, lastHumanDigestFlush(existing), now)
	}
	rec := HumanOutboxRecord{ID: GenerateID(), TS: now, Tier: tier, Text: text, TextHash: th}
	all := append(existing, rec)
	if kept, dropped := pruneHumanOutbox(all, now); dropped > 0 {
		return rec, true, rewriteHumanOutboxLocked(conductor, kept, lastHumanDigestFlush(existing), now)
	}
	return rec, true, appendJSONLine(HumanOutboxPath(conductor), rec)
}

// pruneHumanOutbox returns the item records (never digest markers) worth
// keeping at now, oldest first, and how many items it dropped: acked items
// past the dedup window, unacked ones past humanOutboxUnackedRetention, and,
// beyond humanOutboxMaxItems, the oldest acked items first, then the oldest
// info, then the oldest urgent.
func pruneHumanOutbox(all []HumanOutboxRecord, now time.Time) (kept []HumanOutboxRecord, dropped int) {
	for _, r := range all {
		if r.Tier == humanDigestMarkerTier {
			continue
		}
		age := now.Sub(r.TS)
		if (r.Acked && age >= humanOutboxDedupWindow) || (!r.Acked && age >= humanOutboxUnackedRetention) {
			dropped++
			continue
		}
		kept = append(kept, r)
	}
	for _, drop := range []func(HumanOutboxRecord) bool{
		func(r HumanOutboxRecord) bool { return r.Acked },
		func(r HumanOutboxRecord) bool { return r.Tier != TurnTierUrgent },
		func(HumanOutboxRecord) bool { return true },
	} {
		excess := len(kept) - humanOutboxMaxItems
		if excess <= 0 {
			break
		}
		next := kept[:0:0]
		for _, r := range kept {
			if excess > 0 && drop(r) {
				excess--
				dropped++
				continue
			}
			next = append(next, r)
		}
		kept = next
	}
	return kept, dropped
}

// appendJSONLine appends one JSON line with O_APPEND + fsync.
func appendJSONLine(path string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, humanOutboxFileMode) // #nosec G304 -- sanitized name under the data dir
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := fsyncFile(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ListHumanOutbox returns the conductor's items oldest first, only the
// unacked ones when unackedOnly. Digest markers are never listed.
func ListHumanOutbox(conductor string, unackedOnly bool) ([]HumanOutboxRecord, error) {
	var out []HumanOutboxRecord
	err := withHumanOutboxLock(conductor, func() error {
		all, err := readHumanOutboxLocked(conductor)
		out = filterHumanOutbox(all, unackedOnly)
		return err
	})
	return out, err
}

func filterHumanOutbox(all []HumanOutboxRecord, unackedOnly bool) []HumanOutboxRecord {
	out := []HumanOutboxRecord{}
	for _, r := range all {
		if r.Tier == humanDigestMarkerTier || (unackedOnly && r.Acked) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// AckHumanOutbox marks ids delivered and returns how many were newly acked;
// acking an id twice (or an unknown id) is a no-op. Acking any info item
// records a digest flush, which restarts the human_digest_minutes window.
// The rewrite also prunes (see pruneHumanOutbox).
func AckHumanOutbox(conductor string, ids []string) (int, error) {
	want := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return 0, nil
	}
	acked := 0
	err := withHumanOutboxLock(conductor, func() error {
		all, err := readHumanOutboxLocked(conductor)
		if err != nil {
			return err
		}
		now := time.Now()
		flushed := false
		for i := range all {
			if want[all[i].ID] && !all[i].Acked && all[i].Tier != humanDigestMarkerTier {
				all[i].Acked = true
				acked++
				flushed = flushed || all[i].Tier == TurnTierInfo
			}
		}
		if acked == 0 {
			return nil
		}
		lastFlush := lastHumanDigestFlush(all)
		if flushed {
			lastFlush = now
		}
		return rewriteHumanOutboxLocked(conductor, all, lastFlush, now)
	})
	return acked, err
}

// rewriteHumanOutboxLocked replaces the outbox file with all, pruned as in
// pruneHumanOutbox, and one digest-flush marker.
func rewriteHumanOutboxLocked(conductor string, all []HumanOutboxRecord, lastFlush, now time.Time) error {
	var buf bytes.Buffer
	kept, _ := pruneHumanOutbox(all, now)
	for _, r := range kept {
		writeJSONLine(&buf, r)
	}
	if !lastFlush.IsZero() {
		writeJSONLine(&buf, HumanOutboxRecord{ID: "digest", TS: lastFlush, Tier: humanDigestMarkerTier, Acked: true})
	}
	return writeFileDurable(HumanOutboxPath(conductor), buf.Bytes(), humanOutboxFileMode)
}

func writeJSONLine(buf *bytes.Buffer, v any) {
	if line, err := json.Marshal(v); err == nil {
		buf.Write(line)
		buf.WriteByte('\n')
	}
}

func lastHumanDigestFlush(all []HumanOutboxRecord) time.Time {
	var last time.Time
	for _, r := range all {
		if r.Tier == humanDigestMarkerTier && r.TS.After(last) {
			last = r.TS
		}
	}
	return last
}

// HumanDigest reports whether the unacked info items are due as one digest
// at now: always when they can ride an urgent send (withUrgent), else once
// windowMinutes have passed since the last flush (or, before any flush, since
// the oldest pending item was queued). Returns the items either way.
func HumanDigest(conductor string, now time.Time, windowMinutes int, withUrgent bool) (due bool, items []HumanOutboxRecord, err error) {
	err = withHumanOutboxLock(conductor, func() error {
		all, rerr := readHumanOutboxLocked(conductor)
		if rerr != nil {
			return rerr
		}
		for _, r := range filterHumanOutbox(all, true) {
			if r.Tier == TurnTierInfo {
				items = append(items, r)
			}
		}
		if len(items) == 0 {
			return nil
		}
		since := lastHumanDigestFlush(all)
		if since.IsZero() {
			since = items[0].TS
		}
		due = withUrgent || now.Sub(since) >= time.Duration(windowMinutes)*time.Minute
		return nil
	})
	return due, items, err
}

// humanLineTier classifies one reply line: urgent (NEED:, [urgent], URGENT:),
// info ([info], INFO:) or "" (status and prose, never forwarded). Info text is
// returned without its marker.
func humanLineTier(line string) (tier, text string) {
	if strings.HasPrefix(line, "NEED:") {
		return TurnTierUrgent, line
	}
	upper := strings.ToUpper(line)
	for _, p := range []string{"[URGENT]", "URGENT:"} {
		if strings.HasPrefix(upper, p) {
			return TurnTierUrgent, line
		}
	}
	for _, p := range []string{"[INFO]", "INFO:"} {
		if strings.HasPrefix(upper, p) {
			return TurnTierInfo, strings.TrimSpace(line[len(p):])
		}
	}
	return "", ""
}

// TierFilter applies the human tier rules to one conductor reply (a heartbeat
// or any other turn the bridge reads). Urgent lines are returned in sendNow,
// subject to the retire rule: forwarded on cycles 1..N-1, replaced once on
// cycle N ([conductor] need_retire_cycles, default 3) by "STILL BLOCKED (N
// cycles, no reply): <line>", then dropped; a line absent from a reply resets
// its count. Counts persist in <conductor>.need.json, so a bridge restart
// does not re-alert. Info lines are queued to the outbox (queued counts new
// records; a repeat within 24 h is deduplicated). A [STATUS]-only reply sends
// and queues nothing. An empty reply (the bridge asking only whether the
// digest is due) leaves the retire counts untouched.
func TierFilter(conductor, reply string, now time.Time) (sendNow []string, queued int, err error) {
	return TierFilterReply(conductor, "", reply, now)
}

// TierFilterReply is TierFilter for a caller that delivers the result
// itself (the bridge). With a non-empty replyID the computed counts stay
// pending until AckTierFilterReply(replyID) confirms the platform accepted
// the message, so a failed send, of this reply or of any later one, never
// advances the retire count: every reply is counted from the counts of the
// last delivered one. A reply with nothing to send needs no delivery and
// commits at once (this is how a vanished line resets its count). Re-filtering
// the last delivered reply (a bridge that restarted before saving its own
// state) recomputes from the counts that reply started with. An empty replyID
// commits at once, like TierFilter.
func TierFilterReply(conductor, replyID, reply string, now time.Time) (sendNow []string, queued int, err error) {
	if strings.TrimSpace(reply) == "" {
		return nil, 0, nil
	}
	settings := GetConductorSettings()
	threshold := settings.GetNeedRetireCycles()
	err = withHumanOutboxLock(conductor, func() error {
		ledger := loadHumanNeedLedger(conductor)
		prev := ledger.Counts
		if replyID != "" && replyID == ledger.ReplyID {
			prev = ledger.Base // the last delivered reply again: not a new cycle
		}
		counts := map[string]int{}
		for _, raw := range strings.Split(reply, "\n") {
			line := strings.TrimSpace(raw)
			tier, text := humanLineTier(line)
			switch {
			case tier == TurnTierInfo && text != "":
				_, created, aerr := appendHumanOutboxLocked(conductor, TurnTierInfo, capTextBytes(text, MaxHumanOutboxTextBytes), now)
				if aerr != nil {
					return aerr
				}
				if created {
					queued++
				}
			case tier == TurnTierUrgent:
				if _, seen := counts[line]; seen {
					continue // the same line twice in one reply is one alert
				}
				n := prev[line] + 1
				counts[line] = n
				switch {
				case n < threshold:
					sendNow = append(sendNow, line)
				case n == threshold:
					sendNow = append(sendNow, fmt.Sprintf("STILL BLOCKED (%d cycles, no reply): %s", threshold, line))
				}
			}
		}
		if replyID == "" || len(sendNow) == 0 {
			ledger.commit(replyID, prev, counts)
		} else {
			ledger.Pending = &humanNeedPending{ReplyID: replyID, Base: prev, Counts: counts}
		}
		ledger.UpdatedAt = now
		return saveHumanNeedLedger(conductor, ledger)
	})
	return sendNow, queued, err
}

// AckTierFilterReply confirms that the reply TierFilterReply computed under
// replyID reached the human, committing its retire counts. It reports whether
// this call committed them: an ack for any other id (a reply superseded by a
// later one, or already acked) is a no-op.
func AckTierFilterReply(conductor, replyID string, now time.Time) (committed bool, err error) {
	replyID = strings.TrimSpace(replyID)
	if replyID == "" {
		return false, errors.New("tier-filter ack: empty reply id")
	}
	err = withHumanOutboxLock(conductor, func() error {
		ledger := loadHumanNeedLedger(conductor)
		p := ledger.Pending
		if p == nil || p.ReplyID != replyID {
			return nil
		}
		ledger.commit(p.ReplyID, p.Base, p.Counts)
		ledger.UpdatedAt = now
		committed = true
		return saveHumanNeedLedger(conductor, ledger)
	})
	return committed, err
}

// humanNeedLedger is <conductor>.need.json: the retire counts as of the last
// delivered reply, that reply's id and the counts it started from (Base, so
// re-filtering it recomputes), and the newest computed but not yet delivered
// reply (Pending).
type humanNeedLedger struct {
	Counts    map[string]int    `json:"counts"`
	ReplyID   string            `json:"reply_id,omitempty"`
	Base      map[string]int    `json:"base,omitempty"`
	Pending   *humanNeedPending `json:"pending,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// humanNeedPending is a filtered reply awaiting AckTierFilterReply.
type humanNeedPending struct {
	ReplyID string         `json:"reply_id"`
	Base    map[string]int `json:"base,omitempty"`
	Counts  map[string]int `json:"counts"`
}

// commit makes counts the delivered state and drops any pending reply: a
// later reply supersedes an undelivered earlier one.
func (l *humanNeedLedger) commit(replyID string, base, counts map[string]int) {
	l.Counts, l.ReplyID, l.Base, l.Pending = counts, replyID, nil, nil
	if replyID != "" {
		l.Base = base
	}
}

func saveHumanNeedLedger(conductor string, ledger humanNeedLedger) error {
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	return writeFileDurable(humanNeedLedgerPath(conductor), data, humanOutboxFileMode)
}

func loadHumanNeedLedger(conductor string) humanNeedLedger {
	var ledger humanNeedLedger
	data, err := os.ReadFile(humanNeedLedgerPath(conductor))
	if err != nil || json.Unmarshal(data, &ledger) != nil {
		ledger = humanNeedLedger{} // missing or corrupt: start fresh
	}
	if ledger.Counts == nil {
		ledger.Counts = map[string]int{}
	}
	if ledger.Base == nil {
		ledger.Base = map[string]int{}
	}
	return ledger
}
