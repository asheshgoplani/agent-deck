package session

import (
	"os"
	"strings"
	"testing"
)

func TestRestartClaudeFastPathReconcilesLoadoutBeforeRespawn(t *testing.T) {
	src, err := os.ReadFile("instance.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != \"\" && i.tmuxSession != nil && i.tmuxSession.Exists()")
	if start < 0 {
		t.Fatal("Claude restart fast path not found")
	}
	end := strings.Index(text[start:], "if err := i.tmuxSession.RespawnPane(resumeCmd)")
	if end < 0 {
		t.Fatal("Claude restart respawn not found")
	}
	fastPathBeforeRespawn := text[start : start+end]
	if !strings.Contains(fastPathBeforeRespawn, "ApplyConfiguredLoadout(i)") {
		t.Fatal("Claude restart fast path must reconcile configured loadout before respawn")
	}
}
