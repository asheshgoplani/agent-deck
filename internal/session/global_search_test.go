package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseClaudeJSONL(t *testing.T) {
	jsonl := `{"sessionId":"abc-123","type":"user","message":{"role":"user","content":"Hello world"},"timestamp":"2025-01-15T10:00:00Z","cwd":"/Users/test/project"}
{"sessionId":"abc-123","type":"assistant","message":{"role":"assistant","content":"Hi there!"},"timestamp":"2025-01-15T10:00:01Z"}`

	entry, err := parseClaudeJSONL("test.jsonl", []byte(jsonl), true)
	if err != nil {
		t.Fatalf("Failed to parse JSONL: %v", err)
	}

	if entry.SessionID != "abc-123" {
		t.Errorf("Expected sessionId 'abc-123', got %q", entry.SessionID)
	}
	if entry.CWD != "/Users/test/project" {
		t.Errorf("Expected CWD '/Users/test/project', got %q", entry.CWD)
	}
	if !strings.Contains(entry.ContentString(), "Hello world") {
		t.Error("Content should contain 'Hello world'")
	}
	if !strings.Contains(entry.ContentString(), "Hi there!") {
		t.Error("Content should contain 'Hi there!'")
	}
}

func TestParseClaudeJSONLWithContentBlocks(t *testing.T) {
	// Test parsing content that is an array of blocks (common in Claude responses)
	jsonl := `{"sessionId":"block-test","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"First block"},{"type":"text","text":"Second block"}]}}`

	entry, err := parseClaudeJSONL("test.jsonl", []byte(jsonl), true)
	if err != nil {
		t.Fatalf("Failed to parse JSONL: %v", err)
	}

	if entry.SessionID != "block-test" {
		t.Errorf("Expected sessionId 'block-test', got %q", entry.SessionID)
	}
	if !strings.Contains(entry.ContentString(), "First block") {
		t.Error("Content should contain 'First block'")
	}
	if !strings.Contains(entry.ContentString(), "Second block") {
		t.Error("Content should contain 'Second block'")
	}
}

func TestSearchEntryMatch(t *testing.T) {
	entry := &SearchEntry{
		SessionID: "abc-123",
	}
	entry.setContent([]byte("discussing react hooks implementation"))

	matches := entry.Match("react")
	if len(matches) == 0 {
		t.Error("Expected match for 'react'")
	}
	if len(matches) > 0 && matches[0].Start != 11 {
		t.Errorf("Expected match start at 11, got %d", matches[0].Start)
	}
}

func TestSearchEntryMatchMultiple(t *testing.T) {
	entry := &SearchEntry{
		SessionID: "abc-123",
	}
	entry.setContent([]byte("react react react"))

	matches := entry.Match("react")
	if len(matches) != 3 {
		t.Errorf("Expected 3 matches for 'react', got %d", len(matches))
	}
}

func TestSearchEntryMatchCaseInsensitive(t *testing.T) {
	entry := &SearchEntry{
		SessionID: "abc-123",
	}
	entry.setContent([]byte("React REACT react"))

	matches := entry.Match("REACT")
	if len(matches) != 3 {
		t.Errorf("Expected 3 matches for 'REACT' (case insensitive), got %d", len(matches))
	}
}

func TestSearchEntryGetSnippet(t *testing.T) {
	content := "This is a long piece of content where we are discussing react hooks and how they work in modern web applications."
	entry := &SearchEntry{
		SessionID: "abc-123",
	}
	entry.setContent([]byte(content))

	snippet := entry.GetSnippet("react", 20)
	if !strings.Contains(snippet, "react") {
		t.Error("Snippet should contain the search term 'react'")
	}
	// Snippet should be shorter than full content
	if len(snippet) >= len(content) {
		t.Error("Snippet should be shorter than full content")
	}
}

func TestSearchEntryGetSnippetNoMatch(t *testing.T) {
	content := "This is some content without the search term"
	entry := &SearchEntry{
		SessionID: "abc-123",
	}
	entry.setContent([]byte(content))

	snippet := entry.GetSnippet("nonexistent", 20)
	// Should return beginning of content when no match
	if len(snippet) == 0 {
		t.Error("Snippet should not be empty even without match")
	}
}

func TestDetectTier(t *testing.T) {
	tests := []struct {
		size     int64
		expected SearchTier
	}{
		{50 * 1024 * 1024, TierInstant},   // 50MB -> instant
		{99 * 1024 * 1024, TierInstant},   // 99MB -> instant
		{100 * 1024 * 1024, TierBalanced}, // 100MB -> balanced
		{200 * 1024 * 1024, TierBalanced}, // 200MB -> balanced
	}

	for _, tc := range tests {
		result := DetectTier(tc.size)
		if result != tc.expected {
			t.Errorf("DetectTier(%d) = %v, want %v", tc.size, result, tc.expected)
		}
	}
}

func TestTierName(t *testing.T) {
	if TierName(TierInstant) != "instant" {
		t.Errorf("TierName(TierInstant) = %q, want 'instant'", TierName(TierInstant))
	}
	if TierName(TierBalanced) != "balanced" {
		t.Errorf("TierName(TierBalanced) = %q, want 'balanced'", TierName(TierBalanced))
	}
}

