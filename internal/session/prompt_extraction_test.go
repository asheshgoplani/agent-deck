package session

import (
	"testing"
)

func TestParseClaudeLatestUserPrompt(t *testing.T) {
	jsonlData := `{"sessionId":"sess_1","type":"message","message":{"role":"user","content":"First prompt"},"timestamp":"2026-01-17T00:00:00Z"}
{"sessionId":"sess_1","type":"message","message":{"role":"assistant","content":"Response"},"timestamp":"2026-01-17T00:00:01Z"}
{"sessionId":"sess_1","type":"message","message":{"role":"user","content":"Latest prompt"},"timestamp":"2026-01-17T00:00:02Z"}
{"sessionId":"sess_1","type":"message","message":{"role":"assistant","content":"Thinking..."},"timestamp":"2026-01-17T00:00:03Z"}`

	prompt, err := parseClaudeLatestUserPrompt([]byte(jsonlData))
	if err != nil {
		t.Fatalf("Failed to parse Claude prompt: %v", err)
	}
	if prompt != "Latest prompt" {
		t.Errorf("Expected 'Latest prompt', got %q", prompt)
	}
}

func TestParseGeminiLatestUserPrompt(t *testing.T) {
	jsonData := `{
		"sessionId": "gemini_sess_1",
		"messages": [
			{"id": "1", "type": "user", "content": "First prompt", "timestamp": "2026-01-17T00:00:00Z"},
			{"id": "2", "type": "gemini", "content": "Response", "timestamp": "2026-01-17T00:00:01Z"},
			{"id": "3", "type": "user", "content": "Latest prompt", "timestamp": "2026-01-17T00:00:02Z"}
		]
	}`

	prompt, err := parseGeminiLatestUserPrompt([]byte(jsonData))
	if err != nil {
		t.Fatalf("Failed to parse Gemini prompt: %v", err)
	}
	if prompt != "Latest prompt" {
		t.Errorf("Expected 'Latest prompt', got %q", prompt)
	}
}

func TestParseClaudeLatestUserPrompt_ContentBlocks(t *testing.T) {
	jsonlData := `{"sessionId":"sess_1","type":"message","message":{"role":"user","content":[{"type":"text","text":"Prompt in block"}]},"timestamp":"2026-01-17T00:00:00Z"}`

	prompt, err := parseClaudeLatestUserPrompt([]byte(jsonlData))
	if err != nil {
		t.Fatalf("Failed to parse Claude prompt: %v", err)
	}
	if prompt != "Prompt in block" {
		t.Errorf("Expected 'Prompt in block', got %q", prompt)
	}
}
