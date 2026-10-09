package watcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// HealthSample is a single health snapshot stored in WatcherState.HealthWindow.
type HealthSample struct {
	TS time.Time `json:"ts"`
	OK bool      `json:"ok"`
}

// WatcherState is the persisted per-watcher state snapshot written by the writerLoop
// after every successful event insert. Hot-reload safe: no in-process cache.
type WatcherState struct {
	LastEventTS    time.Time      `json:"last_event_ts"`
	ErrorCount     int            `json:"error_count"`
	AdapterHealthy bool           `json:"adapter_healthy"`
	HealthWindow   []HealthSample `json:"health_window"` // cap at 64 samples
	// DedupCursor is the source resume position of the last event the engine
	// stored (an ntfy message ID for ntfy and slack watchers), and
	// DedupCursorTime is that event's source timestamp. On start the engine
	// turns them into AdapterConfig.ResumeSince (#2538).
	DedupCursor     string    `json:"dedup_cursor"`
	DedupCursorTime time.Time `json:"dedup_cursor_time,omitzero"`
}

// SaveState writes <name>/state.json atomically (write-temp-rename).
// Pattern copied from internal/session SaveWatcherMeta (atomic write-temp-rename).
func SaveState(name string, s *WatcherState) error {
	if name == "" {
		return fmt.Errorf("watcher name cannot be empty")
	}
	if s == nil {
		return fmt.Errorf("watcher state cannot be nil")
	}
	dir, err := WatcherDir(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create watcher dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state.json: %w", err)
	}
	finalPath := filepath.Join(dir, "state.json")
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write state.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename state.json: %w", err)
	}
	return nil
}

// LoadState reads <name>/state.json. Returns (nil, nil) if the file does not exist (fresh watcher).
// Always hits disk — no in-process cache, so external edits are visible on next call (hot-reload safe).
func LoadState(name string) (*WatcherState, error) {
	dir, err := WatcherDir(name)
	if err != nil {
		return nil, err
	}
	statePath := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read state.json for watcher %q: %w", name, err)
	}
	var s WatcherState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to parse state.json for watcher %q: %w", name, err)
	}
	return &s, nil
}
