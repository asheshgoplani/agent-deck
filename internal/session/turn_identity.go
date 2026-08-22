package session

import (
	"bufio"
	"bytes"
	"encoding/json"
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

// AwaitTurnIdentity waits until Claude has durably accepted exactly prompt as
// a main-chain user turn. A transport acknowledgement or timestamp is not an
// identity. Records without UUIDs are rejected rather than guessed.
func AwaitTurnIdentity(path, prompt string, cursor int64, timeout, poll time.Duration) (TurnIdentity, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := os.Open(path)
		if err == nil {
			fi, statErr := f.Stat()
			if statErr == nil && fi.Size() < cursor {
				cursor = 0
			}
			_, _ = f.Seek(cursor, 0)
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
			for sc.Scan() {
				cursor += int64(len(sc.Bytes())) + 1
				var rec turnRecord
				if json.Unmarshal(sc.Bytes(), &rec) != nil {
					continue
				}
				body, human := humanPrompt(rec)
				if !human || body != prompt {
					continue
				}
				_ = f.Close()
				if rec.UUID == "" {
					return TurnIdentity{}, fmt.Errorf("submitted prompt has no transcript UUID; refusing to guess turn identity")
				}
				return TurnIdentity{UUID: rec.UUID, Path: path, StartOffset: cursor}, nil
			}
			_ = f.Close()
		}
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

// AwaitTurnResponse returns only assistant text after id's user record and
// refuses to cross into a later human turn.
func AwaitTurnResponse(id TurnIdentity, timeout, poll time.Duration) (*ResponseOutput, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(id.Path)
		if err == nil && id.StartOffset <= int64(len(data)) {
			var text strings.Builder
			sc := bufio.NewScanner(bytes.NewReader(data[id.StartOffset:]))
			sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
			lastTS := ""
			for sc.Scan() {
				var rec turnRecord
				if json.Unmarshal(sc.Bytes(), &rec) != nil {
					continue
				}
				if _, human := humanPrompt(rec); human {
					return nil, fmt.Errorf("turn %s produced no end_turn before the next submitted prompt", id.UUID)
				}
				chunk, assistant, reason := assistantText(rec)
				if assistant {
					text.WriteString(chunk)
					lastTS = rec.Timestamp
					if reason == "end_turn" {
						return &ResponseOutput{Tool: "claude", Role: "assistant", Content: strings.TrimSpace(text.String()), Timestamp: lastTS}, nil
					}
				}
			}
		}
		time.Sleep(poll)
	}
	return nil, fmt.Errorf("turn %s response not complete within %s", id.UUID, timeout)
}
