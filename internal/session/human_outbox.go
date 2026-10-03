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
// human_digest_minutes, or ride along with the next urgent message. The same
// file records each digest flush so the window survives a bridge restart.
//
// Layout under <data>/runtime/human-outbox/:
//
//	<conductor>.jsonl      outbox records + digest-flush markers
//	<conductor>.need.json  urgent-line retire counts (replaces the bridge's
//	                       in-memory filter_need_lines counters)
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, sanitizeInboxName(conductor)+".lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- sanitized name under the data dir
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
	return rec, true, appendJSONLine(HumanOutboxPath(conductor), rec)
}

// appendJSONLine appends one JSON line with O_APPEND + fsync.
func appendJSONLine(path string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) // #nosec G304 -- sanitized name under the data dir
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
// The rewrite also prunes acked items older than the dedup window.
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

// rewriteHumanOutboxLocked replaces the outbox file with all, dropping acked
// items older than the dedup window and keeping one digest-flush marker.
func rewriteHumanOutboxLocked(conductor string, all []HumanOutboxRecord, lastFlush, now time.Time) error {
	var buf bytes.Buffer
	for _, r := range all {
		if r.Tier == humanDigestMarkerTier || (r.Acked && now.Sub(r.TS) >= humanOutboxDedupWindow) {
			continue
		}
		writeJSONLine(&buf, r)
	}
	if !lastFlush.IsZero() {
		writeJSONLine(&buf, HumanOutboxRecord{ID: "digest", TS: lastFlush, Tier: humanDigestMarkerTier, Acked: true})
	}
	return writeFileDurable(HumanOutboxPath(conductor), buf.Bytes(), 0o644)
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

// TierFilterReply is TierFilter for a caller that may retry the same reply
// (the bridge's OS-heartbeat scan re-sends a reply whose delivery failed).
// A non-empty replyID equal to the one the ledger last saw recomputes from the
// counts that call started with, so N failed sends of one reply are still one
// retire cycle; an empty replyID makes every call a new cycle (heartbeats).
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
			prev = ledger.Base // a retry of the same reply: not a new cycle
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
				if n < threshold {
					sendNow = append(sendNow, line)
				} else if n == threshold {
					sendNow = append(sendNow, fmt.Sprintf("STILL BLOCKED (%d cycles, no reply): %s", threshold, line))
				}
			}
		}
		next := humanNeedLedger{Counts: counts, UpdatedAt: now}
		if replyID != "" {
			next.ReplyID, next.Base = replyID, prev
		}
		data, merr := json.Marshal(next)
		if merr != nil {
			return merr
		}
		return writeFileDurable(humanNeedLedgerPath(conductor), data, 0o644)
	})
	return sendNow, queued, err
}

// humanNeedLedger is <conductor>.need.json: the retire counts after the last
// reply, plus that reply's id and the counts it started from (Base) so a retry
// of the same reply recomputes instead of advancing.
type humanNeedLedger struct {
	Counts    map[string]int `json:"counts"`
	ReplyID   string         `json:"reply_id,omitempty"`
	Base      map[string]int `json:"base,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
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
