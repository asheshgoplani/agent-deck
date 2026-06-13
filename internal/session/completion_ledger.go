package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CompletionLedgerEntry is the durable, non-destructive last-known completion
// for a child session. Unlike the task-worker CompletionRecord (whose presence
// makes the daemon stand down from poll-inference; see emitDoneSignals'
// CompletionRecordExists guard), the ledger is purely informational: it records
// the most recent asserted completion so a parent can query "which of my fleet
// finished" without consuming any delivery event. Last-wins per child.
type CompletionLedgerEntry struct {
	ChildID    string    `json:"child_id"`
	Profile    string    `json:"profile"`
	Title      string    `json:"title,omitempty"`
	Status     string    `json:"status"` // "ok" | "fail"
	Summary    string    `json:"summary,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

func completionLedgerDir() (string, error) {
	return runtimeDataPath("completion-ledger")
}

func completionLedgerPath(childID string) (string, error) {
	dir, err := completionLedgerDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, safeRecordName(childID)+".json"), nil
}

// WriteLedgerEntry persists an entry atomically (tmp + rename), last-wins.
func WriteLedgerEntry(e CompletionLedgerEntry) error {
	if strings.TrimSpace(e.ChildID) == "" {
		return errors.New("completion ledger: empty child id")
	}
	if e.FinishedAt.IsZero() {
		e.FinishedAt = time.Now()
	}
	path, err := completionLedgerPath(e.ChildID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadLedgerEntry returns the last-known completion for a child, if any. The
// read is non-destructive — checking from a parent never consumes a delivery
// event the conductor or another chat relies on.
func ReadLedgerEntry(childID string) (CompletionLedgerEntry, bool) {
	path, err := completionLedgerPath(childID)
	if err != nil {
		return CompletionLedgerEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return CompletionLedgerEntry{}, false
	}
	var e CompletionLedgerEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return CompletionLedgerEntry{}, false
	}
	return e, true
}
