package session

import (
	"strings"
	"testing"
)

func syntheticTransferExport(tool string) *ContextExport {
	return &ContextExport{
		Manifest: ContextManifest{
			Version: 1,
			Source: ContextSourceIdentity{
				InstanceID: "source-instance-42", Tool: tool, SessionID: "source-session-42",
				Account: "source", ProjectPath: "/workspace/repo", Title: "build-api", GroupPath: "projects/api",
			},
			Artifact:   ContextArtifact{Format: tool + "-jsonl", SourceBytes: 2048, PayloadBytes: 2048, Truncated: false},
			Continuity: "native",
		},
		Payload: []byte(`{"type":"message","text":"synthetic context"}`),
	}
}

// TestBuildFreshTargetLaunchPlan_AllCrossHarnessDirections pins the complete
// six-direction matrix. These are pure fixture tests: a plan never starts a
// process, creates a target instance, or changes the source.
func TestBuildFreshTargetLaunchPlan_AllCrossHarnessDirections(t *testing.T) {
	for _, tc := range []struct {
		source, target, account, home string
		wantFlag                      string
	}{
		{"claude", "codex", "codex-work", "/tmp/codex-work", "-C"},
		{"claude", "pi", "", "", "--session-dir"},
		{"codex", "claude", "claude-work", "/tmp/claude-work", "--session-id"},
		{"codex", "pi", "", "", "--session-dir"},
		{"pi", "claude", "claude-work", "/tmp/claude-work", "--session-id"},
		{"pi", "codex", "codex-work", "/tmp/codex-work", "-C"},
	} {
		t.Run(tc.source+"-to-"+tc.target, func(t *testing.T) {
			plan, err := BuildFreshTargetLaunchPlan(syntheticTransferExport(tc.source), FreshTargetLaunchOptions{
				TargetHarness: tc.target, TargetAccount: tc.account, TargetAccountHome: tc.home,
			})
			if err != nil {
				t.Fatalf("BuildFreshTargetLaunchPlan() error = %v", err)
			}
			if plan.Executable || plan.Ready {
				t.Fatal("cross-harness plan must not claim executable readiness")
			}
			if plan.Target.InstanceID == plan.Source.InstanceID {
				t.Fatal("target instance identity must be distinct from source")
			}
			if !strings.Contains(strings.Join(plan.NativeArgs, " "), tc.wantFlag) {
				t.Fatalf("native args %v do not contain target flag %q", plan.NativeArgs, tc.wantFlag)
			}
			if tc.target == "pi" && plan.SessionDir == "" {
				t.Fatal("Pi target has no durable planned instance session directory")
			}
			if tc.target == "pi" && plan.Target.NativeSessionPath != "" {
				t.Fatal("Pi target predicted a native filename despite Pi choosing it at launch")
			}
			if plan.Target.ProjectPath != plan.Source.ProjectPath || plan.Target.Title != plan.Source.Title || plan.Target.GroupPath != plan.Source.GroupPath {
				t.Fatalf("compatible metadata was not preserved: target=%+v source=%+v", plan.Target, plan.Source)
			}
			if len(plan.RemainingIntegration) == 0 || len(plan.Fidelity.Exclusions) == 0 {
				t.Fatal("plan must disclose integration work and fidelity exclusions")
			}
		})
	}
}

func TestBuildFreshTargetLaunchPlan_PiNamedAccountRefused(t *testing.T) {
	_, err := BuildFreshTargetLaunchPlan(syntheticTransferExport("claude"), FreshTargetLaunchOptions{
		TargetHarness: "pi", TargetAccount: "named-pi",
	})
	if err == nil || !strings.Contains(err.Error(), "Pi does not support named accounts") || !strings.Contains(err.Error(), "named-pi") {
		t.Fatalf("named Pi account error = %v, want semantic Pi named-account refusal", err)
	}
}

func TestBuildFreshTargetLaunchPlan_StableTargetIdentity(t *testing.T) {
	opts := FreshTargetLaunchOptions{TargetHarness: "claude", TargetAccount: "work", TargetAccountHome: "/tmp/claude-work"}
	first, err := BuildFreshTargetLaunchPlan(syntheticTransferExport("codex"), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildFreshTargetLaunchPlan(syntheticTransferExport("codex"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Target.InstanceID != second.Target.InstanceID || first.Target.SessionID != second.Target.SessionID {
		t.Fatalf("target identity is unstable: first=%+v second=%+v", first.Target, second.Target)
	}
}
