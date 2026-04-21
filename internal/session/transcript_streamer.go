package session

// transcript_streamer.go — tails a Claude session JSONL file and emits
// structured streaming events as JSONL to an io.Writer.
//
// Phase 1 contract (v1.7.48, issue #689): Claude-only. The streamer assumes
// the transcript format produced by `claude` CLI. Non-Claude callers MUST be
// rejected at the CLI entry point before reaching this code.
//
// Event schema is versioned via StreamSchemaVersion on the initial `start`
// event so downstream consumers can branch on schema evolution.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// StreamSchemaVersion identifies the event schema. Bump on any breaking
// change to field names or event shapes; consumers can branch on it.
const StreamSchemaVersion = "1"

// StreamEvent is one structured event emitted by the streamer, serialized
// as one JSONL line on the output writer.
type StreamEvent struct {
	Type          string          `json:"type"`                     // start | text | tool_use | tool_result | stop | error
	SchemaVersion string          `json:"schema_version,omitempty"` // only set on "start"
	SessionID     string          `json:"session_id,omitempty"`     // set on "start"
	Timestamp     string          `json:"ts,omitempty"`             // RFC3339Nano when available
	MessageID     string          `json:"message_id,omitempty"`     // assistant message id (text / tool_use)
	Delta         string          `json:"delta,omitempty"`          // flushed text block
	ToolUseID     string          `json:"tool_use_id,omitempty"`
	Name          string          `json:"name,omitempty"`  // tool name on tool_use
	Input         json.RawMessage `json:"input,omitempty"` // tool input on tool_use
	Content       json.RawMessage `json:"content,omitempty"`
	Reason        string          `json:"reason,omitempty"`  // stop reason
	Message       string          `json:"message,omitempty"` // error payload
}

// StreamConfig controls streamer behavior. Zero values resolve to the
// approved defaults via WithDefaults(): 10s idle, 4000 chars, 3 tools.
type StreamConfig struct {
	// IdleTimeout is the max time without any new record before the
	// streamer emits an error event and returns. Default: 10s.
	IdleTimeout time.Duration

	// CharBudget is the max chars buffered in the text accumulator before
	// a flush. Default: 4000.
	CharBudget int

	// ToolBudget is the max queued tool events (tool_use + tool_result)
	// before buffered text is flushed. Default: 3.
	ToolBudget int

	// PollInterval is how often the tail loop checks the file for new
	// bytes. Default: 100ms.
	PollInterval time.Duration
}

// WithDefaults returns a StreamConfig with zero-valued fields replaced by
// the approved defaults. Negative values are also treated as zero.
func (c StreamConfig) WithDefaults() StreamConfig {
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 10 * time.Second
	}
	if c.CharBudget <= 0 {
		c.CharBudget = 4000
	}
	if c.ToolBudget <= 0 {
		c.ToolBudget = 3
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 100 * time.Millisecond
	}
	return c
}

// ErrStreamTimeout is returned when IdleTimeout elapses with no progress.
var ErrStreamTimeout = errors.New("stream idle timeout")

