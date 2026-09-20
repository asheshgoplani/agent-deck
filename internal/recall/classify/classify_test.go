package classify

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMessage(t *testing.T) {
	cases := []struct {
		name string
		text string
		s    Signals
		want Class
	}{
		{"prompt", "fix the flaky auth test", Signals{}, Prompt},
		{"assistant", "Looking at the test", Signals{Assistant: true}, Assist},
		{"tool", "", Signals{ToolResult: true}, Tool},
		{"tool error", "", Signals{ToolResult: true, IsError: true}, Error},
		{"heartbeat", "[HEARTBEAT] tick 12", Signals{}, Heartbeat},
		{"event", "[EVENT] child done", Signals{}, Heartbeat},
		{"skill", "Base directory for this skill: /x/skills/agent-deck/\nrest", Signals{}, SkillLoad},
		{"interrupt", "[Request interrupted by user]", Signals{}, Interrupt},
		{"interrupt tool", "[Request interrupted by user for tool use]", Signals{}, Interrupt},
		{"reminder", "<system-reminder>ctx</system-reminder>", Signals{}, Meta},
		{"command", "<command-name>/help</command-name>", Signals{}, Meta},
		{"meta flag", "anything", Signals{IsMeta: true}, Meta},
		{"compact", "This session is being continued", Signals{CompactSummary: true}, CompactSummary},
	}
	for _, c := range cases {
		if got := Message(c.text, c.s); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestSkillName(t *testing.T) {
	cases := map[string]string{
		"":                                       "",
		"Base directory for this skill: /a/b/c/": "c",
		"Base directory for this skill: /a/b/c\nmore": "c",
		"Base directory for this skill:  \n":          "unknown",
	}
	for in, want := range cases {
		if got := SkillName(in); got != want {
			t.Errorf("SkillName(%q) = %q want %q", in, got, want)
		}
	}
}

func TestString(t *testing.T) {
	if Heartbeat.String() != "heartbeat" || Class(99).String() != "unknown" {
		t.Fatal("String")
	}
}

// TestRulesJSONSharedWithDistill is the drift guard: the embedded rules and
// the copy distill.py reads beside itself must be the same bytes.
func TestRulesJSONSharedWithDistill(t *testing.T) {
	root := moduleRootDir(t)
	sibling, err := os.ReadFile(filepath.Join(root, "skills", "agent-deck", "scripts", "self-improvement", "rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sibling, RulesJSON()) {
		t.Fatal("skills/agent-deck/scripts/self-improvement/rules.json differs from internal/recall/classify/rules.json: copy one over the other")
	}
	r := Current()
	if !r.IsNoise("attachment") || r.IsNoise("user") || r.Clip.AssistPreviewChars != 500 || r.LostTime.SlowCallMS <= 0 || r.Outcome.HintKey != "outcome" {
		t.Fatalf("rules: %+v", r)
	}
	if _, err := ParseRules([]byte(`{"version":1}`)); err == nil {
		t.Fatal("a rules.json without the markers must be refused")
	}
}

func moduleRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
