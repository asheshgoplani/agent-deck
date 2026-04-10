package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// AdapterConfig holds the configuration passed to a WatcherAdapter during Setup.
type AdapterConfig struct {
	// Type is the adapter type: "webhook", "ntfy", "github", "slack", "gmail"
	Type string

	// Name is the watcher name (used for logging and health tracking)
	Name string

	// Settings holds adapter-specific key-value pairs from the watcher.toml [source] section.
	Settings map[string]string
}

// WatcherAdapter is the interface that all event source adapters must implement.
// Adapters normalize raw events from external sources into Event structs.
type WatcherAdapter interface {
	// Setup initializes the adapter with the provided config. Called once before Listen.
	Setup(ctx context.Context, config AdapterConfig) error

	// Listen blocks, producing normalized events on the provided channel until ctx is cancelled.
	Listen(ctx context.Context, events chan<- Event) error

	// Teardown cleans up any resources allocated during Setup.
	Teardown() error

	// HealthCheck returns a non-nil error if the adapter cannot currently reach its source.
	HealthCheck() error
}

// Event is a normalized event from any watcher adapter.
// All fields use json tags for persistence and wire format compatibility.
type Event struct {
	// Source is the watcher adapter type (e.g., "webhook", "ntfy", "github", "slack", "gmail")
	Source string `json:"source"`

	// Sender is the normalized email or identifier of the event originator
	Sender string `json:"sender"`

	// Subject is a short human-readable summary of the event
	Subject string `json:"subject"`

	// Body is the full payload text of the event
	Body string `json:"body"`

	// Timestamp is the time the event occurred (from the source, not ingestion time)
	Timestamp time.Time `json:"timestamp"`

	// RawPayload holds the adapter-specific raw data for debugging and audit
	RawPayload json.RawMessage `json:"raw_payload,omitempty"`

	// CustomDedupKey overrides the computed SHA-256 DedupKey when non-empty.
	// Used by adapters that need deterministic keys (e.g., Slack: "slack-{CHANNEL}-{TS}").
	CustomDedupKey string `json:"custom_dedup_key,omitempty"`

	// ParentDedupKey holds the dedup key of the parent event for thread replies.
	// When non-empty, the engine looks up the parent's session_id for thread routing.
	ParentDedupKey string `json:"parent_dedup_key,omitempty"`

	// ThreadSessionID is populated by the engine's writerLoop when a thread reply
	// is routed to an existing session. Empty means spawn a new session.
	ThreadSessionID string `json:"thread_session_id,omitempty"`
}

// DedupKey returns a deterministic hex-encoded SHA-256 hash of the event's
// source, sender, subject, and timestamp. The pipe delimiter prevents
// field-boundary collisions (e.g., sender="a|b" vs sender="a", subject="|b").
// Identical events from the same source at the same time produce the same key.
func (e Event) DedupKey() string {
	if e.CustomDedupKey != "" {
		return e.CustomDedupKey
	}
	h := sha256.New()
	// Pipe-delimited to prevent boundary collisions across field combinations
	fmt.Fprintf(h, "%s|%s|%s|%s", e.Source, e.Sender, e.Subject, e.Timestamp.UTC().Format(time.RFC3339Nano))
	return hex.EncodeToString(h.Sum(nil))
}
