package session

import (
	"strings"
	"testing"
)

func TestRegistryMatchesSupportedOMPLaunchers(t *testing.T) {
	r := Init(nil)
	for _, cmd := range []string{"omp", "oh-my-pi", "npx @oh-my-pi/pi-coding-agent --model opus"} {
		if got := r.Match(cmd); got != "omp" {
			t.Fatalf("Match(%q) = %q, want omp", cmd, got)
		}
	}
}

func TestOMPOptionsExposeCompleteHarnessSurface(t *testing.T) {
	opts := &OMPOptions{SessionMode: "resume", ResumeID: "s-1", Model: "main", Models: []string{"a", "b"}, SmolModel: "small", SlowModel: "deep", PlanModel: "planner", PrintThoughts: true, ApprovalMode: "write", AutoApprove: true, MaxTime: "10m", Profile: "isolated", FromClaude: true, FromCodex: true}
	got := strings.Join(opts.ToArgs(), " ")
	for _, want := range []string{"--resume s-1", "--model main", "--models a,b", "--smol small", "--slow deep", "--plan planner", "--print-thoughts", "--approval-mode write", "--auto-approve", "--max-time 10m", "--profile isolated", "--from-claude", "--from-codex"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}
}

func TestOMPForkCarriesModelAndHarnessOptions(t *testing.T) {
	parent := NewInstance("parent", "/tmp/project")
	parent.ID, parent.Tool, parent.SSHHost = "parent-id", "omp", "remote"
	if err := parent.SetOMPOptions(&OMPOptions{Model: "review/model", Models: []string{"fast", "slow"}, Profile: "review"}); err != nil {
		t.Fatal(err)
	}
	child, cmd, err := parent.CreateForkedOMPInstanceWithOptions("child", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--model review/model", "--models fast,slow", "--profile review", "--fork"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("fork command %q missing %q", cmd, want)
		}
	}
	if child.GetOMPOptions() == nil || child.GetOMPOptions().Model != "review/model" {
		t.Fatal("fork did not persist inherited OMP options")
	}
}

func TestNewOMPOptionsAppliesRoleAndProfileDefaults(t *testing.T) {
	opts := NewOMPOptions(&UserConfig{OMP: OMPSettings{DefaultModel: "main", DefaultProfile: "work", ApprovalMode: "write", SmolModel: "small", SlowModel: "slow", PlanModel: "plan"}})
	if opts.SessionMode != "continue" || opts.Profile != "work" || opts.SmolModel != "small" || opts.SlowModel != "slow" || opts.PlanModel != "plan" {
		t.Fatalf("defaults not applied: %+v", opts)
	}
}
