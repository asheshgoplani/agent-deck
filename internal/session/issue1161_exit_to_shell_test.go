package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Issue #1161 (reporter @Djeeteg007): exiting an agent (`/exit`) should be able
// to drop the pane back to an interactive shell at the same cwd, so the user can
// do shell-only work (aws-vault exec, direnv, …) and then `claude --resume` the
// SAME session. PR #503 made the agent the pane's initial process, which removed
// the parent shell that used to survive `/exit`. Option A (design doc
// docs/decisions/1161-exit-to-shell-then-resume.md) restores the shell fallback
// behind an opt-in flag by wrapping the spawn command as
// `<agent cmd>; exec "$SHELL" -i` (note: NO `exec` before the agent), keeping the
// #503 perf win.
//
// These tests pin the contract: flag ON wraps, flag OFF is byte-for-byte
// unchanged, the session id survives the wrap (resume still works), and
// non-agent / sandboxed sessions are unaffected.

const exitToShellSuffix = `; exec "$SHELL" -i`

// exitToShellTestEnv isolates CLAUDE_CONFIG_DIR / HOME so command building is
// deterministic. Mirrors extraArgsTestEnv (extraargs_test.go:14).
func exitToShellTestEnv(t *testing.T) {
	t.Helper()
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})
}

// boolPtr is provided by gemini_yolo_test.go in this package.

// --- Happy path: flag ON wraps the claude spawn command ---------------------

func TestExitToShell_ClaudeWrappedWhenEnabled(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-claude", t.TempDir(), "claude")
	inst.ExitToShell = boolPtr(true)

	raw := inst.buildClaudeCommand("claude")
	wrapped := inst.wrapExitToShell(raw)

	if !strings.HasSuffix(wrapped, exitToShellSuffix) {
		t.Fatalf("wrapped command must end with %q, got:\n%s", exitToShellSuffix, wrapped)
	}
	// The agent must NOT be exec'd, otherwise it would replace the wrapping
	// bash and the trailing `exec "$SHELL"` would never run.
	if strings.Contains(wrapped, "exec env") || strings.Contains(wrapped, "exec claude") {
		t.Fatalf("agent launcher must not be exec'd in exit-to-shell mode, got:\n%s", wrapped)
	}
}

// --- Regression: flag OFF leaves the command byte-for-byte unchanged --------

func TestExitToShell_DisabledLeavesCommandUnchanged(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-off", t.TempDir(), "claude")
	// No per-session override and no config flag -> default OFF.

	raw := inst.buildClaudeCommand("claude")
	wrapped := inst.wrapExitToShell(raw)

	if wrapped != raw {
		t.Fatalf("flag OFF must not alter the command.\n raw:     %s\n wrapped: %s", raw, wrapped)
	}
	if strings.Contains(wrapped, exitToShellSuffix) {
		t.Fatalf("flag OFF must not append the shell suffix, got:\n%s", wrapped)
	}
}

// --- Resume metadata preserved: the session id survives the wrap ------------

func TestExitToShell_PreservesClaudeSessionID(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-resume", t.TempDir(), "claude")
	inst.ExitToShell = boolPtr(true)

	raw := inst.buildClaudeCommand("claude")
	wrapped := inst.wrapExitToShell(raw)

	if inst.ClaudeSessionID == "" {
		t.Fatal("ClaudeSessionID must be captured in Go memory for resume; got empty")
	}
	// The id must still be on the command line so `claude --session-id <uuid>`
	// (and therefore `--resume <uuid>`) targets the SAME session after the
	// shell detour. Cross-checks conductor_restart_history_loss.
	if !strings.Contains(wrapped, inst.ClaudeSessionID) {
		t.Fatalf("wrapped command lost the session id %q, got:\n%s", inst.ClaudeSessionID, wrapped)
	}
}

// --- Boundary: a tool that does not use `exec` (gemini) still wraps ----------

func TestExitToShell_GeminiWrappedWhenEnabled(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-gemini", t.TempDir(), "gemini")
	inst.ExitToShell = boolPtr(true)

	raw := inst.buildGeminiCommand("gemini")
	wrapped := inst.wrapExitToShell(raw)

	if !strings.HasSuffix(wrapped, exitToShellSuffix) {
		t.Fatalf("gemini command must end with %q, got:\n%s", exitToShellSuffix, wrapped)
	}
	if !strings.Contains(wrapped, "gemini") {
		t.Fatalf("gemini invocation lost from wrapped command, got:\n%s", wrapped)
	}
}

// --- Failure mode: non-agent (shell) tool is never wrapped ------------------

func TestExitToShell_ShellToolNotWrapped(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-shell", t.TempDir(), "shell")
	inst.ExitToShell = boolPtr(true)

	raw := "vim"
	wrapped := inst.wrapExitToShell(raw)

	if wrapped != raw {
		t.Fatalf("shell tool must not be exit-to-shell wrapped, got:\n%s", wrapped)
	}
}

// --- Failure mode: sandboxed sessions are excluded (docker exec path) -------