func TestGlobalSearchIndexInstantTier(t *testing.T) {
	// Create temp directory with test JSONL files
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "projects", "-Users-test-project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	// Create test session file (UUID format filename)
	jsonl := `{"sessionId":"a1b2c3d4-e5f6-7890-abcd-ef1234567890","type":"user","message":{"role":"user","content":"implement react hooks"},"cwd":"/Users/test/project"}
{"sessionId":"a1b2c3d4-e5f6-7890-abcd-ef1234567890","type":"assistant","message":{"role":"assistant","content":"I'll help you implement react hooks for state management."}}`

	if err := os.WriteFile(filepath.Join(projectDir, "a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create another session
	jsonl2 := `{"sessionId":"b2c3d4e5-f6a7-8901-bcde-f23456789012","type":"user","message":{"role":"user","content":"fix the database connection"},"cwd":"/Users/test/project"}
{"sessionId":"b2c3d4e5-f6a7-8901-bcde-f23456789012","type":"assistant","message":{"role":"assistant","content":"Let me help fix that database issue."}}`

	if err := os.WriteFile(filepath.Join(projectDir, "b2c3d4e5-f6a7-8901-bcde-f23456789012.jsonl"), []byte(jsonl2), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create index
	config := GlobalSearchSettings{
		Enabled:        boolPtr(true),
		Tier:           "auto",
		MemoryLimitMB:  100,
		RecentDays:     0, // All sessions
		IndexRateLimit: 100,
	}

	index, err := NewGlobalSearchIndex(tmpDir, config)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer index.Close()

	// Wait for initial load
	time.Sleep(200 * time.Millisecond)

	// Verify entry count
	if index.EntryCount() != 2 {
		t.Errorf("Expected 2 entries, got %d", index.EntryCount())
	}

	// Test search
	results := index.Search("react hooks")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'react hooks', got %d", len(results))
	}
	if len(results) > 0 && results[0].Entry.SessionID != "a1b2c3d4-e5f6-7890-abcd-ef1234567890" {
		t.Errorf("Expected session 'a1b2c3d4-e5f6-7890-abcd-ef1234567890', got %q", results[0].Entry.SessionID)
	}

	// Test search for database
	results2 := index.Search("database")
	if len(results2) != 1 {
		t.Errorf("Expected 1 result for 'database', got %d", len(results2))
	}
}

func TestGlobalSearchIndexFuzzyMatch(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "projects", "-Users-test-project")
	_ = os.MkdirAll(projectDir, 0755)

	jsonl := `{"sessionId":"c3d4e5f6-a7b8-9012-cdef-345678901234","type":"user","message":{"role":"user","content":"authentication system implementation"}}`
	_ = os.WriteFile(filepath.Join(projectDir, "c3d4e5f6-a7b8-9012-cdef-345678901234.jsonl"), []byte(jsonl), 0644)

	config := GlobalSearchSettings{Enabled: boolPtr(true), Tier: "auto", MemoryLimitMB: 100, IndexRateLimit: 100}
	index, err := NewGlobalSearchIndex(tmpDir, config)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer index.Close()
	time.Sleep(200 * time.Millisecond)

	// Test fuzzy match (typo)
	results := index.FuzzySearch("authentcation") // missing 'i'
	if len(results) == 0 {
		t.Error("Fuzzy search should find 'authentication' with typo 'authentcation'")
	}
}

func TestGlobalSearchIndexDisabled(t *testing.T) {
	config := GlobalSearchSettings{Enabled: boolPtr(false)}
	index, err := NewGlobalSearchIndex("/tmp", config)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if index != nil {
		t.Error("Index should be nil when disabled")
	}
}

func TestGlobalSearchIndexEmptyQuery(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "projects", "-Users-test-project")
	_ = os.MkdirAll(projectDir, 0755)

	config := GlobalSearchSettings{Enabled: boolPtr(true), Tier: "auto", MemoryLimitMB: 100, IndexRateLimit: 100}
	index, _ := NewGlobalSearchIndex(tmpDir, config)
	if index == nil {
		t.Fatal("Index should not be nil")
	}
	defer index.Close()

	results := index.Search("")
	if results != nil {
		t.Error("Empty query should return nil")
	}
}

func TestGlobalSearchIndexBalancedTier(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "projects", "-Users-test-project")
	_ = os.MkdirAll(projectDir, 0755)

	// Create multiple test session files
	for i := 0; i < 5; i++ {
		sessionID := fmt.Sprintf("d4e5f6a7-b8c9-0123-def4-%012d", i)
		jsonl := fmt.Sprintf(`{"sessionId":"%s","type":"user","message":{"role":"user","content":"content for session %d with unique keyword%d"}}`, sessionID, i, i)
		_ = os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(jsonl), 0644)
	}

	// Force balanced tier
	config := GlobalSearchSettings{
		Enabled:        boolPtr(true),
		Tier:           "balanced",
		MemoryLimitMB:  1, // Very low limit
		RecentDays:     0,
		IndexRateLimit: 100,
	}

	index, err := NewGlobalSearchIndex(tmpDir, config)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer index.Close()

	time.Sleep(200 * time.Millisecond)

	if index.GetTier() != TierBalanced {
		t.Errorf("Expected balanced tier, got %v", TierName(index.GetTier()))
	}

	// Verify entries loaded
	if index.EntryCount() != 5 {
		t.Errorf("Expected 5 entries, got %d", index.EntryCount())
	}

	// Search should still work
	results := index.Search("keyword3")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'keyword3', got %d", len(results))
	}
}

func TestGlobalSearchIndexTierAutoDetect(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "projects", "-Users-test-project")
	_ = os.MkdirAll(projectDir, 0755)

	// Small dataset should auto-detect as instant
	config := GlobalSearchSettings{
		Enabled:        boolPtr(true),
		Tier:           "auto",
		MemoryLimitMB:  100,
		IndexRateLimit: 100,
	}

	index, _ := NewGlobalSearchIndex(tmpDir, config)
	if index == nil {
		t.Fatal("Index should not be nil")
	}
	defer index.Close()

	// Small/empty data should be instant tier
	if index.GetTier() != TierInstant {
		t.Errorf("Expected instant tier for small data, got %v", TierName(index.GetTier()))
	}
}
