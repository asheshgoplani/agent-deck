package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The fan-out line must read the same in the identity block, the skill's
// worker prompt conventions, the config reference and launch-subagent.sh.
func TestFanoutHint_DocsAndScriptCarryTheLine(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"skills/agent-deck/references/sub-agents.md",
		"skills/agent-deck/references/config-reference.md",
		"skills/agent-deck/scripts/launch-subagent.sh",
		"documentation/HARNESS_IDENTITY.md",
	} {
		path := filepath.Join("..", "..", filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(data), session.FanoutHint) {
			t.Errorf("%s must carry the fan-out line %q", rel, session.FanoutHint)
		}
	}
	ref, err := os.ReadFile(filepath.Join("..", "..", "skills", "agent-deck", "references", "config-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ref), "| `fanout_hint` | bool | `true` |") {
		t.Error("config-reference.md must document [launch] fanout_hint with default true")
	}
}

// runLaunchSubagent runs the real script against stub agent-deck and jq
// binaries and returns the -m prompt the stub received.
func runLaunchSubagent(t *testing.T, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "skills", "agent-deck", "scripts", "launch-subagent.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	work := filepath.Join(dir, "work")
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{
		// Records the value after -m on launch; everything else is a no-op.
		"agent-deck": `#!/bin/bash
if [ "$1 $2" = "session current" ]; then echo '{}'; exit 0; fi
while [ $# -gt 0 ]; do
  if [ "$1" = "-m" ]; then printf '%s' "$2" > "$STUB_PROMPT_FILE"; fi
  shift
done
exit 0
`,
		"jq": `#!/bin/bash
cat >/dev/null
case "$2" in
  .session) echo parent-1 ;;
  .profile) echo default ;;
  .path) echo "$STUB_WORK_DIR" ;;
  *) echo "" ;;
esac
`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_PROMPT_FILE="+promptFile,
		"STUB_WORK_DIR="+work,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch-subagent.sh %v: %v\n%s", args, err, out)
	}
	got, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("stub agent-deck saw no launch -m: %v", err)
	}
	return string(got)
}

func TestLaunchSubagentScript_AppendsFanoutHint(t *testing.T) {
	t.Parallel()
	got := runLaunchSubagent(t, "Worker", "Do the task.")
	want := "Do the task.\n\n" + session.FanoutHint
	if got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

func TestLaunchSubagentScript_FanoutHintOptOutAndNoDuplicate(t *testing.T) {
	t.Parallel()
	if got := runLaunchSubagent(t, "Worker", "Do the task.", "--no-fanout-hint"); got != "Do the task." {
		t.Errorf("--no-fanout-hint prompt = %q, want the prompt unchanged", got)
	}
	already := "Do it.\n" + session.FanoutHint
	if got := runLaunchSubagent(t, "Worker", already); got != already {
		t.Errorf("prompt already carrying the line was changed: %q", got)
	}
}
