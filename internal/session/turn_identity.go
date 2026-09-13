package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// TurnIdentity binds a submitted prompt to its durable Claude transcript
// record. StartOffset is immediately after that user record, so consumers can
// never mistake output from the preceding turn for this turn's reply.
type TurnIdentity struct {
	UUID        string
	Path        string
	StartOffset int64
}

// TurnQuery describes the transcript record a send is looking for.
//
// Cursor is the transcript size captured before the send; the search starts
// there so a record written before this send can never be adopted. When the
// transcript path only became known after the send, Cursor is 0 and NotBefore
// carries the guard instead: only a main-chain user record whose timestamp
// parses and is at or after NotBefore qualifies, and a record with a missing
// or malformed timestamp is rejected rather than trusted. Heartbeats, inbox
// nudges and retries resend identical text, so the older copy of the same
// prompt must never become this send's identity (PR #2043 round 2, #1978).
type TurnQuery struct {
	Path      string
	Prompt    string
	Cursor    int64
	NotBefore time.Time
}

// ErrTurnResponseIncomplete is returned by AwaitTurnResponse together with the
// text collected so far when the deadline passes before the turn reports an
// end-of-turn stop reason. The caller decides whether to surface the partial
// reply; it must not be presented as complete.
var ErrTurnResponseIncomplete = errors.New("turn response incomplete at deadline")

// ErrTranscriptTruncated is returned when the transcript is shorter than a
// durable position captured earlier (the pre-send cursor or a turn's start
// offset). The boundary is gone, and rescanning from offset 0 would replay
// records that precede the send — so the turn is refused, never guessed.
var ErrTranscriptTruncated = errors.New("transcript truncated below a durable turn boundary")

// TranscriptCursor returns the current end of a transcript. It is captured
// before transport submission and is only a search cursor, never turn proof.
func TranscriptCursor(path string) (int64, error) {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

type turnRecord struct {
	UUID        string          `json:"uuid"`
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type turnMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason *string         `json:"stop_reason"`
}

// normalizeTurnPrompt makes the transport's framing and Claude's storage
// comparable: CRLF from bracketed paste becomes LF, and surrounding
// whitespace (a trailing newline, composer padding) is ignored.
func normalizeTurnPrompt(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
}

func humanPrompt(rec turnRecord) (string, bool) {
	if rec.Type != "user" || rec.IsSidechain || len(rec.Message) == 0 {
		return "", false
	}
	var msg turnMessage
	if json.Unmarshal(rec.Message, &msg) != nil || msg.Role != "user" {
		return "", false
	}
	var text string
	if json.Unmarshal(msg.Content, &text) == nil {
		return text, true
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return "", false
	}
	var b strings.Builder
	for _, block := range blocks {
		var typ, text string
		_ = json.Unmarshal(block["type"], &typ)
		if typ != "text" {
			continue
		}
		_ = json.Unmarshal(block["text"], &text)
		b.WriteString(text)
	}
	return b.String(), b.Len() > 0
}

// recordTooOld applies the NotBefore guard. With a zero notBefore nothing is
// rejected (the cursor is the proof). Otherwise a record is accepted only on
// positive evidence: a parseable timestamp at or after notBefore. Missing or
// malformed timestamps are rejected — a guard that passes on absence is no
// guard when the search starts at offset 0.
func recordTooOld(rec turnRecord, notBefore time.Time) bool {
	if notBefore.IsZero() {
		return false
	}
	if rec.Timestamp == "" {
		return true
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, rec.Timestamp)
		if err != nil {
			return true
		}
	}
	return ts.Before(notBefore)
}

// scanTurnIdentity reads complete records from cursor and returns the first
// one that is this send's turn. The returned cursor is where the next scan
// resumes: past every complete line examined, and never past a partial
// trailing line, which Claude may still be writing.
func scanTurnIdentity(q TurnQuery, cursor int64) (TurnIdentity, int64, bool, error) {
	want := normalizeTurnPrompt(q.Prompt)
	f, err := os.Open(q.Path)
	if err != nil {
		return TurnIdentity{}, cursor, false, nil
	}
	defer f.Close()
	if fi, statErr := f.Stat(); statErr == nil && fi.Size() < cursor {
		return TurnIdentity{}, cursor, false, fmt.Errorf("%w: %s is %d bytes, cursor at %d", ErrTranscriptTruncated, q.Path, fi.Size(), cursor)
	}
	if _, err := f.Seek(cursor, 0); err != nil {
		return TurnIdentity{}, cursor, false, nil
	}
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, readErr := r.ReadBytes('\n')
		if readErr != nil {
			// Do not consume a partial trailing JSON record. Claude may be
			// writing it concurrently; the next poll must retry from the
			// same durable cursor once its newline arrives.
			return TurnIdentity{}, cursor, false, nil
		}
		cursor += int64(len(line))
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		var rec turnRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		body, human := humanPrompt(rec)
		if !human || normalizeTurnPrompt(body) != want || recordTooOld(rec, q.NotBefore) {
			continue
		}
		if rec.UUID == "" {
			return TurnIdentity{}, cursor, false, fmt.Errorf("submitted prompt has no transcript UUID; refusing to guess turn identity")
		}
		return TurnIdentity{UUID: rec.UUID, Path: q.Path, StartOffset: cursor}, cursor, true, nil
	}
}

