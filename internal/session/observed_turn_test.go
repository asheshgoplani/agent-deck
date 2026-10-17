package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAwaitObservedTurn_PreservesFreshMainChainBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	old := `{"type":"user","uuid":"old","message":{"role":"user","content":"reply ok"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"STALE","stop_reason":"end_turn"}}` + "\n"
	peer := `{"type":"user","uuid":"peer","sessionId":"native","isMeta":true,"origin":{"kind":"peer"},"message":{"role":"user","content":"Another Claude session sent a message:\nreply ok\n\nA peer, not your user."}}` + "\n"
	data := old +
		`{"type":"assistant","message":{"role":"assistant","content":"OLD TAIL","stop_reason":"end_turn"}}` + "\n" +
		`{"type":"user","uuid":"side","isSidechain":true,"message":{"role":"user","content":"side task"}}` + "\n" + peer +
		`{"type":"assistant","message":{"role":"assistant","content":"NEW REPLY","stop_reason":"end_turn"}}` + "\n" +
		`{"type":"user","uuid":"next","message":{"role":"user","content":"next prompt"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"NEXT REPLY","stop_reason":"end_turn"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	q := TurnQuery{Path: path, Prompt: "reply ok", Cursor: int64(len(old))}
	if _, _, found, err := scanTurnIdentity(q, q.Cursor); err != nil || found {
		t.Fatalf("exact identity adopted an unrelated/wrapped prompt: found=%v err=%v", found, err)
	}
	id, err := AwaitObservedTurn(q, time.Second, time.Millisecond)
	if err != nil || id.UUID != "peer" || id.SessionID != "native" {
		t.Fatalf("observed boundary=%+v err=%v", id, err)
	}
	response, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil || response.Content != "NEW REPLY" {
		t.Fatalf("observed response=%+v err=%v", response, err)
	}
}

func TestAwaitObservedTurn_UnknownPathRequiresTimestamp(t *testing.T) {
	now := time.Now().UTC()
	for _, stamp := range []string{"", "invalid", now.Add(-time.Second).Format(time.RFC3339Nano)} {
		t.Run(stamp, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			data := fmt.Sprintf(`{"type":"user","uuid":"old","timestamp":%q,"message":{"role":"user","content":"old peer"}}`, stamp) + "\n" +
				fmt.Sprintf(`{"type":"user","uuid":"new","timestamp":%q,"message":{"role":"user","content":"new peer"}}`, now.Format(time.RFC3339Nano)) + "\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := AwaitObservedTurn(TurnQuery{Path: path, NotBefore: now}, time.Second, time.Millisecond)
			if err != nil || id.UUID != "new" {
				t.Fatalf("observed boundary=%+v err=%v", id, err)
			}
		})
	}
}

func TestAwaitObservedTurn_RefusesMissingUUIDAndTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := `{"type":"user","message":{"role":"user","content":"peer"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AwaitObservedTurn(TurnQuery{Path: path}, time.Second, time.Millisecond); err == nil {
		t.Fatal("adopted a record without UUID")
	}
	if _, err := AwaitObservedTurn(TurnQuery{Path: path, Cursor: int64(len(data) + 1)}, time.Second, time.Millisecond); !errors.Is(err, ErrTranscriptTruncated) {
		t.Fatalf("truncated boundary replayed old records: %v", err)
	}
}

func TestObservedTurn_RetainsPartialRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := `{"type":"user","uuid":"peer","message":{"role":"user","content":"peer"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	q := TurnQuery{Path: path}
	if _, next, found, err := scanTurn(q, 0, false); err != nil || found || next != 0 {
		t.Fatalf("consumed a partial record: next=%d found=%v err=%v", next, found, err)
	}
	if err := os.WriteFile(path, []byte(data+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := AwaitObservedTurn(q, time.Second, time.Millisecond)
	if err != nil || id.UUID != "peer" || id.StartOffset != int64(len(data)+1) {
		t.Fatalf("completed record=%+v err=%v", id, err)
	}
}
