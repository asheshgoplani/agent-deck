package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// rm-time cleanup of transition-notifier state for a removed child session.
//
// Two artifacts must be swept on `agent-deck rm <child>` to stop the
// conductor from replaying events for a session that no longer exists
// (issue #910):
//
//   1. Every per-conductor inbox at <agent-deck-dir>/inboxes/*.jsonl —
//      drop lines whose child_session_id matches the removed id.
//   2. The dedup ledger at runtime/transition-notify-state.json —
//      delete the records[<child>] entry.
//
// Both ops are idempotent and best-effort: failures must not block the
// rm itself. Callers can log the swept counts but the caller's exit
// status is decided by the SQLite delete, not by this sweep.

// SweepInboxesForChildSession rewrites every inbox JSONL file under the
// agent-deck inboxes directory, dropping lines whose child_session_id
// equals the given id. Returns the total number of lines dropped across
// all inbox files.
//
// Each rewrite is atomic (temp file + rename). A no-op for absent files,
// empty inboxes, or inboxes that contain no matching lines.
//
// Issue #910 — without this sweep, the conductor replays
// deferred_target_busy events for children that have been removed.
func SweepInboxesForChildSession(childSessionID string) (int, error) {
	if strings.TrimSpace(childSessionID) == "" {
		return 0, errors.New("inbox sweep: empty child session id")
	}

	dir := InboxDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	inboxWriteMu.Lock()
	defer inboxWriteMu.Unlock()

	totalDropped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		dropped, err := sweepOneInboxLocked(path, childSessionID)
		if err != nil {
			return totalDropped, err
		}
		totalDropped += dropped
	}
	return totalDropped, nil
}

// sweepOneInboxLocked rewrites one inbox file without lines whose
// child_session_id matches. Caller holds inboxWriteMu.
//
// Strategy: stream-read, write surviving lines to a sibling .tmp file,
// rename atomically. The process-local fingerprint cache for the path
// is invalidated so any subsequent WriteInboxEvent rebuilds it from disk.
func sweepOneInboxLocked(path, childSessionID string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	var kept [][]byte
	var dropped int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var probe struct {
			ChildSessionID string `json:"child_session_id"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			// Preserve unparseable lines verbatim — we'd rather replay a
			// corrupt event than silently drop it during cleanup.
			kept = append(kept, append([]byte(nil), raw...))
			continue
		}
		if probe.ChildSessionID == childSessionID {
			dropped++
			continue
		}
		kept = append(kept, append([]byte(nil), raw...))
	}
	if err := scanner.Err(); err != nil {
		return dropped, err
	}
	_ = f.Close()

	if dropped == 0 {
		return 0, nil
	}

	if len(kept) == 0 {
		// Entire file was matching events — remove it outright.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return dropped, err
		}
		delete(inboxFingerprintCache, path)
		return dropped, nil
	}

	tmp := path + ".sweep.tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return dropped, err
	}
	w := bufio.NewWriter(out)
	for _, line := range kept {
		if _, err := w.Write(line); err != nil {
			_ = w.Flush()
			_ = out.Close()
			_ = os.Remove(tmp)
			return dropped, err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			_ = w.Flush()
			_ = out.Close()
			_ = os.Remove(tmp)
			return dropped, err
		}
	}
	if err := w.Flush(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return dropped, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return dropped, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return dropped, err
	}
	delete(inboxFingerprintCache, path)
	return dropped, nil
}

// RemoveNotifyStateRecord drops records[childSessionID] from
// runtime/transition-notify-state.json. Returns whether a record was
// actually present.
//
// Idempotent: a second call on the same id returns (false, nil). A
// missing state file is treated as "no record present".
//
// Issue #910 — the dedup ledger must release the removed child's slot,
// otherwise the next genuinely-delivered transition for a reused id
// would be (incorrectly) dedup-suppressed against the stale row.
func RemoveNotifyStateRecord(childSessionID string) (bool, error) {
	if strings.TrimSpace(childSessionID) == "" {
		return false, errors.New("notify-state sweep: empty child session id")
	}

	path := transitionNotifyStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	var state transitionNotifyState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, err
	}
	if state.Records == nil {
		return false, nil
	}
	if _, present := state.Records[childSessionID]; !present {
		return false, nil
	}
	delete(state.Records, childSessionID)

	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return true, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return true, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return true, err
	}
	return true, nil
}