// claudeRecordHeader mirrors the record envelope at the top of each JSONL
// line. We reparse message subfields after confirming type.
type claudeRecordHeader struct {
	Type      string          `json:"type"`
	UUID      string          `json:"uuid"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type claudeMessageEnvelope struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason *string         `json:"stop_reason"`
}

// StreamTranscript tails path (a Claude session JSONL file) and writes
// structured StreamEvents as JSONL to w. sentAt gates out records whose
// timestamp is strictly before (sentAt - 250ms skew) so we don't replay
// history.
//
// Returns nil on natural end_turn stop, ctx.Err on cancel, or
// ErrStreamTimeout on idle timeout (with an error event already written).
func StreamTranscript(ctx context.Context, path, sessionID string, sentAt time.Time, w io.Writer, cfg StreamConfig) error {
	cfg = cfg.WithDefaults()

	enc := newEventEncoder(w)
	startEv := StreamEvent{
		Type:          "start",
		SchemaVersion: StreamSchemaVersion,
		SessionID:     sessionID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := enc.emit(startEv); err != nil {
		return err
	}

	st := newStreamerState(sentAt, cfg, enc)

	// Tail loop: re-open on missing-file (Claude may not have flushed
	// yet for a fresh session) but never block if the file exists.
	var offset int64
	lastProgress := time.Now()
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		// ctx cancel takes priority — flush any pending text so the
		// consumer sees what we had buffered.
		select {
		case <-ctx.Done():
			st.flushText("")
			return ctx.Err()
		default:
		}

		newOffset, progressed, stopped, stopReason, err := st.consumeFile(path, offset)
		if err != nil && !os.IsNotExist(err) {
			_ = enc.emit(StreamEvent{Type: "error", Message: err.Error(), Timestamp: time.Now().UTC().Format(time.RFC3339Nano)})
			return err
		}
		offset = newOffset
		if progressed {
			lastProgress = time.Now()
		}
		if stopped {
			st.flushText(stopReason)
			_ = enc.emit(StreamEvent{
				Type:      "stop",
				Reason:    stopReason,
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			})
			return nil
		}

		// Char-budget flush between ticks when no stop boundary yet.
		if st.textLen >= cfg.CharBudget {
			st.flushText("")
		}

		// Idle timeout check
		if time.Since(lastProgress) > cfg.IdleTimeout {
			st.flushText("")
			_ = enc.emit(StreamEvent{
				Type:      "error",
				Message:   fmt.Sprintf("idle timeout after %s with no new events", cfg.IdleTimeout),
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			})
			return ErrStreamTimeout
		}

		select {
		case <-ctx.Done():
			st.flushText("")
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// eventEncoder serializes events as JSONL to a writer.
type eventEncoder struct {
	w io.Writer
}

func newEventEncoder(w io.Writer) *eventEncoder {
	return &eventEncoder{w: w}
}

func (e *eventEncoder) emit(ev StreamEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = e.w.Write(b)
	return err
}

// streamerState accumulates parser state across polling ticks: file
// offset, seen-UUID dedup set, and the text + tool buffers for batching.
type streamerState struct {
	sentAtSkew time.Time
	cfg        StreamConfig
	enc        *eventEncoder

	seen map[string]struct{}

	// text accumulator: consecutive assistant text blocks from the same
	// message are merged until a flush boundary fires.
	textBuf    strings.Builder
	textLen    int
	textMsgID  string
	textTS     string
	toolsSince int // tool events emitted since the last text flush
}

func newStreamerState(sentAt time.Time, cfg StreamConfig, enc *eventEncoder) *streamerState {
	return &streamerState{
		sentAtSkew: sentAt.Add(-250 * time.Millisecond),
		cfg:        cfg,
		enc:        enc,
		seen:       make(map[string]struct{}),
	}
}

// consumeFile reads from offset, parses new lines, and emits events.
// Returns the new offset, whether any new line was parsed (progressed),
// whether a natural stop boundary was hit, the stop reason, and any error.
func (s *streamerState) consumeFile(path string, offset int64) (int64, bool, bool, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, false, false, "", err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return offset, false, false, "", err
	}
	// Truncation detection: if the file shrank below our offset, reset
	// to 0 and replay — rare, but we must not seek past EOF.
	if fi.Size() < offset {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset, false, false, "", err
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	progressed := false
	var stopped bool
	var stopReason string

	for sc.Scan() {
		line := sc.Bytes()
		offset += int64(len(line)) + 1 // +1 for newline consumed by Scanner
		if len(line) == 0 {
			continue
		}
		progressed = true

		var hdr claudeRecordHeader
		if err := json.Unmarshal(line, &hdr); err != nil {
			continue // skip malformed lines
		}
		if hdr.UUID != "" {
			if _, ok := s.seen[hdr.UUID]; ok {
				continue
			}
			s.seen[hdr.UUID] = struct{}{}
		}

		// Timestamp freshness gate — drop pre-sentAt history.
		if !s.isFresh(hdr.Timestamp) {
			continue
		}

		switch hdr.Type {
		case "assistant":
			st, reason := s.handleAssistant(hdr)
			if st {
				stopped = true
				stopReason = reason
				return offset, progressed, stopped, stopReason, nil
			}
		case "user":
			s.handleUser(hdr)
		}
	}
	if err := sc.Err(); err != nil {
		return offset, progressed, false, "", err
	}
	return offset, progressed, false, "", nil
}

func (s *streamerState) isFresh(ts string) bool {
	if ts == "" {
		return true // no timestamp = assume fresh
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return true
		}
	}
	return !t.Before(s.sentAtSkew)
}

// handleAssistant processes an assistant record, emits tool_use events,
// buffers text, and signals stop when stop_reason == "end_turn".
// Returns (stopped, reason).
func (s *streamerState) handleAssistant(hdr claudeRecordHeader) (bool, string) {
	if len(hdr.Message) == 0 {
		return false, ""
	}
	var msg claudeMessageEnvelope
	if err := json.Unmarshal(hdr.Message, &msg); err != nil {
		return false, ""
	}
	if msg.Role != "assistant" {
		return false, ""
	}

	// Extract blocks: content can be string OR array of typed blocks.
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		// Simple string content — treat as text block.
		var s2 string
		if err := json.Unmarshal(msg.Content, &s2); err == nil {
			s.appendText(msg.ID, hdr.Timestamp, s2)
		}
	} else {
		for _, blk := range blocks {
			var btype string
			_ = json.Unmarshal(blk["type"], &btype)
			switch btype {
			case "text":
				var txt string
				_ = json.Unmarshal(blk["text"], &txt)
				s.appendText(msg.ID, hdr.Timestamp, txt)
			case "tool_use":
				// Flush pending text before tool_use so consumer
				// sees "reasoning then tool call" ordering.
				s.flushText("")
				var toolID, toolName string
				_ = json.Unmarshal(blk["id"], &toolID)
				_ = json.Unmarshal(blk["name"], &toolName)
				input := blk["input"]
				_ = s.enc.emit(StreamEvent{
					Type:      "tool_use",
					Timestamp: hdr.Timestamp,
					MessageID: msg.ID,
					ToolUseID: toolID,
					Name:      toolName,
					Input:     input,
				})
				s.toolsSince++
				if s.toolsSince >= s.cfg.ToolBudget {
					s.flushText("")
				}
			case "thinking":
				// Phase 1: skip thinking blocks — they're noisy
				// and downstream consumers haven't asked for them
				// yet. Revisit in a future schema version.
			}
		}
	}

	// Natural stop: end_turn.
	if msg.StopReason != nil {
		switch *msg.StopReason {
		case "end_turn", "stop_sequence", "max_tokens":
			return true, *msg.StopReason
		case "tool_use":
			// Assistant paused on tool_use; the tool_result will
			// arrive in a later user record. Don't stop.
		}
	}
	return false, ""
}

// handleUser processes a user record; only tool_result blocks are emitted.
func (s *streamerState) handleUser(hdr claudeRecordHeader) {
	if len(hdr.Message) == 0 {
		return
	}
	var msg claudeMessageEnvelope
	if err := json.Unmarshal(hdr.Message, &msg); err != nil {
		return
	}
	if msg.Role != "user" {
		return
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}
	for _, blk := range blocks {
		var btype string
		_ = json.Unmarshal(blk["type"], &btype)
		if btype != "tool_result" {
			continue
		}
		// Flush buffered text so ordering stays stable.
		s.flushText("")
		var toolUseID string
		_ = json.Unmarshal(blk["tool_use_id"], &toolUseID)
		content := blk["content"]
		_ = s.enc.emit(StreamEvent{
			Type:      "tool_result",
			Timestamp: hdr.Timestamp,
			ToolUseID: toolUseID,
			Content:   content,
		})
		s.toolsSince++
		if s.toolsSince >= s.cfg.ToolBudget {
			s.flushText("")
		}
	}
}

// appendText buffers a text block, flushing first if it belongs to a
// different message than what we're currently accumulating.
func (s *streamerState) appendText(msgID, ts, txt string) {
	if txt == "" {
		return
	}
	if s.textMsgID != "" && s.textMsgID != msgID {
		s.flushText("")
	}
	if s.textMsgID == "" {
		s.textMsgID = msgID
		s.textTS = ts
	}
	s.textBuf.WriteString(txt)
	s.textLen += len(txt)
}

// flushText emits any buffered text as a text event and clears the buffer.
// reason is unused today but kept for future use (flush-cause telemetry).
func (s *streamerState) flushText(_ string) {
	if s.textBuf.Len() == 0 {
		s.textMsgID = ""
		s.textTS = ""
		s.textLen = 0
		s.toolsSince = 0
		return
	}
	_ = s.enc.emit(StreamEvent{
		Type:      "text",
		Timestamp: s.textTS,
		MessageID: s.textMsgID,
		Delta:     s.textBuf.String(),
	})
	s.textBuf.Reset()
	s.textLen = 0
	s.textMsgID = ""
	s.textTS = ""
	s.toolsSince = 0
}
