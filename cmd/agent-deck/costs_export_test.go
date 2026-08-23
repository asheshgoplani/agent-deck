package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func openTestCostStore(t *testing.T) (*costs.Store, *statedb.StateDB) {
	t.Helper()
	db, err := statedb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return costs.NewStore(db.DB()), db
}

func TestWriteCostExportJSONStableFieldsAndUnknown(t *testing.T) {
	known := 1.25
	rows := []costs.ExportRow{{SessionID: "s1", Title: "one", Tool: "claude", Model: "claude-sonnet-4-6", Account: "acct", Profile: "work", Events: 2, InputTokens: 3, OutputTokens: 4, CacheReadTokens: 5, CacheWriteTokens: 6, CostUSD: &known, FirstTimestamp: time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC), LastTimestamp: time.Date(2026, 8, 20, 2, 3, 4, 0, time.UTC)}, {SessionID: "s2", Model: "claude-fable-5"}}
	var out bytes.Buffer
	if err := writeCostExport(&out, rows, true); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "title", "tool", "model", "account", "profile", "events", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "first_timestamp", "last_timestamp"} {
		if _, ok := got[0][key]; !ok {
			t.Errorf("missing stable field %q", key)
		}
	}
	if got[1]["cost_usd"] != nil {
		t.Fatalf("unknown cost = %#v, want null", got[1]["cost_usd"])
	}
}

func TestWriteCostExportTableUsesQuestionMarkForUnknown(t *testing.T) {
	var out bytes.Buffer
	if err := writeCostExport(&out, []costs.ExportRow{{SessionID: "s", Model: "claude-fable-5"}}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "?") || strings.Contains(out.String(), "$0.00") {
		t.Fatalf("table = %q", out.String())
	}
}

func TestCostKnownForRange_IgnoresHistoricalUnknown(t *testing.T) {
	store, storage := openTestCostStore(t)
	defer storage.Close()
	pricer := costs.NewPricer(costs.PricerConfig{})
	for _, ev := range []costs.CostEvent{
		{ID: "old-unknown", SessionID: "old", Timestamp: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Model: "unknown-model"},
		{ID: "current-known", SessionID: "new", Timestamp: time.Date(2026, 8, 23, 1, 0, 0, 0, time.UTC), Model: "claude-sonnet-4-6", CostMicrodollars: 1234},
	} {
		if err := store.WriteCostEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	known, err := costKnownForRange(store, time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), pricer)
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Fatal("historical unknown poisoned current known window")
	}
}
