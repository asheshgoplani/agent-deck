package costs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
)

// The transcript below has two top-level assistant records with usage, one
// without, and a subagent transcript whose usage bills to the same deck
// session. Both `costs sync` and the recall usage sink import through the
// same reader and the same dedup key, so running both never double counts.
const syncTranscript = `{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-09-19T10:00:00Z","sessionId":"11111111-1111-4111-8111-111111111111"}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":50,"cache_creation_input_tokens":5}},"uuid":"a1","timestamp":"2026-09-19T10:00:01Z","sessionId":"11111111-1111-4111-8111-111111111111"}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"more"}],"usage":{"input_tokens":0,"output_tokens":0}},"uuid":"a2","timestamp":"2026-09-19T10:00:02Z","sessionId":"11111111-1111-4111-8111-111111111111"}
{"type":"progress","data":{"message":{"message":{"model":"claude-opus-5","usage":{"input_tokens":999,"output_tokens":999}}}},"uuid":"p1"}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"bye"}],"usage":{"input_tokens":7,"output_tokens":3}},"uuid":"a3","timestamp":"2026-09-19T10:00:03Z","sessionId":"11111111-1111-4111-8111-111111111111"}
`

const subagentTranscript = `{"type":"assistant","isSidechain":true,"message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"sub"}],"usage":{"input_tokens":20,"output_tokens":2}},"uuid":"s1","timestamp":"2026-09-19T10:00:05Z","sessionId":"11111111-1111-4111-8111-111111111111"}
`

func TestSyncFromTranscripts_ReadsViaRecallReaderAndDedupes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const sid = "11111111-1111-4111-8111-111111111111"
	proj := filepath.Join(home, ".claude", "projects", "-Users-x-proj")
	if err := os.MkdirAll(filepath.Join(proj, sid, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(syncTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, sid, "subagents", "agent-1.jsonl"), []byte(subagentTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	pricer := costs.NewPricer(costs.PricerConfig{})
	sessions := []costs.SyncSession{
		{InstanceID: "deck-1", ClaudeSessionID: sid, ProjectPath: "/Users/x/proj", Tool: "claude"},
		{InstanceID: "deck-2", ClaudeSessionID: "missing", ProjectPath: "/Users/x/proj", Tool: "claude"},
		{InstanceID: "deck-3", ClaudeSessionID: sid, ProjectPath: "/Users/x/proj", Tool: "codex"},
	}
	res := costs.SyncFromTranscripts(store, pricer, sessions)
	if res.SessionsScanned != 2 || res.EventsImported != 3 || res.EventsSkipped != 0 || len(res.Errors) != 0 {
		t.Fatalf("first sync: %+v", res)
	}
	sum, err := store.TotalBySession("deck-1")
	if err != nil || sum.EventCount != 3 || sum.TotalInputTokens != 127 {
		t.Fatalf("totals: %v %+v", err, sum)
	}
	// Second run: everything is already there.
	res = costs.SyncFromTranscripts(store, pricer, sessions)
	if res.EventsImported != 0 || res.EventsSkipped != 3 {
		t.Fatalf("second sync: %+v", res)
	}
	// The recall sink path uses the same keys, so a sweep after a sync
	// adds nothing either.
	imp := costs.NewUsageImporter(store, pricer)
	events, err := reader.ScanClaudeUsage(context.Background(), filepath.Join(proj, sid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	imported, skipped, errs := imp.Import("deck-1", events)
	if imported != 0 || skipped != 3 || len(errs) != 0 {
		t.Fatalf("sink after sync: %d %d %v", imported, skipped, errs)
	}
}
