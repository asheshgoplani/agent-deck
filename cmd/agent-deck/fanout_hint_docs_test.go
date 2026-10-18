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
		data := readAcceptanceFixture(t, "..", "..", filepath.FromSlash(rel))
		if !strings.Contains(data, session.FanoutHint) {
			t.Errorf("%s must carry the fan-out line %q", rel, session.FanoutHint)
		}
		if strings.HasSuffix(rel, "config-reference.md") && !strings.Contains(data, "| `fanout_hint` | bool | `true` |") {
			t.Error("config-reference.md must document [launch] fanout_hint with default true")
		}
	}
}

// runLaunchSubagentEnv runs the real script (with extra env for the stubs) against stub agent-deck and jq
// binaries and returns the -m prompt the stub received.
func runLaunchSubagentEnv(t *testing.T, env []string, args ...string) string {
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
if [ "$1 $2 $3" = "config get launch.fanout_hint" ]; then echo "${STUB_FANOUT_HINT:-true}"; exit 0; fi
if [ "$1 $2 $3" = "config get launch.context_level" ]; then echo "${STUB_CONTEXT_LEVEL:-full}"; exit 0; fi
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
	cmd.Env = append(cmd.Env, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch-subagent.sh %v: %v\n%s", args, err, out)
	}
	got, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("stub agent-deck saw no launch -m: %v", err)
	}
	return string(got)
}

// launch-subagent.sh appends the line only where the identity block will
// not carry it: a claude child with no identity block (global level none),
// with [launch] fanout_hint not false and no --no-fanout-hint.
func TestLaunchSubagentScript_AppendsFanoutHintOnlyWithoutIdentityBlock(t *testing.T) {
	t.Parallel()
	with := "Do the task.\n\n" + session.FanoutHint
	for _, tc := range []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{"identity block carries it", nil, nil, "Do the task."},
		{"primer carries it too", []string{"STUB_CONTEXT_LEVEL=primer"}, nil, "Do the task."},
		{"no identity block", []string{"STUB_CONTEXT_LEVEL=none"}, nil, with},
		{"no identity block, explicit claude", []string{"STUB_CONTEXT_LEVEL=none"}, []string{"--tool", "claude"}, with},
		{"fanout_hint=false", []string{"STUB_CONTEXT_LEVEL=none", "STUB_FANOUT_HINT=false"}, nil, "Do the task."},
		{"--no-fanout-hint", []string{"STUB_CONTEXT_LEVEL=none"}, []string{"--no-fanout-hint"}, "Do the task."},
		{"codex", []string{"STUB_CONTEXT_LEVEL=none"}, []string{"--tool", "codex"}, "Do the task."},
		{"gemini", []string{"STUB_CONTEXT_LEVEL=none"}, []string{"--tool", "gemini"}, "Do the task."},
		{"pi", []string{"STUB_CONTEXT_LEVEL=none"}, []string{"--tool", "pi"}, "Do the task."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runLaunchSubagentEnv(t, tc.env, append([]string{"Worker", "Do the task."}, tc.args...)...)
			if got != tc.want {
				t.Errorf("prompt = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLaunchSubagentScript_FanoutHintNoDuplicate(t *testing.T) {
	t.Parallel()
	already := "Do it.\n" + session.FanoutHint
	if got := runLaunchSubagentEnv(t, []string{"STUB_CONTEXT_LEVEL=none"}, "Worker", already); got != already {
		t.Errorf("prompt already carrying the line was changed: %q", got)
	}
}