// TurnAdvanced reports whether the transcript already holds this send's turn
// record (one scan, no waiting). The verification loop uses it as the
// authoritative "the target took this message up" signal: turn advancement
// in the harness's own transcript, not pane inference.
func TurnAdvanced(q TurnQuery) bool {
	_, _, found, err := scanTurnIdentity(q, q.Cursor)
	return err == nil && found
}

// AwaitTurnIdentity waits until Claude has durably accepted exactly q.Prompt
// as a main-chain user turn at or after q.Cursor (and not before q.NotBefore).
// A transport acknowledgement or timestamp is not an identity. Records
// without UUIDs are rejected rather than guessed.
//
// A message sent to a busy target is queued and only becomes a user record
// when the queued turn starts, so this is also the gate that holds --wait and
// --stream until then — the in-flight turn's output can never be consumed as
// the queued message's reply.
func AwaitTurnIdentity(q TurnQuery, timeout, poll time.Duration) (TurnIdentity, error) {
	cursor := q.Cursor
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		id, next, found, err := scanTurnIdentity(q, cursor)
		if err != nil {
			return TurnIdentity{}, err
		}
		if found {
			return id, nil
		}
		cursor = next
		time.Sleep(poll)
	}
	return TurnIdentity{}, fmt.Errorf("turn identity not established within %s", timeout)
}

func assistantText(rec turnRecord) (string, bool, string) {
	if rec.Type != "assistant" || rec.IsSidechain {
		return "", false, ""
	}
	var msg turnMessage
	if json.Unmarshal(rec.Message, &msg) != nil || msg.Role != "assistant" {
		return "", false, ""
	}
	var out strings.Builder
	var plain string
	if json.Unmarshal(msg.Content, &plain) == nil {
		out.WriteString(plain)
	} else {
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(msg.Content, &blocks) == nil {
			for _, block := range blocks {
				var typ, text string
				_ = json.Unmarshal(block["type"], &typ)
				if typ == "text" {
					_ = json.Unmarshal(block["text"], &text)
					out.WriteString(text)
				}
			}
		}
	}
	reason := ""
	if msg.StopReason != nil {
		reason = *msg.StopReason
	}
	return out.String(), true, reason
}

// turnEnded mirrors the streamer: Claude closes a turn with end_turn,
// stop_sequence or max_tokens; tool_use pauses for a tool_result and the turn
// continues.
func turnEnded(reason string) bool {
	switch reason {
	case "end_turn", "stop_sequence", "max_tokens":
		return true
	}
	return false
}

// AwaitTurnResponse returns only assistant text after id's user record and
// refuses to cross into a later human turn. Each poll reads the transcript
// from id.StartOffset, never the prefix that is always discarded.
//
// When timeout elapses with assistant text collected but no end-of-turn stop
// reason, the partial text is returned together with
// ErrTurnResponseIncomplete so the caller can surface it honestly.
func AwaitTurnResponse(id TurnIdentity, timeout, poll time.Duration) (*ResponseOutput, error) {
	deadline := time.Now().Add(timeout)
	var partial *ResponseOutput
	for {
		resp, done, err := readTurnResponse(id)
		if err != nil {
			return nil, err
		}
		if done {
			return resp, nil
		}
		if resp != nil && resp.Content != "" {
			partial = resp
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(poll)
	}
	if partial != nil {
		return partial, fmt.Errorf("turn %s: %w after %s", id.UUID, ErrTurnResponseIncomplete, timeout)
	}
	return nil, fmt.Errorf("turn %s response not complete within %s", id.UUID, timeout)
}

// readTurnResponse scans the transcript tail after id.StartOffset once. It
// returns the assistant text so far, whether the turn has ended, and an error
// only when a later human prompt appears before this turn ended.
func readTurnResponse(id TurnIdentity) (*ResponseOutput, bool, error) {
	f, err := os.Open(id.Path)
	if err != nil {
		return nil, false, nil
	}
	defer f.Close()
	if fi, statErr := f.Stat(); statErr == nil && fi.Size() < id.StartOffset {
		return nil, false, fmt.Errorf("%w: turn %s started at offset %d, transcript is %d bytes", ErrTranscriptTruncated, id.UUID, id.StartOffset, fi.Size())
	}
	if _, err := f.Seek(id.StartOffset, 0); err != nil {
		return nil, false, nil
	}
	var text strings.Builder
	lastTS := ""
	ended := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var rec turnRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if _, human := humanPrompt(rec); human {
			return nil, false, fmt.Errorf("turn %s produced no end_turn before the next submitted prompt", id.UUID)
		}
		chunk, assistant, reason := assistantText(rec)
		if !assistant {
			continue
		}
		text.WriteString(chunk)
		lastTS = rec.Timestamp
		if turnEnded(reason) {
			ended = true
			break
		}
	}
	if !ended && text.Len() == 0 {
		return nil, false, nil
	}
	return &ResponseOutput{Tool: "claude", Role: "assistant", Content: strings.TrimSpace(text.String()), Timestamp: lastTS}, ended, nil
}
