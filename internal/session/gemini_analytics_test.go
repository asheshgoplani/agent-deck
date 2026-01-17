package session

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGeminiSessionAnalytics_JSON(t *testing.T) {
	analytics := &GeminiSessionAnalytics{
		InputTokens:   100,
		OutputTokens:  200,
		EstimatedCost: 0.05,
		TotalTurns:    5,
		Duration:      10 * time.Minute,
	}

	data, err := json.Marshal(analytics)
	if err != nil {
		t.Fatalf("Failed to marshal GeminiSessionAnalytics: %v", err)
	}

	var parsed GeminiSessionAnalytics
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal GeminiSessionAnalytics: %v", err)
	}

	if parsed.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", parsed.InputTokens)
	}
	if parsed.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", parsed.OutputTokens)
	}
	if parsed.EstimatedCost != 0.05 {
		t.Errorf("EstimatedCost = %f, want 0.05", parsed.EstimatedCost)
	}
	if parsed.TotalTurns != 5 {
		t.Errorf("TotalTurns = %d, want 5", parsed.TotalTurns)
	}
	if parsed.Duration != 10*time.Minute {
		t.Errorf("Duration = %v, want 10m", parsed.Duration)
	}

	if parsed.TotalTokens() != 300 {
		t.Errorf("TotalTokens = %d, want 300", parsed.TotalTokens())
	}
}
