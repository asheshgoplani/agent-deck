package session

import (
	"strings"
	"testing"
)

func TestClaudeHandoffRejectsTraversalIdentityBeforeLookup(t *testing.T) {
	for _, id := range []string{"../../foreign", "nested/thread", `nested\thread`, "thread*", "thread\x00"} {
		t.Run(id, func(t *testing.T) {
			inst := &Instance{Tool: "claude", ClaudeSessionID: id, ProjectPath: t.TempDir()}
			if _, err := canonicalClaudeExactTranscriptPath(inst); err == nil || !strings.Contains(err.Error(), "identity") {
				t.Fatalf("canonical lookup must reject malformed identity: %v", err)
			}
			if prompt, _, err := BuildClaudeToCodexHandoffPrompt(inst, 1000); err == nil || prompt != "" {
				t.Fatalf("handoff accepted malformed identity: prompt=%q err=%v", prompt, err)
			}
		})
	}
}
