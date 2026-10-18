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
			ID: costs.HookCostEventID(inst, record, time.Now().UnixNano()), SessionID: inst, Timestamp: time.Now(),
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
	if got := costs.HookCostEventID("inst-1", "", 42); got != "inst-1_42" {
		t.Fatalf("id = %q, want the old <instance>_<ts> form", got)
	}
}
