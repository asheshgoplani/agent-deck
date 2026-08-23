package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentDeckSkillDocumentsOMPForkSupport(t *testing.T) {
	t.Parallel()

	checks := map[string][]string{
		filepath.Join("..", "..", "skills", "agent-deck", "SKILL.md"): {
			"| `agent-deck session fork <name>` | Fork Claude/OpenCode/Pi/Codex/Oh My Pi conversation |",
			"| `f/F` | Fork Claude/OpenCode/Pi/Codex/Oh My Pi session |",
		},
		filepath.Join("..", "..", "skills", "agent-deck", "references", "tui-reference.md"): {
			"| `f` | Quick fork (Claude/OpenCode/Pi/Codex/Oh My Pi) |",
			"| `F` | Fork with options (Claude/OpenCode/Pi/Codex/Oh My Pi) |",
		},
	}

	for path, required := range checks {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, entry := range required {
			if !strings.Contains(string(content), entry) {
				t.Errorf("%s must advertise Oh My Pi fork support in %q", path, entry)
			}
		}
	}
}
