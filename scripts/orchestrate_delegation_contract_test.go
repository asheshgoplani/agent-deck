package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrchestrateDelegatesAllTaskExecution(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillPath := filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read orchestrate skill: %v", err)
	}

	contract := "The conductor delegates all task execution to child sessions. It only " +
		"decomposes and sequences work, launches and supervises children, routes decisions and results, " +
		"maintains orchestration state, and reports outcomes."
	normalizedSkill := strings.Join(strings.Fields(string(skill)), " ")
	if !strings.Contains(normalizedSkill, contract) {
		t.Fatalf("orchestrate skill missing delegation contract %q", contract)
	}
	if strings.Contains(normalizedSkill, "clean up yourself") {
		t.Fatal("orchestrate skill still assigns cleanup execution to the conductor")
	}
	if !strings.Contains(normalizedSkill, `RUN_ROOT="$ROOT_WT/.agent-deck/$RUN_ID"`) {
		t.Fatal("orchestrate run root is not repository-local")
	}
	if !strings.Contains(normalizedSkill, `RUN_DIR="$RUN_ROOT/orchestrate"`) {
		t.Fatal("orchestrate control-plane directory is not under the run root")
	}
	if !strings.Contains(normalizedSkill, `PLAN_ROOT="$RUN_ROOT/plan"`) {
		t.Fatal("orchestrate plan directory is not under the run root")
	}
	if !strings.Contains(normalizedSkill, `RUN_ROOT=$(cd "$(dirname "$SPEC_PATH")/.." && pwd)`) {
		t.Fatal("orchestrate does not derive the shared run root from an approved design")
	}
	if !strings.Contains(normalizedSkill, `WORKTREES_DIR="$ROOT_WT/.worktrees"`) {
		t.Fatal("orchestrate worktrees directory is not rooted at repository .worktrees")
	}
	if strings.Contains(normalizedSkill, `RUN_DIR="$AD_DIR/orchestrate/`) {
		t.Fatal("orchestrate skill still uses the global run directory")
	}
	if strings.Contains(normalizedSkill, `$RUN_DIR/worktrees/$RUN_ID`) {
		t.Fatal("orchestrate skill places source worktrees inside the run artifact directory")
	}
	if !strings.Contains(normalizedSkill, `RETRO_PATH="$RUN_DIR/retro.md"`) {
		t.Fatal("orchestrate retrospective is not stored with repository-local run artifacts")
	}
	if strings.Contains(normalizedSkill, `$RUN_DIR/inputs/`) {
		t.Fatal("orchestrate still places design inputs in its control-plane directory")
	}
	if strings.Contains(normalizedSkill, `$RUN_DIR/<slug>/tasks/`) {
		t.Fatal("orchestrate still places planner task files in its control-plane directory")
	}
	if !strings.Contains(normalizedSkill, `$PLAN_ROOT/<task-slug>/tasks/`) {
		t.Fatal("orchestrate does not route planner task files through the plan directory")
	}
	if strings.Contains(normalizedSkill, `gh issue view <n> --json body -q .body >`) {
		t.Fatal("orchestrate skill still assigns issue-body fetching to the conductor")
	}

	promptDir := filepath.Join(repoRoot, "skills", "orchestrate", "references", "prompts")
	renderScript := filepath.Join(promptDir, "render.sh")
	tests := []struct {
		name     string
		args     []string
		required []string
	}{
		{
			name: "inspect",
			args: []string{
				"TASK=Inspect repository policy", "ARTIFACT_PATH=/run/landing-policy.md",
			},
			required: []string{
				"Perform this task read-only", "Write the complete result atomically",
				"do not brainstorm, redesign, broaden targets, or launch more sessions",
			},
		},
		{
			name: "cleanup-execute",
			args: []string{
				"REPO_ROOT=/repo", "BASE_REF=origin/main", "CANDIDATE_FILE=/run/candidates.tsv",
				"RESULT_FILE=/run/cleanup-result.tsv",
			},
			required: []string{
				"Execute only the exact cleanup candidates", "one at a time",
				"Refuse any candidate that is dirty", "Write the result atomically",
			},
		},
		{
			name: "cleanup-verify",
			args: []string{
				"REPO_ROOT=/repo", "BASE_REF=origin/main", "CANDIDATE_FILE=/run/candidates.tsv",
				"RESULT_FILE=/run/cleanup-result.tsv", "VERDICT_FILE=/run/cleanup-verdict.md",
			},
			required: []string{
				"Independently verify the cleanup", "Run read-only commands only",
				"VERDICT: clean", "VERDICT: fix-needed",
			},
		},
		{
			name: "retrospective",
			args: []string{
				"RUN_DIR=/repo/.agent-deck/run", "RETRO_PATH=/repo/.agent-deck/run/retro.md",
			},
			required: []string{
				"Run retrospective", "Do not edit any other file, commit, push, or alter run state",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), tt.name+".md")
			args := []string{renderScript, tt.name, outputPath}
			args = append(args, tt.args...)
			if output, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
				t.Fatalf("render prompt: %v\n%s", err, output)
			}
			rendered, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read rendered prompt: %v", err)
			}
			normalized := strings.Join(strings.Fields(string(rendered)), " ")
			if strings.Contains(normalized, "{{") {
				t.Fatalf("rendered prompt contains an unresolved placeholder: %s", rendered)
			}
			for _, required := range tt.required {
				if !strings.Contains(normalized, required) {
					t.Errorf("rendered prompt missing required contract text %q", required)
				}
			}
		})
	}
}

