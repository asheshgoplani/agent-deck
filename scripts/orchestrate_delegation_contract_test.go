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
	if !strings.Contains(normalizedSkill, `RUN_DIR="$ROOT_WT/.agent-deck/$RUN_ID"`) {
		t.Fatal("orchestrate run directory is not repository-local")
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
