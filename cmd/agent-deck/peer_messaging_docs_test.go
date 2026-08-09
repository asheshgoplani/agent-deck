package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeOrchestrationDocsDefineNativePeerMessagingFallback(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "conductor", "conductor-claude.md"),
		filepath.Join("..", "..", "skills", "fleet", "SKILL.md"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(raw)
		for _, required := range []string{"ListAgents", "SendMessage", "agent-deck session send", "durable"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s lacks required native-peer routing term %q", path, required)
			}
		}
		if !strings.Contains(text, "not completion proof") && !strings.Contains(text, "not proof of completion") {
			t.Errorf("%s must state that native delivery is not completion proof", path)
		}
	}
}
