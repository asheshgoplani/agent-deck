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

func TestGeminiSessionAnalytics_CalculateCost(t *testing.T) {
	analytics := &GeminiSessionAnalytics{
		InputTokens:  1000000,
		OutputTokens: 1000000,
	}

	// Assuming pricing (USD per 1M tokens):
	// Flash: Input $0.075, Output $0.30
	// Pro:   Input $3.50,  Output $10.50

	cost := analytics.CalculateCost("gemini-1.5-flash")
	// 0.075 + 0.30 = 0.375
	expected := 0.375

	if cost != expected {
		t.Errorf("CalculateCost('gemini-1.5-flash') = %f, want %f", cost, expected)
	}

	costPro := analytics.CalculateCost("gemini-1.5-pro")
	// 3.50 + 10.50 = 14.00
	expectedPro := 14.00
	if costPro != expectedPro {
		t.Errorf("CalculateCost('gemini-1.5-pro') = %f, want %f", costPro, expectedPro)
	}
}
