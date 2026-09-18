package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DeadLetterRecord is defined in deadletter_inspection.go — it is shared with
// the read-only inspection surface (#2111); see that file's doc comment for
// why the two identifier fields (Ref, ID) exist and mean different things.

type deadLetterEntry struct {
	record DeadLetterRecord
	event  TransitionNotificationEvent
	path   string
	raw    []byte
}

func deadLetterRecordID(store string, raw []byte) string {
	sum := sha256.Sum256(append(append([]byte(store), 0), raw...))
	return hex.EncodeToString(sum[:])[:16]
}

func deadLetterPayloadSummary(event TransitionNotificationEvent) string {
	if event.Kind == transitionKindFinished {
		summary := fmt.Sprintf("completion status=%s", strings.TrimSpace(event.DoneStatus))
		if event.DoneSummary != "" {
			summary += fmt.Sprintf(" summary_bytes=%d", len(event.DoneSummary))
		}
		return summary
	}
	from, to := strings.TrimSpace(event.FromStatus), strings.TrimSpace(event.ToStatus)
	if from == "" && to == "" {
		return "transition metadata unavailable"
	}
	return fmt.Sprintf("transition %s -> %s", from, to)
}

func readDeadLetterEntries(now time.Time) ([]deadLetterEntry, error) {
	dirEntries, err := os.ReadDir(DeadLetterDir())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		dirEntries = nil
	}
	var paths []string
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(DeadLetterDir(), dirEntry.Name()))
	}
	// _unowned is the discovery-only companion ledger counted by
	// CountDeadLetterRecords. Include it so an operator can actually clear the
	// warning rather than purging the forensic directory and still seeing it.
	paths = append(paths, InboxPathFor(UnownedInboxID))
	var out []deadLetterEntry
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
		for scanner.Scan() {
			raw := append([]byte(nil), scanner.Bytes()...)
			if len(strings.TrimSpace(string(raw))) == 0 {
				continue
			}
			entry := deadLetterEntry{path: path, raw: raw}
			entry.record.Store = "dead-letter"
			if path == InboxPathFor(UnownedInboxID) {
				entry.record.Store = "unowned"
			}
			entry.record.ID = deadLetterRecordID(entry.record.Store, raw)
			if err := json.Unmarshal(raw, &entry.event); err != nil {
				entry.record.ChildSessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
				entry.record.Reason = "corrupt"
				entry.record.PayloadSummary = "record could not be decoded"
				entry.record.Corrupt = true
				out = append(out, entry)
				continue
			}
			event := entry.event
			entry.record.ChildSessionID = event.ChildSessionID
			entry.record.ChildTitle = event.ChildTitle
			entry.record.TargetSessionID = event.TargetSessionID
			entry.record.Profile = event.Profile
			entry.record.Reason = strings.TrimSpace(event.DeadLetterReason)
			if entry.record.Reason == "" {
				entry.record.Reason = deadLetterReasonUnresolvable
			}
			entry.record.Timestamp = event.Timestamp
			if !event.Timestamp.IsZero() && now.After(event.Timestamp) {
				entry.record.AgeSeconds = int64(now.Sub(event.Timestamp) / time.Second)
			}
			entry.record.Attempts = event.Attempts
			entry.record.PayloadSummary = deadLetterPayloadSummary(event)
			out = append(out, entry)
		}
		scanErr := scanner.Err()
		closeErr := f.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].record.Timestamp.Equal(out[j].record.Timestamp) {
			return out[i].record.ID < out[j].record.ID
		}
		return out[i].record.Timestamp.After(out[j].record.Timestamp)
	})
	return out, nil
}

// ListDeadLetters returns stable, bounded metadata for every physical record.
func ListDeadLetters() ([]DeadLetterRecord, error) {
	entries, err := readDeadLetterEntries(time.Now())
	if err != nil {
		return nil, err
	}
	records := make([]DeadLetterRecord, len(entries))
	for i := range entries {
		records[i] = entries[i].record
	}
	return records, nil
}

