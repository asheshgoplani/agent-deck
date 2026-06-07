package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WatcherMeta holds metadata for a named watcher instance.
// Persisted as meta.json in the effective watcher/<name> data directory.
type WatcherMeta struct {
	Name           string `json:"name"`
	Type           string `json:"type"`                       // adapter type: "webhook", "ntfy", "github", "slack", "gmail"
	CreatedAt      string `json:"created_at"`                 // RFC3339 timestamp
	WatchExpiry    string `json:"watch_expiry,omitempty"`     // RFC3339 UTC (gmail only) — Gmail watch() expiration
	WatchHistoryID string `json:"watch_history_id,omitempty"` // uint64 as string (gmail only) — last processed Gmail history ID
}

// WatcherDir returns the effective base directory for all watchers.
func WatcherDir() (string, error) {
	return dataPath("watcher", "watcher", "watchers")
}

// WatcherNameDir returns the effective directory for a named watcher.
func WatcherNameDir(name string) (string, error) {
	base, err := WatcherDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// SaveWatcherMeta writes meta.json for a watcher.
// Creates the watcher directory if it does not exist.
//
// Uses a write-temp-rename atomic pattern: data is first written to
// meta.json.tmp, then renamed into place via os.Rename (POSIX atomic on
// the same filesystem). A mid-write crash therefore leaves either the
// old meta.json intact or the new meta.json complete — never a partial
// write. Any stale .tmp from a previous crash is overwritten on the
// next Save and removed on rename failure.
func SaveWatcherMeta(meta *WatcherMeta) error {
	if meta == nil {
		return fmt.Errorf("watcher metadata cannot be nil")
	}
	if meta.Name == "" {
		return fmt.Errorf("watcher name cannot be empty")
	}
	dir, err := WatcherNameDir(meta.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create watcher dir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal watcher meta.json: %w", err)
	}
	finalPath := filepath.Join(dir, "meta.json")
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write watcher meta.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename watcher meta.json: %w", err)
	}
	return nil
}

// LoadWatcherMeta reads meta.json for a named watcher.
func LoadWatcherMeta(name string) (*WatcherMeta, error) {
	dir, err := WatcherNameDir(name)
	if err != nil {
		return nil, err
	}
	metaPath := filepath.Join(dir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read meta.json for watcher %q: %w", name, err)
	}
	var meta WatcherMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse meta.json for watcher %q: %w", name, err)
	}
	if meta.Name == "" {
		meta.Name = name
	}
	return &meta, nil
}