func TestExitToShell_SandboxNotWrapped(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-sandbox", t.TempDir(), "claude")
	inst.ExitToShell = boolPtr(true)
	inst.Sandbox = &SandboxConfig{Enabled: true, Image: "agentdeck/sandbox:latest"}

	raw := inst.buildClaudeCommand("claude")
	wrapped := inst.wrapExitToShell(raw)

	if wrapped != raw {
		t.Fatalf("sandboxed session must not be exit-to-shell wrapped, got:\n%s", wrapped)
	}
}

// --- Empty command is a no-op (defensive) -----------------------------------

func TestExitToShell_EmptyCommandNoop(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-empty", t.TempDir(), "claude")
	inst.ExitToShell = boolPtr(true)

	if got := inst.wrapExitToShell(""); got != "" {
		t.Fatalf("empty command must stay empty, got:\n%s", got)
	}
}

// --- Config default is OFF (opt-in) -----------------------------------------

func TestExitToShell_ConfigDefaultOff(t *testing.T) {
	var s ShellSettings
	if s.GetExitToShell() {
		t.Fatal("ShellSettings.GetExitToShell() must default to false (opt-in)")
	}
	s.ExitToShell = boolPtr(true)
	if !s.GetExitToShell() {
		t.Fatal("ShellSettings.GetExitToShell() must honor an explicit true")
	}
}

// --- Per-session override beats config; config used when override is nil ----

func TestExitToShell_ResolutionPrecedence(t *testing.T) {
	exitToShellTestEnv(t)

	// Override nil + config off -> off.
	inst := NewInstanceWithTool("e2s-prec", t.TempDir(), "claude")
	if inst.exitToShellEnabled() {
		t.Fatal("default (no override, no config) must be OFF")
	}

	// Override true wins regardless of config.
	inst.ExitToShell = boolPtr(true)
	if !inst.exitToShellEnabled() {
		t.Fatal("per-session override true must enable exit-to-shell")
	}

	// Override false wins regardless of config.
	inst.ExitToShell = boolPtr(false)
	if inst.exitToShellEnabled() {
		t.Fatal("per-session override false must disable exit-to-shell")
	}
}

// --- Integration: the actual spawn path (prepareCommand) applies the wrap ----

// prepareCommand is the universal choke point every start/restart/fork path
// flows through. For a plain non-sandbox, non-wrapper claude session it returns
// the command otherwise unchanged, so this asserts the feature is wired in, not
// just that the helper exists.
func TestExitToShell_PrepareCommandWiresInWrap(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-prepare", t.TempDir(), "claude")
	inst.ExitToShell = boolPtr(true)

	raw := inst.buildClaudeCommand("claude")
	prepared, container, err := inst.prepareCommand(raw)
	if err != nil {
		t.Fatalf("prepareCommand: %v", err)
	}
	if container != "" {
		t.Fatalf("non-sandbox session must not produce a container, got %q", container)
	}
	if !strings.HasSuffix(prepared, exitToShellSuffix) {
		t.Fatalf("prepareCommand must apply the exit-to-shell wrap, got:\n%s", prepared)
	}
	if !strings.Contains(prepared, inst.ClaudeSessionID) {
		t.Fatalf("prepared command lost session id %q, got:\n%s", inst.ClaudeSessionID, prepared)
	}
}

func TestExitToShell_PrepareCommandUnchangedWhenDisabled(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-prepare-off", t.TempDir(), "claude")
	// Flag OFF (default).

	raw := inst.buildClaudeCommand("claude")
	prepared, _, err := inst.prepareCommand(raw)
	if err != nil {
		t.Fatalf("prepareCommand: %v", err)
	}
	if strings.Contains(prepared, exitToShellSuffix) {
		t.Fatalf("flag OFF must not wrap via prepareCommand, got:\n%s", prepared)
	}
}

// --- Persistence: the per-session override round-trips through JSON ----------

// The override lives on Instance (Storage.Save → Storage.Load uses json), so it
// must survive a marshal/unmarshal cycle for restart to honor it.
func TestExitToShell_OverridePersistsThroughJSON(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-persist", t.TempDir(), "claude")
	inst.ExitToShell = boolPtr(true)

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("json.Marshal(inst): %v", err)
	}
	if !strings.Contains(string(data), `"exit_to_shell":`) {
		t.Fatalf("marshalled instance missing \"exit_to_shell\" json tag; got:\n%s", string(data))
	}

	revived := &Instance{}
	if err := json.Unmarshal(data, revived); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if revived.ExitToShell == nil || !*revived.ExitToShell {
		t.Fatalf("revived ExitToShell = %v, want true", revived.ExitToShell)
	}
	if !revived.exitToShellEnabled() {
		t.Fatal("revived instance must still resolve exit-to-shell as enabled")
	}
}

// --- Default-off contract via the global config (no override) ----------------

// A session with no override and the default config must NOT wrap, even though
// the tool is a built-in agent — proving the feature is genuinely opt-in.
func TestExitToShell_GlobalDefaultIsOptIn(t *testing.T) {
	exitToShellTestEnv(t)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Shell.GetExitToShell() {
		t.Fatal("default loaded config must have exit_to_shell OFF")
	}

	inst := NewInstanceWithTool("e2s-global-default", t.TempDir(), "claude")
	if inst.exitToShellEnabled() {
		t.Fatal("instance with no override under default config must be OFF")
	}
}
