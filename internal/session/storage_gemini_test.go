package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStorage_GeminiAnalyticsPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "sessions.json")
	
	// Create storage
	s := &Storage{
		path:    storagePath,
		profile: "_test",
	}

	// Create instance with analytics
	inst := NewInstanceWithTool("gemini-test", "/tmp", "gemini")
	inst.GeminiSessionID = "abc-123"
	inst.GeminiAnalytics = &GeminiSessionAnalytics{
		InputTokens:   1000,
		OutputTokens:  500,
		TotalTurns:    5,
		Duration:      10 * time.Minute,
		EstimatedCost: 0.05,
	}

	// Save
	err := s.Save([]*Instance{inst})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load
	loadedInstances, err := s.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loadedInstances) != 1 {
		t.Fatalf("Expected 1 instance, got %d", len(loadedInstances))
	}

	loaded := loadedInstances[0]
	if loaded.GeminiAnalytics == nil {
		t.Fatal("GeminiAnalytics should not be nil after load")
	}

	if loaded.GeminiAnalytics.InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000", loaded.GeminiAnalytics.InputTokens)
	}
	if loaded.GeminiAnalytics.Duration != 10*time.Minute {
		t.Errorf("Duration = %v, want 10m", loaded.GeminiAnalytics.Duration)
	}
	if loaded.GeminiAnalytics.EstimatedCost != 0.05 {
		t.Errorf("EstimatedCost = %f, want 0.05", loaded.GeminiAnalytics.EstimatedCost)
	}
}
