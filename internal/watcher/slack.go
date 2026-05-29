package watcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SlackAdapter subscribes to an ntfy.sh topic that receives bridged Slack events
// from a Cloudflare Worker. It parses both v1 (plain text) and v2 (structured JSON)
// payloads, normalizes to Event with deterministic Slack-specific dedup keys, and
// detects thread replies for session routing.
//
// The adapter duplicates the ntfy NDJSON streaming core (independent, not embedding
// NtfyAdapter) per design decision D-02.
type SlackAdapter struct {
	server string       // ntfy server URL (e.g., "https://ntfy.sh")
	topic  string       // topic name
	client *http.Client // HTTP client for streaming requests

	lastID string     // last received message ID for reconnect resumption
	mu     sync.Mutex // protects lastID

	// initialBackoff and maxBackoff are configurable for testing.
	// Production defaults: 2s initial, 30s max.
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// slackV2Payload is the Cloudflare Worker output schema (D-09).
type slackV2Payload struct {
	Type        string `json:"type"`
	V           int    `json:"v"`
	Channel     string `json:"channel"`
	User        string `json:"user"`
	TS          string `json:"ts"`
	Subtype     string `json:"subtype"`
	HasFiles    bool   `json:"has_files"`
	FileCount   int    `json:"file_count"`
	ThreadTS    string `json:"thread_ts"`
	SlackLink   string `json:"slack_link"`
	TextPreview string `json:"text_preview"`
}

// Setup initializes the adapter with the ntfy server URL and topic.
// The topic is required; the server defaults to "https://ntfy.sh".
//
// Settings:
//   - "topic": required ntfy topic name for Slack bridge
//   - "server": ntfy server URL (default "https://ntfy.sh")
func (a *SlackAdapter) Setup(_ context.Context, config AdapterConfig) error {
	a.topic = config.Settings["topic"]
	if a.topic == "" {
		return errors.New("slack adapter requires Settings[\"topic\"]")
	}

	a.server = config.Settings["server"]
	if a.server == "" {
		a.server = "https://ntfy.sh"
	}
	a.server = strings.TrimRight(a.server, "/")

	// Streaming client: no timeout on body reads (context handles cancellation).
	a.client = &http.Client{Timeout: 0}

	// Set defaults for backoff if not already set (tests may override before Listen).
	if a.initialBackoff == 0 {
		a.initialBackoff = 2 * time.Second
	}
	if a.maxBackoff == 0 {
		a.maxBackoff = 30 * time.Second
	}

	return nil
}

// Listen connects to the ntfy NDJSON stream and emits normalized Events on the
// provided channel. On disconnection, it reconnects with exponential backoff
// (initial 2s, 2x factor, 30s cap). Listen only returns when the context is cancelled.
func (a *SlackAdapter) Listen(ctx context.Context, events chan<- Event) error {
	backoff := a.initialBackoff

	for {
		err := a.streamOnce(ctx, events)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = err // logged by engine's runAdapter
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > a.maxBackoff {
			backoff = a.maxBackoff
		}
	}
}

// streamOnce opens a single NDJSON streaming connection and reads events until
// the connection closes or the context is cancelled.
func (a *SlackAdapter) streamOnce(ctx context.Context, events chan<- Event) error {
	a.mu.Lock()
	lastID := a.lastID
	a.mu.Unlock()

	streamURL := a.server + "/" + a.topic + "/json"
	if lastID != "" {
		streamURL += "?since=" + lastID
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy server returned status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var msg ntfyMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue // malformed JSON line, skip
		}
		if msg.Event != "message" {
			continue // skip "open" and "keepalive" events
		}

		// Track last ID for reconnect resumption
		a.mu.Lock()
		a.lastID = msg.ID
		a.mu.Unlock()

		evt := a.normalizeSlackEvent(msg, scanner.Bytes())

		// Non-blocking send (drop event if channel full)
		select {
		case events <- evt:
		default:
		}
	}

	return scanner.Err()
}

// normalizeSlackEvent converts an ntfy message into a normalized Event.
// It attempts to parse the message body as a v2 Slack payload; on failure
// or if the version is not 2, it falls back to v1 (plain text) handling.
func (a *SlackAdapter) normalizeSlackEvent(msg ntfyMessage, rawLine []byte) Event {
	var payload slackV2Payload
	if err := json.Unmarshal([]byte(msg.Message), &payload); err == nil && payload.V == 2 {
		return a.normalizeV2(msg, payload, rawLine)
	}
	return a.normalizeV1(msg, rawLine)
}

// normalizeV2 builds an Event from a Cloudflare Worker v2 Slack payload.
func (a *SlackAdapter) normalizeV2(msg ntfyMessage, payload slackV2Payload, rawLine []byte) Event {
	subject := firstLine(payload.TextPreview, 200)
	if subject == "" {
		subject = "Slack message"
	}

	evt := Event{
		Source:  "slack",
		Sender:  fmt.Sprintf("slack:%s", payload.Channel),
		Subject: subject,
		// Body carries the full message text (the worker's text_preview),
		// not the raw ntfy envelope, so downstream consumers (the conductor
		// bridge, triage prompt) get the complete multi-line message rather
		// than the first-line/200-byte `subject` label. The raw payload is
		// still preserved separately in RawPayload.
		Body:           payload.TextPreview,
		Timestamp:      time.Unix(msg.Time, 0),
		RawPayload:     json.RawMessage(rawLine),
		CustomDedupKey: fmt.Sprintf("slack-%s-%s", payload.Channel, payload.TS),
	}

	// Detect thread reply: thread_ts present and different from ts (D-07)
	if payload.ThreadTS != "" && payload.ThreadTS != payload.TS {
		evt.ParentDedupKey = fmt.Sprintf("slack-%s-%s", payload.Channel, payload.ThreadTS)
	}

	return evt
}

// normalizeV1 builds an Event from a plain text ntfy message (legacy bridge).
func (a *SlackAdapter) normalizeV1(msg ntfyMessage, rawLine []byte) Event {
	subject := firstLine(msg.Message, 200)
	if subject == "" {
		subject = "Slack notification"
	}

	return Event{
		Source:     "slack",
		Sender:     "slack:unknown",
		Subject:    subject,
		Body:       msg.Message,
		Timestamp:  time.Unix(msg.Time, 0),
		RawPayload: json.RawMessage(rawLine),
	}
}

// Teardown is a no-op. The streaming HTTP connection is closed by context
// cancellation in Listen.
func (a *SlackAdapter) Teardown() error {
	return nil
}

// HealthCheck verifies the ntfy server is reachable by sending an HTTP HEAD request
// with a 5-second timeout.
func (a *SlackAdapter) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, a.server, nil)
	if err != nil {
		return err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack health check returned status %d", resp.StatusCode)
	}

	return nil
}
