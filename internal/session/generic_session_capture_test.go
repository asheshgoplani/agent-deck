package session

import (
	"strings"
	"testing"
)

// A tool can report its conversation id in JSON output (output_format_flag +
// session_id_json_path) while exporting no environment variable of its own.
// Before the capture published the id, that id lived only as a shell variable
// inside the pane: it resumed the launch that produced it and was gone, so the
// feature's whole promise -- the conversation survives a reboot -- did not hold
// for exactly the tools that needed the capture path.

const captureOnlyToolConfig = `
[tools.capturetool]
command = "capturetool"
resume_flag = "--resume"
output_format_flag = "--json"
session_id_json_path = ".session_id"
`

func TestGenericCapture_PublishesIDForDurability(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), captureOnlyToolConfig)

	inst := NewInstance("capture-target", "/tmp/proj")
	inst.Tool = "capturetool"

	cmd := inst.buildGenericCommand("capturetool")

	if !strings.Contains(cmd, "set-environment "+genericCapturedSessionIDEnv) {
		t.Fatalf("capture command does not publish the id for agent-deck to read back:\n%s", cmd)
	}
	// The id must reach the variable through the shell, never through Go-side
	// interpolation -- the whole point of capturing it in the pane is that
	// agent-deck never sees the value at command-build time.
	if !strings.Contains(cmd, `set-environment `+genericCapturedSessionIDEnv+` "$session_id"`) {
		t.Errorf("published value is not the shell variable:\n%s", cmd)
	}
	// Publishing is bookkeeping; a tmux hiccup must not stop the tool starting.
	if !strings.Contains(cmd, "|| true") {
		t.Errorf("a failed publish would abort the launch:\n%s", cmd)
	}
	// The resume still runs after the publish, in the same branch.
	if idx := strings.Index(cmd, "set-environment"); idx < 0 || !strings.Contains(cmd[idx:], "--resume") {
		t.Errorf("resume no longer follows the publish:\n%s", cmd)
	}
}

// TestGenericSessionEnvNames_PrefersToolOwnVariable keeps the fallback from
// shadowing a tool that does declare its own variable: that one is the tool's
// live truth, and the agent-deck-owned one can only ever be a copy.
func TestGenericSessionEnvNames_PrefersToolOwnVariable(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	// BOTH tables, so the second assertion exercises a configured tool that
	// declares no session_id_env rather than an unknown tool. They return the
	// same list today; writing only one would let a future change that reads
	// other ToolDef fields pass unnoticed.
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.envtool]
command = "envtool"
resume_flag = "--resume"
session_id_env = "ENVTOOL_SESSION_ID"
`+captureOnlyToolConfig)

	inst := NewInstance("env-target", "/tmp/proj")
	inst.Tool = "envtool"

	names := inst.genericSessionEnvNames()
	if len(names) != 2 || names[0] != "ENVTOOL_SESSION_ID" || names[1] != genericCapturedSessionIDEnv {
		t.Fatalf("genericSessionEnvNames() = %v, want the tool's own variable first then %s",
			names, genericCapturedSessionIDEnv)
	}

	// A tool with no variable of its own still has the fallback to read.
	inst.Tool = "capturetool"
	if names := inst.genericSessionEnvNames(); len(names) != 1 || names[0] != genericCapturedSessionIDEnv {
		t.Errorf("genericSessionEnvNames() = %v for a tool with no session_id_env, want [%s]",
			names, genericCapturedSessionIDEnv)
	}
}
