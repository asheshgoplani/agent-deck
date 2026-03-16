package costs_test

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestCostPoller_Dedup(t *testing.T) {
	pricer := costs.NewPricer(costs.PricerConfig{})
	collector := costs.NewCollector(pricer)
	poller := costs.NewCostPoller(collector)

	output := "Token count: 1,234 input, 567 output"

	// First poll should return events
	events, err := poller.Poll("gemini", "s1", output)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("first poll: len = %d, want 1", len(events))
	}
	if events[0].InputTokens != 1234 {
		t.Errorf("input = %d, want 1234", events[0].InputTokens)
	}

	// Second poll with same output should be deduped
	events, err = poller.Poll("gemini", "s1", output)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("second poll should be deduped, got %d events", len(events))
	}

	// Different session, same output should NOT be deduped
	events, err = poller.Poll("gemini", "s2", output)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("different session: len = %d, want 1", len(events))
	}
}
