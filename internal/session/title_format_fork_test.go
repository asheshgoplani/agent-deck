package session

import (
	"testing"
	"time"
)

func TestTitleFormatForkConstructors(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "opencode", "pi"} {
		for _, group := range []string{"", "selected/nested"} {
			t.Run(tool+"/"+group, func(t *testing.T) {
				extraArgsTestEnv(t)
				codexHome := t.TempDir()
				t.Setenv("CODEX_HOME", codexHome)
				parent := NewInstanceWithGroupAndTool("parent", t.TempDir(), "inherited/nested", tool)
				sid := "11111111-2222-3333-4444-555555555555"
				switch tool {
				case "claude":
					parent.ClaudeSessionID, parent.ClaudeDetectedAt = sid, time.Now()
				case "codex":
					seedCodexRollout(t, codexHome, sid)
					parent.CodexSessionID, parent.CodexDetectedAt = sid, time.Now()
				case "opencode":
					parent.OpenCodeSessionID, parent.OpenCodeDetectedAt = "ses_parent_123", time.Now()
				case "pi":
					seedLocalPiSessionFile(t, parent)
				}
				fork, _, err := parent.CreateForkedInstanceForTool("child", group, nil)
				if err != nil {
					t.Fatal(err)
				}
				want := group
				if want == "" {
					want = parent.GroupPath
				}
				if got := fork.GetTmuxSession().GroupPath; got != want {
					t.Fatalf("fork tmux group=%q, want %q", got, want)
				}
			})
		}
	}
}
