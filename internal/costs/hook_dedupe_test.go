package costs_test

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
)

// Review M3: the Stop hook and the recall importer both record a Claude
// turn's usage. The hook's cost event must get the importer's id for the same
// transcript record, so the turn is stored once whichever runs first.
func TestHookAndImporterStoreATurnOnce(t *testing.T) {
	const inst, record = "inst-1", "6f1c0d6e-0000-4000-8000-000000000001"
	usage := reader.Usage{UUID: record, TS: time.Now(), Model: "claude-test", In: 1000, Out: 500}
	hookEvent := func() costs.CostEvent {
		return costs.CostEvent{
			ID: costs.HookCostEventID(inst, "", record, time.Now().UnixNano()), SessionID: inst, Timestamp: time.Now(),
			Model: usage.Model, InputTokens: usage.In, OutputTokens: usage.Out, CostMicrodollars: 1_000_000,
		}
	}
	for _, order := range []string{"hook first", "importer first"} {
		t.Run(order, func(t *testing.T) {
			s := testStore(t)
			importer := costs.NewUsageImporter(s, costs.NewPricer(costs.PricerConfig{}))
			if order == "hook first" {
				_ = s.WriteCostEvent(hookEvent())
				importer.Import(inst, []reader.Usage{usage})
			} else {
				importer.Import(inst, []reader.Usage{usage})
				_ = s.WriteCostEvent(hookEvent())
			}
			sum, err := s.TotalBySession(inst)
			if err != nil {
				t.Fatal(err)
			}
			if sum.EventCount != 1 || sum.TotalInputTokens != 1000 {
				t.Fatalf("turn stored %d times (%d input tokens), want once", sum.EventCount, sum.TotalInputTokens)
			}
		})
	}
}

func TestHookCostEventIDFallsBackWithoutAUUID(t *testing.T) {
	if got := costs.HookCostEventID("inst-1", "", "", 42); got != "inst-1_42" {
		t.Fatalf("id = %q, want the old <instance>_<ts> form", got)
	}
}

// Review follow-up (3): Claude writes one message as several records (one
// per content block) with the same message id and usage. It is counted once.
func TestImporterCountsASplitMessageOnce(t *testing.T) {
	s := testStore(t)
	importer := costs.NewUsageImporter(s, costs.NewPricer(costs.PricerConfig{}))
	now := time.Now()
	var records []reader.Usage
	for i, rec := range []string{"r1", "r2", "r3"} {
		records = append(records, reader.Usage{UUID: rec, MessageID: "msg_01", TS: now.Add(time.Duration(i) * time.Millisecond), Model: "claude-test", In: 1000, Out: 500})
	}
	importer.Import("inst-1", records)
	costs.NewUsageImporter(s, costs.NewPricer(costs.PricerConfig{})).Import("inst-1", records) // a later pass
	sum, err := s.TotalBySession("inst-1")
	if err != nil {
		t.Fatal(err)
	}
	if sum.EventCount != 1 || sum.TotalInputTokens != 1000 {
		t.Fatalf("split message stored %d times (%d input tokens), want once", sum.EventCount, sum.TotalInputTokens)
	}
}

// Rows an earlier version stored per record fold into one on the next
// import of the transcript (costs sync), matched exactly by record uuid.
func TestImporterFoldsLegacyPerRecordRows(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	for _, id := range []string{"inst-1_r1", "inst-1_r2", "inst-1_r3"} {
		if err := s.WriteCostEvent(costs.CostEvent{ID: id, SessionID: "inst-1", Timestamp: now, Model: "claude-test", InputTokens: 1000, OutputTokens: 500}); err != nil {
			t.Fatal(err)
		}
	}
	var records []reader.Usage
	for _, rec := range []string{"r1", "r2", "r3"} {
		records = append(records, reader.Usage{UUID: rec, MessageID: "msg_01", TS: now, Model: "claude-test", In: 1000, Out: 500})
	}
	costs.NewUsageImporter(s, costs.NewPricer(costs.PricerConfig{})).Import("inst-1", records)
	sum, _ := s.TotalBySession("inst-1")
	if sum.EventCount != 1 {
		t.Fatalf("legacy per-record rows left: %d, want 1", sum.EventCount)
	}
	var id string
	if err := s.DB().QueryRow(`SELECT id FROM cost_events WHERE session_id = 'inst-1'`).Scan(&id); err != nil || id != "inst-1_msg_01" {
		t.Fatalf("kept row id = %q (%v), want inst-1_msg_01", id, err)
	}
}

// With message ids the hook and the importer share one id as well.
func TestHookAndImporterShareTheMessageID(t *testing.T) {
	for _, order := range []string{"hook first", "importer first"} {
		t.Run(order, func(t *testing.T) {
			s := testStore(t)
			records := []reader.Usage{
				{UUID: "r1", MessageID: "msg_01", TS: time.Now(), Model: "claude-test", In: 1000, Out: 500},
				{UUID: "r2", MessageID: "msg_01", TS: time.Now(), Model: "claude-test", In: 1000, Out: 500},
			}
			hook := costs.CostEvent{ID: costs.HookCostEventID("inst-1", "msg_01", "r2", time.Now().UnixNano()), SessionID: "inst-1",
				Timestamp: time.Now(), Model: "claude-test", InputTokens: 1000, OutputTokens: 500}
			imp := costs.NewUsageImporter(s, costs.NewPricer(costs.PricerConfig{}))
			if order == "hook first" {
				_ = s.WriteCostEvent(hook)
				imp.Import("inst-1", records)
			} else {
				imp.Import("inst-1", records)
				_ = s.WriteCostEvent(hook)
			}
			if sum, _ := s.TotalBySession("inst-1"); sum.EventCount != 1 {
				t.Fatalf("stored %d times, want once", sum.EventCount)
			}
		})
	}
}