func findDeadLetterEntry(id string) (deadLetterEntry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return deadLetterEntry{}, errors.New("dead-letter record id is required")
	}
	entries, err := readDeadLetterEntries(time.Now())
	if err != nil {
		return deadLetterEntry{}, err
	}
	var matches []deadLetterEntry
	for _, entry := range entries {
		if entry.record.ID == id || strings.HasPrefix(entry.record.ID, id) {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 0 {
		return deadLetterEntry{}, fmt.Errorf("dead-letter record %q not found", id)
	}
	if len(matches) > 1 {
		return deadLetterEntry{}, fmt.Errorf("dead-letter record id %q is ambiguous", id)
	}
	return matches[0], nil
}

// GetDeadLetter returns one operator-safe record by full ID or unique prefix.
func GetDeadLetter(id string) (DeadLetterRecord, error) {
	entry, err := findDeadLetterEntry(id)
	return entry.record, err
}

func removeDeadLetterEntry(entry deadLetterEntry) error {
	lock, err := AcquireConfigFileLock(entry.path)
	if err != nil {
		return fmt.Errorf("lock dead-letter record: %w", err)
	}
	defer lock.Release()
	managesInboxCache := entry.path == InboxPathFor(UnownedInboxID)
	if managesInboxCache {
		// Match the normal inbox writer's lock order (file lock, then process
		// mutex) and invalidate its fingerprint cache after removing a line.
		// Otherwise a TUI purge in this long-lived process could make a later
		// legitimate reappearance of the same event look already persisted.
		inboxWriteMu.Lock()
		defer inboxWriteMu.Unlock()
	}

	raw, err := os.ReadFile(entry.path)
	if err != nil {
		return err
	}
	var kept []string
	removed := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !removed && deadLetterRecordID(entry.record.Store, []byte(line)) == entry.record.ID {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return fmt.Errorf("dead-letter record %q changed or no longer exists", entry.record.ID)
	}
	if len(kept) == 0 {
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fsyncDir(filepath.Dir(entry.path))
		if managesInboxCache {
			delete(inboxFingerprintCache, entry.path)
		}
		return nil
	}
	data := []byte(strings.Join(kept, "\n") + "\n")
	if err := writeFileDurable(entry.path, data, 0o644); err != nil {
		return err
	}
	if managesInboxCache {
		delete(inboxFingerprintCache, entry.path)
	}
	return nil
}

// RetryDeadLetter re-resolves the child's current parent, commits the event to
// that durable inbox, and only then removes this one dead-letter record.
func RetryDeadLetter(id string) (string, error) {
	entry, err := findDeadLetterEntry(id)
	if err != nil {
		return "", err
	}
	if entry.record.Corrupt {
		return "", fmt.Errorf("dead-letter record %q is corrupt and cannot be retried", entry.record.ID)
	}
	storage, err := NewStorageWithProfile(entry.event.Profile)
	if err != nil {
		return "", fmt.Errorf("open profile %q: %w", entry.event.Profile, err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return "", fmt.Errorf("load profile %q: %w", entry.event.Profile, err)
	}
	byID := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}
	child := byID[entry.event.ChildSessionID]
	if child == nil {
		return "", fmt.Errorf("retry failed: child target %q no longer exists; record retained", entry.event.ChildSessionID)
	}
	if child.NoTransitionNotify {
		return "", fmt.Errorf("retry failed: child %q still has transition notifications disabled; record retained", child.ID)
	}
	parent := resolveParentNotificationTarget(child, byID)
	if parent == nil {
		target := strings.TrimSpace(child.ParentSessionID)
		if target == "" {
			target = entry.event.TargetSessionID
		}
		return "", fmt.Errorf("retry failed: parent target %q no longer exists or is not deliverable; record retained", target)
	}
	event := entry.event
	event.TargetSessionID = parent.ID
	event.DeadLetterReason = ""
	event.Attempts = 0
	event.DeliveryResult = transitionDeliveryCommitted
	if err := CommitToInbox(parent.ID, event); err != nil {
		return "", fmt.Errorf("retry delivery to %q failed; record retained: %w", parent.ID, err)
	}
	if err := removeDeadLetterEntry(entry); err != nil {
		return "", fmt.Errorf("delivered to %q but could not remove dead-letter record: %w", parent.ID, err)
	}
	return parent.ID, nil
}

// UnownedPurgeSkipReason explains, in both the human-readable summary and the
// --json outcome, why purge leaves an _unowned record alone: unlike a
// dead-letter or a parent inbox, that ledger has no consumer to ack a
// record — unowned_inbox.go documents that only SweepInboxByTTL may ever
// remove one, on the TTL sweep's generous (default 7-day) horizon, not an
// operator's purge. Purge including it (the reviewed rc.4 behavior) let a
// single `--yes` erase the only evidence a remote session ever stalled.
const UnownedPurgeSkipReason = "_unowned ledger has no ack path; only the TTL sweep may remove its records"

// DeadLetterActionOutcome is one --json record for a management call
// (retry or purge) that may affect several physical records, so a machine
// consumer gets the same per-record detail the human-readable summary does.
type DeadLetterActionOutcome struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// PurgeDeadLetter removes exactly one selected record. It refuses an
// _unowned record for the same reason purgeDeadLetters skips them in bulk.
func PurgeDeadLetter(id string) error {
	entry, err := findDeadLetterEntry(id)
	if err != nil {
		return err
	}
	if entry.record.Store == "unowned" {
		return fmt.Errorf("dead-letter record %q: %s", entry.record.ID, UnownedPurgeSkipReason)
	}
	return removeDeadLetterEntry(entry)
}

// purgeDeadLetters removes every matched record except those in the
// _unowned ledger, which it always skips (see UnownedPurgeSkipReason),
// stopping at the first removal failure. It returns one outcome per record
// considered so callers can report counts or emit them as --json.
func purgeDeadLetters(match func(DeadLetterRecord) bool) ([]DeadLetterActionOutcome, error) {
	entries, err := readDeadLetterEntries(time.Now())
	if err != nil {
		return nil, err
	}
	var results []DeadLetterActionOutcome
	for _, entry := range entries {
		if match != nil && !match(entry.record) {
			continue
		}
		if entry.record.Store == "unowned" {
			results = append(results, DeadLetterActionOutcome{ID: entry.record.ID, Action: "purge", Outcome: "skipped", Reason: UnownedPurgeSkipReason})
			continue
		}
		if err := removeDeadLetterEntry(entry); err != nil {
			return results, err
		}
		results = append(results, DeadLetterActionOutcome{ID: entry.record.ID, Action: "purge", Outcome: "removed"})
	}
	return results, nil
}

// PurgeDeadLettersOlderThan removes only records with a valid timestamp older
// than cutoff. Corrupt/undated records require explicit per-record or all purge.
func PurgeDeadLettersOlderThan(cutoff time.Time) ([]DeadLetterActionOutcome, error) {
	return purgeDeadLetters(func(record DeadLetterRecord) bool {
		return !record.Timestamp.IsZero() && record.Timestamp.Before(cutoff)
	})
}

// PurgeAllDeadLetters removes every selected physical record except the
// _unowned ledger. Callers must enforce explicit user confirmation before
// invoking this unbounded operation.
func PurgeAllDeadLetters() ([]DeadLetterActionOutcome, error) {
	return purgeDeadLetters(nil)
}