func TestBrainstormingUsesTypedRepositoryLocalRunLayout(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillPath := filepath.Join(repoRoot, "skills", "brainstorming", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read brainstorming skill: %v", err)
	}
	normalizedSkill := strings.Join(strings.Fields(string(skill)), " ")
	if !strings.Contains(normalizedSkill, `RUN_ROOT="$ROOT_WT/.agent-deck/$RUN_ID"`) {
		t.Fatal("brainstorming does not establish a repository-local run root")
	}
	if !strings.Contains(normalizedSkill, `SPEC_PATH="$RUN_ROOT/design/design.md"`) {
		t.Fatal("brainstorming design is not stored in the run's design directory")
	}
	if strings.Contains(normalizedSkill, `.agent-deck/designs/`) {
		t.Fatal("brainstorming still uses the legacy global designs directory")
	}
	for _, forbidden := range []string{"docs/", ".agent-deck/designs/"} {
		if strings.Contains(normalizedSkill, forbidden) {
			t.Fatalf("brainstorming still permits workflow artifacts outside .agent-deck: %q", forbidden)
		}
	}
	if !strings.Contains(normalizedSkill, "Never create a design, plan, task file, prompt, review, report, or retrospective outside `$RUN_ROOT`.") {
		t.Fatal("brainstorming does not state the run-artifact boundary")
	}
}

func TestOrchestrateKeepsAllWorkflowArtifactsInRepositoryRunRoot(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillPath := filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read orchestrate skill: %v", err)
	}
	normalizedSkill := strings.Join(strings.Fields(string(skill)), " ")
	if !strings.Contains(normalizedSkill, "Never create a design, plan, task file, prompt, review, report, or retrospective outside `$RUN_ROOT`.") {
		t.Fatal("orchestrate does not state the run-artifact boundary")
	}
	if !strings.Contains(normalizedSkill, "A file-based design or plan input must already be under `$RUN_ROOT`.") {
		t.Fatal("orchestrate permits file-based workflow inputs outside the run root")
	}
	if strings.Contains(normalizedSkill, "docs/") {
		t.Fatal("orchestrate still references a docs directory outside the run root")
	}
}

func TestWorkflowArtifactsAreNotTrackedOutsideAgentDeck(t *testing.T) {
	repoRoot := filepath.Clean("..")
	for _, legacyDir := range []string{
		"docs/design", "docs/plans", "docs/retros", "docs/superpowers",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, legacyDir)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("workflow artifacts must be ignored under .agent-deck/, not present at %s (stat error: %v)", legacyDir, err)
		}
	}

	agents, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read repository agent guidance: %v", err)
	}
	if !strings.Contains(string(agents), "Keep every workflow artifact under `.agent-deck/<run-id>/`.") {
		t.Fatal("repository agent guidance does not state the workflow-artifact location")
	}
}

func TestCleanupRunsUsesTypedRepositoryRunLayout(t *testing.T) {
	repoRoot := filepath.Clean("..")
	scriptPath := filepath.Join(repoRoot, "skills", "orchestrate", "references", "cleanup-runs.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read cleanup script: %v", err)
	}
	if strings.Contains(string(script), `"${HOME}/.agent-deck/orchestrate"`) {
		t.Fatal("cleanup script still defaults to the legacy global run root")
	}
	if !strings.Contains(string(script), `control="$run/orchestrate"`) {
		t.Fatal("cleanup script does not locate control-plane files below each typed run root")
	}
}

func TestCreateWorktreeUsesRepositoryWorktreesDirectory(t *testing.T) {
	repoRoot := filepath.Clean("..")
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	runDir := filepath.Join(repo, ".agent-deck", "run-test")

	run := func(dir string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, output)
		}
		return strings.TrimSpace(string(output))
	}

	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	run(repo, "git", "init", "-b", "main")
	run(repo, "git", "config", "user.name", "Contract Test")
	run(repo, "git", "config", "user.email", "contract@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	run(repo, "git", "add", "README.md")
	run(repo, "git", "commit", "-m", "initial")

	createScript := filepath.Join(repoRoot, "skills", "orchestrate", "references", "create-worktree.sh")
	got := run(repo, "sh", createScript,
		"--repo", repo,
		"--run-dir", runDir,
		"--run-id", "run-test",
		"--task", "example",
		"--branch", "feature/example",
		"--base", "main",
	)
	lines := strings.Split(got, "\n")
	got = lines[len(lines)-1]
	want := filepath.Join(repo, ".worktrees", "run-test-example")
	got, err = filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("canonicalize reported worktree: %v", err)
	}
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("canonicalize expected worktree: %v", err)
	}
	if got != want {
		t.Fatalf("worktree path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("repository worktree missing: %v", err)
	}
}
