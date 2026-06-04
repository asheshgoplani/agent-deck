package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Issue #1225: the consumer side of the durable per-parent outbox. The parent
// drains its inbox at its own turn boundary (Stop hook) and on heartbeat.
//
// Audit B1 — TRUE at-least-once durability. The drain is a two-phase commit so
// no record is ever lost to a crash:
//
//	Phase 1 (stage, under inboxWriteMu): recover any records left by a prior
//	crashed drain (the in-flight WAL), read the current inbox, durably stage the
//	union to the WAL (fsync), THEN remove the inbox file. The WAL is the durable
//	copy that survives the truncate — "record intent → delete → finalize".
//
//	Phase 2 (finalize, under consumedTurnsMu): collapse to last-wins per child,
//	skip turn_fingerprints already consumed (exactly-once EFFECTS), mark the rest
//	consumed (fsync the ledger), and only THEN drop the WAL.
//
// A process death anywhere between the inbox-remove and the WAL-drop re-delivers
// the staged records on the next drain; the consumed-turn ledger (a fsync'd
// dedup table) collapses any duplicate. This is the outbox + inbox dedup-table
// pattern: at-least-once delivery with exactly-once effects, never loss.

// consumedTurnsTTL bounds the consumed-fingerprint ledger so it can't grow
// without limit. A turn older than this can never be re-delivered (the inbox
// TTL is shorter), so forgetting it is safe.
const consumedTurnsTTL = 14 * 24 * time.Hour

var consumedTurnsMu sync.Mutex

// ConsumedTurnsDir holds per-parent consumed-fingerprint ledgers.
func ConsumedTurnsDir() string {
	dir, err := runtimeDataPath("consumed-turns")
	if err != nil {
		return tempAgentDeckPath("runtime", "consumed-turns")
	}
	return dir
}

func consumedTurnsPathFor(parentID string) string {
	return filepath.Join(ConsumedTurnsDir(), sanitizeInboxName(parentID)+".json")
}

