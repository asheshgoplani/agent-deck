package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// evilInstanceID is an instance id that breaks out of a bare
// `AGENTDECK_INSTANCE_ID=%s` splice. Ids are minted by GenerateID, but they are
// loaded back from state.db / legacy JSON without a charset check, so the
// launch builders quote them like the fork and cross-harness builders do.
const evilInstanceID = "x; touch PWNED; $(touch PWNED)"

// runWithInstanceIDProbe runs cmdStr in a real bash with a stub `tool` first on
// PATH that records the AGENTDECK_INSTANCE_ID it received, and returns it. It
// fails the test if the injected `touch PWNED` ran.
func runWithInstanceIDProbe(t *testing.T, tool, cmdStr string) string {
	t.Helper()
	runDir := t.TempDir()
	binDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	stub := "#!/bin/sh\nprintf '%s' \"$AGENTDECK_INSTANCE_ID\" > " + shellQuote(record) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, tool), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	cmd := exec.Command("bash", "-c", cmdStr)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	out, runErr := cmd.CombinedOutput()
	if _, err := os.Stat(filepath.Join(runDir, "PWNED")); err == nil {
		t.Errorf("injected command executed: PWNED created\ncmd: %s", cmdStr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("stub %s never ran: %v (run err %v, output %s)\ncmd: %s", tool, err, runErr, out, cmdStr)
	}
	return string(data)
}

func assertInstanceIDEnv(t *testing.T, tool, cmdStr string) {
	t.Helper()
	if got := runWithInstanceIDProbe(t, tool, cmdStr); got != evilInstanceID {
		t.Errorf("AGENTDECK_INSTANCE_ID = %q, want %q\ncmd: %s", got, evilInstanceID, cmdStr)
	}
}

func instanceWithEvilID(t *testing.T, tool string) *Instance {
	t.Helper()
	inst := NewInstanceWithTool("id-inject", sessionIDInjectionEnv(t), tool)
	inst.ID = evilInstanceID
	return inst
}

// buildBashExportPrefix (claude start/restart/fork).
func TestInstanceIDQuoting_ClaudeExportPrefix(t *testing.T) {
	inst := instanceWithEvilID(t, "claude")
	cmdStr := inst.buildClaudeCommand("claude")
	assertInstanceIDEnv(t, "claude", cmdStr)
}

// buildCodexCommand env prefix.
func TestInstanceIDQuoting_Codex(t *testing.T) {
	inst := instanceWithEvilID(t, "codex")
	cmdStr := inst.buildCodexCommand("codex")
	assertInstanceIDEnv(t, "codex", cmdStr)
}

// buildDeepSeekCommand and buildDeepSeekResumeCommand env prefixes (the
// launched binary is dsh).
func TestInstanceIDQuoting_DeepSeek(t *testing.T) {
	inst := instanceWithEvilID(t, "deepseek")
	cmdStr := inst.buildDeepSeekCommand("deepseek")
	assertInstanceIDEnv(t, "dsh", cmdStr)
}

func TestInstanceIDQuoting_DeepSeekResume(t *testing.T) {
	inst := instanceWithEvilID(t, "deepseek")
	inst.Command = "deepseek"
	cmdStr := inst.buildDeepSeekResumeCommand()
	assertInstanceIDEnv(t, "dsh", cmdStr)
}

// buildHermesCommand env prefix.
func TestInstanceIDQuoting_Hermes(t *testing.T) {
	inst := instanceWithEvilID(t, "hermes")
	cmdStr := inst.buildHermesCommand("hermes")
	assertInstanceIDEnv(t, "hermes", cmdStr)
}

// buildShellPassthroughCommand, claude and codex branches.
func TestInstanceIDQuoting_ShellPassthrough(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			inst := instanceWithEvilID(t, "shell")
			inst.Command = tool + " --version"
			inst.SubcommandPassthrough = true
			cmdStr := inst.buildShellPassthroughCommand(inst.Command)
			assertInstanceIDEnv(t, tool, cmdStr)
		})
	}
}

// TestInstanceIDQuoting_OrdinaryIDUnchanged pins that a GenerateID-shaped id
// is emitted bare, so real commands stay byte-identical.
func TestInstanceIDQuoting_OrdinaryIDUnchanged(t *testing.T) {
	id := GenerateID()
	builds := map[string]func(*Instance) string{
		"claude":   func(i *Instance) string { return i.buildClaudeCommand("claude") },
		"codex":    func(i *Instance) string { return i.buildCodexCommand("codex") },
		"deepseek": func(i *Instance) string { return i.buildDeepSeekCommand("deepseek") },
		"hermes":   func(i *Instance) string { return i.buildHermesCommand("hermes") },
	}
	for tool, build := range builds {
		t.Run(tool, func(t *testing.T) {
			inst := NewInstanceWithTool("id-plain", sessionIDInjectionEnv(t), tool)
			inst.ID = id
			if got := build(inst); !strings.Contains(got, "AGENTDECK_INSTANCE_ID="+id+" ") && !strings.Contains(got, "AGENTDECK_INSTANCE_ID="+id+";") {
				t.Errorf("command = %q, want bare AGENTDECK_INSTANCE_ID=%s", got, id)
			}
		})
	}
}
