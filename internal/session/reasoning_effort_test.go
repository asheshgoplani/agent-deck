package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyLaunchReasoningEffort_ClaudePersistsAndLaunches(t *testing.T) {
	extraArgsTestEnv(t)
	inst := NewInstanceWithTool("effort-claude", t.TempDir(), "claude")

	if err := inst.ApplyLaunchReasoningEffort("high"); err != nil {
		t.Fatalf("ApplyLaunchReasoningEffort(high): %v", err)
	}
	if got := inst.LaunchReasoningEffort(); got != "high" {
		t.Fatalf("LaunchReasoningEffort() = %q, want high", got)
	}
	if cmd := inst.buildClaudeCommand("claude"); !strings.Contains(cmd, "--effort high") {
		t.Fatalf("Claude launch command missing --effort high:\n%s", cmd)
	}

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	revived := &Instance{}
	if err := json.Unmarshal(data, revived); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := revived.LaunchReasoningEffort(); got != "high" {
		t.Fatalf("revived LaunchReasoningEffort() = %q, want high", got)
	}
}

func TestApplyLaunchReasoningEffort_CodexPersistsAndLaunches(t *testing.T) {
	inst := NewInstanceWithTool("effort-codex", t.TempDir(), "codex")

	if err := inst.ApplyLaunchReasoningEffort("xhigh"); err != nil {
		t.Fatalf("ApplyLaunchReasoningEffort(xhigh): %v", err)
	}
	if got := inst.LaunchReasoningEffort(); got != "xhigh" {
		t.Fatalf("LaunchReasoningEffort() = %q, want xhigh", got)
	}
	cmd := inst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "--config model_reasoning_effort=xhigh") {
		t.Fatalf("Codex launch command missing reasoning override:\n%s", cmd)
	}
}

func TestApplyLaunchReasoningEffort_ValidatesPerToolValues(t *testing.T) {
	tests := []struct {
		tool   string
		effort string
	}{
		{tool: "claude", effort: "minimal"},
		{tool: "codex", effort: "max"},
		{tool: "shell", effort: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.tool+"/"+tt.effort, func(t *testing.T) {
			inst := NewInstanceWithTool("invalid-effort", t.TempDir(), tt.tool)
			if err := inst.ApplyLaunchReasoningEffort(tt.effort); err == nil {
				t.Fatalf("ApplyLaunchReasoningEffort(%q) for %s succeeded, want validation error", tt.effort, tt.tool)
			}
		})
	}
}

func TestLaunchReasoningEffortsForTool(t *testing.T) {
	if got := LaunchReasoningEffortsForTool("claude"); strings.Join(got, ",") != "low,medium,high,xhigh,max" {
		t.Fatalf("Claude efforts = %v", got)
	}
	if got := LaunchReasoningEffortsForTool("codex"); strings.Join(got, ",") != "minimal,low,medium,high,xhigh" {
		t.Fatalf("Codex efforts = %v", got)
	}
}
