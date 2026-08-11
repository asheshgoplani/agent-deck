package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOrchestrationSkillDeployedVerificationStructure(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillPath := filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md")
	skillBytes, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read orchestration skill: %v", err)
	}
	skill := string(skillBytes)
	normalizedSkill := strings.Join(strings.Fields(skill), " ")

	entrance := strings.Index(skill, "- A **deployed-system verification** request")
	verificationFlow := strings.Index(skill, "## Deployed-system verification")
	deliveryPipeline := strings.Index(skill, "## Per-task pipeline")
	if entrance < 0 || verificationFlow < 0 || deliveryPipeline < 0 {
		t.Fatalf("missing orchestration sections: entrance=%d verification=%d delivery=%d", entrance, verificationFlow, deliveryPipeline)
	}
	if !(entrance < verificationFlow && verificationFlow < deliveryPipeline) {
		t.Fatalf("verification entrance and flow must precede delivery: entrance=%d verification=%d delivery=%d", entrance, verificationFlow, deliveryPipeline)
	}

	entrances := []string{
		"list of tasks/issues (2+)",
		"single small task",
		"single big task, no spec",
		"design/spec document",
		"implementation plan",
		"deployed-system verification",
	}
	for _, entranceName := range entrances {
		if !strings.Contains(skill, entranceName) {
			t.Errorf("skill no longer documents existing entrance %q", entranceName)
		}
	}

	phases := []string{
		"1. **Recon.**",
		"2. **Independent measurement arms.**",
		"3. **Conductor validation and adjudication.**",
		"4. **Consolidated report.**",
	}
	previous := verificationFlow
	for _, phase := range phases {
		position := strings.Index(skill[verificationFlow:deliveryPipeline], phase)
		if position < 0 {
			t.Fatalf("verification flow missing phase %q", phase)
		}
		position += verificationFlow
		if position <= previous {
			t.Fatalf("verification phase %q is out of order", phase)
		}
		previous = position
	}

	requiredContracts := []string{
		"verification-only work does not",
		"with **no assumed edit**: do not enter implementation, pull-request, CI, or deployment stages unless the outcome and authorized scope explicitly permit delivery",
		"Before reading even deciding fields, validate each artifact's expected schema, provenance, producer completion, and freshness against recon",
		"Read only the deciding fields where possible",
		"For a flaky external measurement, preserve and diagnose the first failure evidence, then permit at most **one clean rerun** by default",
		"A second failure is a product `defect` when it demonstrates product behavior, or `inconclusive` when the harness, environment, or license prevents a trustworthy decision",
		"A `pass` is terminal with no edits, pull request, CI run, or deployment",
		"A `defect` enters the delivery pipeline only when the defect is within the authorized scope",
		"An `inconclusive` result terminates honestly with what blocked a trustworthy decision; do not claim success or retry indefinitely",
		"The existing child and conductor rotation/handoff rules apply throughout this flow",
		"verification-only `pass` and `inconclusive` outcomes stop before those stages",
	}
	for _, contract := range requiredContracts {
		if !strings.Contains(normalizedSkill, contract) {
			t.Errorf("skill missing deployed-verification contract %q", contract)
		}
	}

	if !strings.Contains(skill, `cp <agent-deck-repo>/skills/orchestrate/references/rotate-conductor.sh "$RUN_DIR/"`) {
		t.Error("run setup no longer installs the conductor rotation/handoff artifact")
	}

	rotationPath := filepath.Join(repoRoot, "skills", "orchestrate", "references", "rotate-conductor.sh")
	rotationBytes, err := os.ReadFile(rotationPath)
	if err != nil {
		t.Fatalf("read conductor rotation artifact: %v", err)
	}
	rotation := string(rotationBytes)
	rotationContracts := []string{
		`HANDOFF="$D/conductor-handoff.md"`,
		`for f in "$MANIFEST" "$HANDOFF"; do`,
		`if [ ! -s "$f" ]; then`,
		"Read these two files before you do anything else:",
		`agent-deck session set-parent "$cid" "$NEW_ID"`,
		`agent-deck session archive "$SELF_ID"`,
	}
	for _, contract := range rotationContracts {
		if !strings.Contains(rotation, contract) {
			t.Errorf("rotation artifact missing handoff contract %q", contract)
		}
	}
}

func TestExistingDeliveryPromptTemplatesStillRender(t *testing.T) {
	repoRoot := filepath.Clean("..")
	promptDir := filepath.Join(repoRoot, "skills", "orchestrate", "references", "prompts")
	renderScript := filepath.Join(promptDir, "render.sh")
	unresolved := regexp.MustCompile(`\{\{(?:include:)?[A-Za-z0-9_.-]+\}\}`)

	tests := []struct {
		name     string
		args     []string
		required []string
	}{
		{
			name: "plan",
			args: []string{
				"SPEC_PATH=/tmp/approved-design.md",
				"TASK_DIR=/tmp/orchestrate/task",
			},
			required: []string{"/tmp/approved-design.md", "/tmp/orchestrate/task/plan.md"},
		},
		{
			name: "impl",
			args: []string{
				"RUN_DIR=/tmp/orchestrate/run",
				"SPEC_BLOCK=Approved requirements block",
				"TASK_SLUG=contract-task",
				"TASK_TITLE=Contract task",
			},
			required: []string{"Task: Contract task", "Approved requirements block", "/tmp/orchestrate/run/contract-task/handoff.md"},
		},
		{
			name: "review-full",
			args: []string{
				"AGENT_DECK_REPO=/tmp/agent-deck",
				"BASE_BRANCH=main",
				"BASELINE=baseline: none",
				"SPEC_BLOCK=Review requirements block",
				"VERDICT_FILE=/tmp/orchestrate/review-r1.md",
			},
			required: []string{"git merge-base main HEAD", "Review requirements block", "/tmp/orchestrate/review-r1.md"},
		},
		{
			name: "review-incremental",
			args: []string{
				"AGENT_DECK_REPO=/tmp/agent-deck",
				"BASELINE=baseline: none",
				"PREVIOUS_FINDINGS=One previous finding",
				"REVIEWED_SHA=0123456789abcdef",
				"SPEC_BLOCK=Review requirements block",
				"VERDICT_FILE=/tmp/orchestrate/review-r2.md",
			},
			required: []string{"One previous finding", "git diff 0123456789abcdef...HEAD", "/tmp/orchestrate/review-r2.md"},
		},
		{
			name: "fix",
			args: []string{
				"FINDINGS=Fix this concrete finding",
				"ROUND=2",
			},
			required: []string{"Review round 2", "Fix this concrete finding"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), tt.name+".md")
			args := []string{renderScript, tt.name, outputPath}
			args = append(args, tt.args...)
			if output, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
				t.Fatalf("render existing %s template: %v\n%s", tt.name, err, output)
			}

			rendered, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read rendered %s prompt: %v", tt.name, err)
			}
			if match := unresolved.Find(rendered); match != nil {
				t.Fatalf("rendered %s prompt contains unresolved token %q", tt.name, match)
			}
			for _, required := range tt.required {
				if !strings.Contains(string(rendered), required) {
					t.Errorf("rendered %s prompt missing substituted contract %q", tt.name, required)
				}
			}
		})
	}
}
