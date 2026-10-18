package reader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Claude splits one message into several records that repeat its id and
// usage; the usage events carry the message id so the importer counts it once.
func TestScanClaudeUsageCarriesTheMessageID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	body := `{"type":"assistant","uuid":"r1","sessionId":"s","timestamp":"2026-10-01T10:00:00.000Z","message":{"id":"msg_01","role":"assistant","model":"m","content":[{"type":"thinking","thinking":"x"}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"assistant","uuid":"r2","sessionId":"s","timestamp":"2026-10-01T10:00:00.100Z","message":{"id":"msg_01","role":"assistant","model":"m","content":[{"type":"text","text":"y"}],"usage":{"input_tokens":10,"output_tokens":5}}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := ScanClaudeUsage(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want one per record", len(events))
	}
	for _, ev := range events {
		if ev.MessageID != "msg_01" {
			t.Fatalf("usage event %+v carries no message id", ev)
		}
	}
}
