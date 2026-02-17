package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// StatusEvent represents a session status change event.
// Written atomically to ~/.agent-deck/events/ by both the hook handler (Claude)
// and the TUI's background status poller (all tools).
type StatusEvent struct {
	InstanceID string `json:"instance_id"`
	Title      string `json:"title"`
	Tool       string `json:"tool"`
	Status     string `json:"status"`
	PrevStatus string `json:"prev_status"`
	Timestamp  int64  `json:"ts"`
}

// GetEventsDir returns the path to the events directory.
func GetEventsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "events")
	}
	return filepath.Join(home, ".agent-deck", "events")
}

// WriteStatusEvent atomically writes a status event to the events directory.
// Uses tmp file + rename to avoid partial reads by watchers.
func WriteStatusEvent(event StatusEvent) error {
	eventsDir := GetEventsDir()
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		return fmt.Errorf("create events dir: %w", err)
	}

	jsonData, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	filePath := filepath.Join(eventsDir, event.InstanceID+".json")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, jsonData, 0644); err != nil {
		return fmt.Errorf("write tmp event: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("rename event: %w", err)
	}

	hookLog.Debug("status_event_written",
		slog.String("instance", event.InstanceID),
		slog.String("status", event.Status),
		slog.String("prev", event.PrevStatus),
	)
	return nil
}

// ReadPrevEventStatus reads the previous status from an existing event file.
// Returns empty string if no event file exists or can't be read.
func ReadPrevEventStatus(instanceID string) string {
	eventsDir := GetEventsDir()
	filePath := filepath.Join(eventsDir, instanceID+".json")

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}

	var event StatusEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return ""
	}
	return event.Status
}

// CleanStaleEventFiles removes event files older than 24 hours.
func CleanStaleEventFiles() {
	eventsDir := GetEventsDir()
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(eventsDir, entry.Name()))
		}
	}
}
