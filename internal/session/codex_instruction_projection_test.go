package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPortableCodexProjectionOmitsInjectedUserInstructions(t *testing.T) {
	var lines []string
	for _, text := range []string{"# AGENTS.md instructions for /project\n<INSTRUCTIONS>PRIVATE_POLICY_SENTINEL</INSTRUCTIONS>\n<environment_context>ENV_SENTINEL</environment_context>", "<environment_context>ENV_SENTINEL</environment_context>", "<permissions instructions>POLICY_SENTINEL</permissions instructions>", "Use sage-green walls, oak desk, option B because quieter; budget under 900 euros."} {
		b, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}}})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	got, ok := projectPortableContext("codex", []byte(strings.Join(lines, "\n")))
	if !ok || !strings.Contains(got, "sage-green") || strings.Contains(got, "SENTINEL") {
		t.Fatalf("wrong portable projection: %q", got)
	}
}
