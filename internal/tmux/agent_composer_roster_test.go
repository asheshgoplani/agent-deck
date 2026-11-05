package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ASK-201 (owner, 5 Nov: "I can't send an image from my Mac to my deck"):
// Claude Code lists its background agents and workflows under the composer
// ("← for agents", one row per agent). The owner's conductor runs many, so
// the lower rule sat more than 8 lines above the bottom, the composer was
// not seen and Ctrl+V reached the host's Claude, which has no image.
func TestAgentComposerVisible_AgentRosterUnderTheComposer(t *testing.T) {
	roster := "⏺ Done.\n\n" +
		"────────────────────────────────────────────── work ─\n" +
		"❯ \n" +
		"──────────────────────────────────────────────────────────────\n" +
		"  [user · work] user@host:/x | [model] ctx:19%\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
		"  ⏺ main\n"
	for i := 1; i <= 16; i++ {
		roster += "  ◯ general-purpose  Task " + strings.Repeat("x", i) + "\n"
	}
	if !AgentComposerVisible("claude", roster) {
		t.Fatal("a composer with 16 agent rows under it is still the composer")
	}
	// The recorded panes with a roster (status corpus) are composers too.
	for _, name := range []string{
		"synth-claude-idle-12-subagent-rows.txt",
		"synth-claude-spinner-12-subagents.txt",
		"synth-claude-awaited-agents-above-16-roster-rows.txt",
		"claude-derived-status-audit-18-agent-rows.txt",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "status_corpus", name))
		if err != nil {
			t.Fatal(err)
		}
		if !AgentComposerVisible("claude", string(raw)) {
			t.Errorf("%s: the composer above the agent roster was not seen", name)
		}
	}
}

// The roster rule must not open the gate for an old composer that program
// output (unindented, or behind another rule) has pushed up.
func TestAgentComposerVisible_RosterRuleKeepsOldComposersOut(t *testing.T) {
	composer := "──────────────\n❯ old\n──────────────\n"
	cases := map[string]string{
		"shell output after claude exited": composer + "  footer\n" + strings.Repeat("user@box:~$ ls\n", 12),
		"a dialog below another rule":      composer + strings.Repeat("  row\n", 9) + "──────────────\n  ❯ 1. Yes\n",
		"an endless indented listing":      composer + strings.Repeat("  row\n", 200),
	}
	for name, pane := range cases {
		if AgentComposerVisible("claude", pane) {
			t.Errorf("%s: taken for a composer", name)
		}
	}
}
