package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestWriteCostExportJSONStableFieldsAndUnknown(t *testing.T) {
	known := 1.25
	rows := []costs.ExportRow{{SessionID: "s1", Title: "one", Tool: "claude", Model: "claude-sonnet-4-6", Account: "acct", Profile: "work", Events: 2, InputTokens: 3, OutputTokens: 4, CacheReadTokens: 5, CacheWriteTokens: 6, CostUSD: &known, FirstTimestamp: time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC), LastTimestamp: time.Date(2026, 8, 20, 2, 3, 4, 0, time.UTC)}, {SessionID: "s2", Model: "claude-fable-5"}}
	var out bytes.Buffer
	if err := writeCostExport(&out, rows, true); err != nil { t.Fatal(err) }
	var got []map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil { t.Fatal(err) }
	for _, key := range []string{"session_id", "title", "tool", "model", "account", "profile", "events", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "first_timestamp", "last_timestamp"} {
		if _, ok := got[0][key]; !ok { t.Errorf("missing stable field %q", key) }
	}
	if got[1]["cost_usd"] != nil { t.Fatalf("unknown cost = %#v, want null", got[1]["cost_usd"]) }
}

func TestWriteCostExportTableUsesQuestionMarkForUnknown(t *testing.T) {
	var out bytes.Buffer
	if err := writeCostExport(&out, []costs.ExportRow{{SessionID: "s", Model: "claude-fable-5"}}, false); err != nil { t.Fatal(err) }
	if !strings.Contains(out.String(), "?") || strings.Contains(out.String(), "$0.00") { t.Fatalf("table = %q", out.String()) }
}
