package costs_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func testStore(t *testing.T) *costs.Store {
	t.Helper()
	dir := t.TempDir()
	sdb, err := statedb.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sdb.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return costs.NewStore(sdb.DB())
}

func TestStore_WriteThenRead(t *testing.T) {
	s := testStore(t)

	ev := costs.CostEvent{
		ID:               "evt-1",
		SessionID:        "sess-1",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		InputTokens:      4231,
		OutputTokens:     1892,
		CacheReadTokens:  3500,
		CacheWriteTokens: 0,
		CostMicrodollars: 41193,
	}

	if err := s.WriteCostEvent(ev); err != nil {
		t.Fatalf("WriteCostEvent: %v", err)
	}

	summary, err := s.TotalBySession("sess-1")
	if err != nil {
		t.Fatalf("TotalBySession: %v", err)
	}
	if summary.TotalCostMicrodollars != 41193 {
		t.Errorf("cost = %d, want 41193", summary.TotalCostMicrodollars)
	}
	if summary.TotalInputTokens != 4231 {
		t.Errorf("input = %d, want 4231", summary.TotalInputTokens)
	}
	if summary.EventCount != 1 {
		t.Errorf("count = %d, want 1", summary.EventCount)
	}
}

func TestStore_TotalToday(t *testing.T) {
	s := testStore(t)
	now := time.Now()

	s.WriteCostEvent(costs.CostEvent{
		ID: "e1", SessionID: "s1", Timestamp: now,
		Model: "claude-sonnet-4-6", InputTokens: 1000, OutputTokens: 500,
		CostMicrodollars: 10000,
	})
	s.WriteCostEvent(costs.CostEvent{
		ID: "e2", SessionID: "s2", Timestamp: now,
		Model: "gemini-2.5-pro", InputTokens: 2000, OutputTokens: 1000,
		CostMicrodollars: 20000,
	})

	summary, err := s.TotalToday()
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalCostMicrodollars != 30000 {
		t.Errorf("today total = %d, want 30000", summary.TotalCostMicrodollars)
	}
	if summary.EventCount != 2 {
		t.Errorf("count = %d, want 2", summary.EventCount)
	}
}

func TestStore_CostByModel(t *testing.T) {
	s := testStore(t)
	now := time.Now()

	s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "claude-sonnet-4-6", CostMicrodollars: 10000})
	s.WriteCostEvent(costs.CostEvent{ID: "e2", SessionID: "s1", Timestamp: now, Model: "claude-sonnet-4-6", CostMicrodollars: 5000})
	s.WriteCostEvent(costs.CostEvent{ID: "e3", SessionID: "s2", Timestamp: now, Model: "gemini-2.5-pro", CostMicrodollars: 20000})

	byModel, err := s.CostByModel()
	if err != nil {
		t.Fatal(err)
	}
	if byModel["claude-sonnet-4-6"] != 15000 {
		t.Errorf("claude = %d, want 15000", byModel["claude-sonnet-4-6"])
	}
	if byModel["gemini-2.5-pro"] != 20000 {
		t.Errorf("gemini = %d, want 20000", byModel["gemini-2.5-pro"])
	}
}

func TestStore_TopSessionsByCost(t *testing.T) {
	s := testStore(t)
	now := time.Now()

	s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "m", CostMicrodollars: 50000})
	s.WriteCostEvent(costs.CostEvent{ID: "e2", SessionID: "s2", Timestamp: now, Model: "m", CostMicrodollars: 30000})
	s.WriteCostEvent(costs.CostEvent{ID: "e3", SessionID: "s3", Timestamp: now, Model: "m", CostMicrodollars: 70000})

	top, err := s.TopSessionsByCost(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("len = %d, want 2", len(top))
	}
	if top[0].SessionID != "s3" {
		t.Errorf("top[0] = %s, want s3", top[0].SessionID)
	}
	if top[1].SessionID != "s1" {
		t.Errorf("top[1] = %s, want s1", top[1].SessionID)
	}
}

func TestStore_Retention(t *testing.T) {
	s := testStore(t)
	old := time.Now().AddDate(0, 0, -100)
	recent := time.Now()

	s.WriteCostEvent(costs.CostEvent{ID: "old", SessionID: "s1", Timestamp: old, Model: "m", CostMicrodollars: 10000})
	s.WriteCostEvent(costs.CostEvent{ID: "new", SessionID: "s1", Timestamp: recent, Model: "m", CostMicrodollars: 20000})

	deleted, err := s.PurgeOlderThan(90)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	summary, _ := s.TotalBySession("s1")
	if summary.TotalCostMicrodollars != 20000 {
		t.Errorf("remaining = %d, want 20000", summary.TotalCostMicrodollars)
	}
}

func TestFormatUSD(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "$0.00"},
		{1_000_000, "$1.00"},
		{12_345_678, "$12.35"},
		{500, "$0.00"},
	}
	for _, tt := range tests {
		got := costs.FormatUSD(tt.input)
		if got != tt.want {
			t.Errorf("FormatUSD(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