func loadConsumedTurnsLocked(parentID string) map[string]int64 {
	out := map[string]int64{}
	raw, err := os.ReadFile(consumedTurnsPathFor(parentID))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func saveConsumedTurnsLocked(parentID string, m map[string]int64) error {
	// Prune expired entries on every save to bound growth.
	cutoff := time.Now().Add(-consumedTurnsTTL).Unix()
	for fp, ts := range m {
		if ts < cutoff {
			delete(m, fp)
		}
	}
	path := consumedTurnsPathFor(parentID)
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// Audit B1: the consumed ledger is the exactly-once dedup table; it MUST be
	// durably on disk (fsync) before the WAL is dropped, or a crash could both
	// forget the record was consumed AND lose the staged copy.
	return writeFileDurable(path, data, 0o644)
}

// DrainInboxForParent drains the parent's durable outbox and returns the
// deliverables (last-wins per child, exactly-once per turn). See file comment.
//
// Two-phase, crash-safe (audit B1): stage the records into the in-flight WAL
// before truncating the inbox, then finalize the consumed ledger and drop the
// WAL. A crash between the two phases re-delivers from the WAL on the next call.
func DrainInboxForParent(parentID string) ([]TransitionNotificationEvent, error) {
	if strings.TrimSpace(parentID) == "" {
		return nil, errors.New("inbox drain: empty parent session id")
	}

	// Phase 1: recover prior in-flight records, read the current inbox, durably
	// stage the union to the WAL, then remove the inbox. Exactly one concurrent
	// caller wins the inbox under inboxWriteMu.
	staged, err := stageInboxDrainLocked(parentID)
	if err != nil {
		return nil, err
	}
	if len(staged) == 0 {
		return nil, nil
	}

	// Phase 2: dedup against the consumed ledger, mark the rest consumed (fsync),
	// then drop the WAL.
	return finalizeInboxDrain(parentID, staged)
}

// stageInboxDrainLocked is phase 1 of the crash-safe drain. It recovers any
// records a prior crashed drain left in the in-flight WAL, reads the current
// inbox, durably stages the union to the WAL (fsync), then removes the inbox
// file. After it returns the records live in the WAL even though the inbox is
// gone, so a crash before finalize re-delivers them. Caller passes nothing; the
// function acquires inboxWriteMu itself.
func stageInboxDrainLocked(parentID string) ([]TransitionNotificationEvent, error) {
	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	recovered, err := loadInflightLocked(parentID)
	if err != nil {
		return nil, err
	}
	current, err := readInboxEventsLocked(InboxPathFor(parentID))
	if err != nil {
		return nil, err
	}

	union := append(recovered, current...)
	if len(union) == 0 {
		// Nothing recovered and nothing pending — make sure no stale WAL lingers.
		removeInflightLocked(parentID)
		return nil, nil
	}

	// Record intent: durably stage the union BEFORE truncating the inbox.
	if err := writeInflightLocked(parentID, union); err != nil {
		return nil, err
	}
	// Now safe to remove the inbox — the records are durably in the WAL.
	if err := os.Remove(InboxPathFor(parentID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return union, err
	}
	delete(inboxFingerprintCache, InboxPathFor(parentID))
	return union, nil
}

// finalizeInboxDrain is phase 2: collapse last-wins, dedup against the consumed
// ledger, mark newly-delivered turns consumed (durable), then drop the WAL.
func finalizeInboxDrain(parentID string, staged []TransitionNotificationEvent) ([]TransitionNotificationEvent, error) {
	collapsed := collapseLastWins(staged)

	consumedTurnsMu.Lock()
	defer consumedTurnsMu.Unlock()
	consumed := loadConsumedTurnsLocked(parentID)

	now := time.Now().Unix()
	var out []TransitionNotificationEvent
	dirty := false
	for _, ev := range collapsed {
		fp := ev.TurnFingerprint
		if fp == "" {
			fp = TurnFingerprint(ev)
		}
		if _, seen := consumed[fp]; seen {
			continue // exactly-once effects: this turn was already acted on
		}
		consumed[fp] = now
		dirty = true
		out = append(out, ev)
	}
	if dirty {
		if err := saveConsumedTurnsLocked(parentID, consumed); err != nil {
			// Ledger not durable — leave the WAL in place so the next drain
			// re-delivers rather than loses.
			return out, err
		}
	}
	// Consumed ledger durable (or nothing new to mark) — the records are now
	// either delivered-and-recorded or already-consumed duplicates; drop the WAL.
	inboxWriteMu.Lock()
	removeInflightLocked(parentID)
	inboxWriteMu.Unlock()
	return out, nil
}

// DrainStagePhaseForCrashTest runs ONLY phase 1 of the drain (stage + truncate)
// and returns the staged records, simulating a process that dies before the
// consumed ledger is finalized. Tests use it to prove the at-least-once
// re-delivery contract (audit B1). Production code never calls it.
func DrainStagePhaseForCrashTest(parentID string) ([]TransitionNotificationEvent, error) {
	return stageInboxDrainLocked(parentID)
}

// InboxHasPending cheaply reports whether the parent has anything to drain —
// either a non-empty inbox or records recovered in the in-flight WAL. It does
// NOT consume anything (a single stat per file). Used as the Stop-hook fast
// path: a leaf / non-parent session (no inbox file) returns false, so the
// Stop-hook sync flip is inert for it — no block, no stop-block ledger write
// (audit B12 scope).
func InboxHasPending(parentID string) bool {
	if strings.TrimSpace(parentID) == "" {
		return false
	}
	return fileHasContent(InboxPathFor(parentID)) || fileHasContent(inboxInflightPathFor(parentID))
}

func fileHasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// --- in-flight WAL (audit B1 durability) ------------------------------------

// inboxInflightDir holds per-parent in-flight drain WALs: the durable copy of
// records staged for a drain, written before the inbox is truncated and dropped
// only after the consumed ledger is finalized.
func inboxInflightDir() string {
	dir, err := runtimeDataPath("inbox-inflight")
	if err != nil {
		return tempAgentDeckPath("runtime", "inbox-inflight")
	}
	return dir
}

func inboxInflightPathFor(parentID string) string {
	return filepath.Join(inboxInflightDir(), sanitizeInboxName(parentID)+".jsonl")
}

// loadInflightLocked reads any records a prior crashed drain staged but never
// finalized. Caller holds inboxWriteMu. Corrupt lines are skipped (matching the
// inbox read path) so one bad line can't strand the rest.
func loadInflightLocked(parentID string) ([]TransitionNotificationEvent, error) {
	return readInboxEventsLocked(inboxInflightPathFor(parentID))
}

// writeInflightLocked durably stages records to the in-flight WAL. Caller holds
// inboxWriteMu. The records are fsync'd before this returns so they survive a
// crash that happens after the subsequent inbox truncate.
func writeInflightLocked(parentID string, events []TransitionNotificationEvent) error {
	var buf strings.Builder
	for _, ev := range events {
		fp := EventFingerprint(ev)
		line, err := json.Marshal(inboxWireEvent{TransitionNotificationEvent: ev, Fingerprint: fp})
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return writeFileDurable(inboxInflightPathFor(parentID), []byte(buf.String()), 0o644)
}

// removeInflightLocked drops the in-flight WAL after a drain is fully finalized.
// Caller holds inboxWriteMu. Best-effort: a missing file is not an error.
func removeInflightLocked(parentID string) {
	_ = os.Remove(inboxInflightPathFor(parentID))
}

// readInboxEventsLocked reads all parseable events from a JSONL inbox/WAL file
// without truncating it. Returns an empty slice for a missing/empty file.
// Corrupt lines are skipped rather than failing the whole read (audit B3/B11),
// and the scanner cap is raised so oversized events are not silently truncated
// (audit B6). Caller holds inboxWriteMu.
func readInboxEventsLocked(path string) ([]TransitionNotificationEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []TransitionNotificationEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		ev, derr := decodeInboxLine([]byte(line))
		if derr != nil {
			continue // skip corrupt lines rather than failing the whole drain
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// collapseLastWins reduces multiple records for one child to the single latest
// (by Timestamp), preserving first-seen order of children for stable output.
func collapseLastWins(events []TransitionNotificationEvent) []TransitionNotificationEvent {
	latest := map[string]TransitionNotificationEvent{}
	order := []string{}
	for _, ev := range events {
		cur, seen := latest[ev.ChildSessionID]
		if !seen {
			order = append(order, ev.ChildSessionID)
			latest[ev.ChildSessionID] = ev
			continue
		}
		if !ev.Timestamp.Before(cur.Timestamp) {
			latest[ev.ChildSessionID] = ev
		}
	}
	out := make([]TransitionNotificationEvent, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out
}

// ForgetConsumedTurnsForChild removes any consumed-turn ledger entries for a
// child across all parents — used by rm_sweep on child removal so the ledger
// doesn't leak. Best-effort.
func ForgetConsumedTurnsForChild(childSessionID string) {
	child := strings.TrimSpace(childSessionID)
	if child == "" {
		return
	}
	prefix := child + "@"
	consumedTurnsMu.Lock()
	defer consumedTurnsMu.Unlock()
	entries, err := os.ReadDir(ConsumedTurnsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		parentID := strings.TrimSuffix(e.Name(), ".json")
		m := loadConsumedTurnsLocked(parentID)
		changed := false
		for fp := range m {
			if strings.HasPrefix(fp, prefix) {
				delete(m, fp)
				changed = true
			}
		}
		if changed {
			_ = saveConsumedTurnsLocked(parentID, m)
		}
	}
}
