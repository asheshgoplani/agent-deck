package session

import "testing"

// TestClassifyTerminatedPane_CleanExitVsCrash pins the classification of a
// session whose tmux pane has terminated after having been started.
//
// A one-shot worker runs a command that finishes and exits. When tmux still
// holds the dead pane (remain-on-exit), the real process exit code is
// available: exit 0 is a clean completion (■ StatusStopped), not a crash — the
// bug was that every terminated pane read as StatusError (✕), making a
// successful one-shot exit indistinguishable from a genuine failure. A
// non-zero exit is a real crash → StatusError.
//
// When no exit code is available (pane torn down without remain-on-exit, so
// tmux discarded the exit status), classification consults the hook-emitting
// tool's last recorded hook status (issue #2091): a Stop-edge hook ("waiting"/
// "idle") already proves the turn finished cleanly before the pane vanished,
// so that TOCTOU must not read as a crash. No hook record at all keeps the
// historical StatusError default (backward compatible) but flags the verdict
// as SubstateUnknownExit — a guess, not a fact — rather than silently
// asserting a crash that was never observed. OpenCode's hookless `/exit`
// reads as stopped regardless (#1617).
func TestClassifyTerminatedPane_CleanExitVsCrash(t *testing.T) {
	tests := []struct {
		name         string
		exitCode     int
		haveExitCode bool
		tool         string
		hookStatus   string
		want         Status
		wantSubstate Substate
	}{
		// Exit code known (remain-on-exit): the code decides, tool is irrelevant.
		{name: "clean exit 0 (shell)", exitCode: 0, haveExitCode: true, tool: "shell", want: StatusStopped},
		{name: "clean exit 0 (claude)", exitCode: 0, haveExitCode: true, tool: "claude", want: StatusStopped},
		{name: "clean exit 0 (sandboxed worker)", exitCode: 0, haveExitCode: true, tool: "codex", want: StatusStopped},
		{name: "crash exit 1", exitCode: 1, haveExitCode: true, tool: "shell", want: StatusError},
		{name: "crash exit 137 (SIGKILL)", exitCode: 137, haveExitCode: true, tool: "claude", want: StatusError},
		{name: "crash exit 2 (opencode)", exitCode: 2, haveExitCode: true, tool: "opencode", want: StatusError},

		// No exit code (pane torn down), OpenCode: always the hookless heuristic.
		{name: "no exit code, opencode clean /exit", tool: "opencode", want: StatusStopped},

		// No exit code, hook-emitting tool: the hook record decides (#2091).
		{name: "no exit code, claude Stop already fired (waiting)", tool: "claude", hookStatus: "waiting", want: StatusStopped},
		{name: "no exit code, claude Stop already fired (idle)", tool: "claude", hookStatus: "idle", want: StatusStopped},
		{name: "no exit code, codex turn complete (waiting)", tool: "codex", hookStatus: "waiting", want: StatusStopped},
		{name: "no exit code, claude turn in flight", tool: "claude", hookStatus: "running", want: StatusError},
		{name: "no exit code, codex reported dead", tool: "codex", hookStatus: "dead", want: StatusError},

		// No exit code AND no hook record: unverifiable — keep the historical
		// error default but mark it as a guess.
		{name: "no exit code, claude, no hook record", tool: "claude", want: StatusError, wantSubstate: SubstateUnknownExit},
		{name: "no exit code, shell (not hook-emitting)", tool: "shell", want: StatusError},
		{name: "no exit code, unknown tool", tool: "", want: StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotSubstate := classifyTerminatedPane(tt.exitCode, tt.haveExitCode, tt.tool, tt.hookStatus)
			if got != tt.want {
				t.Errorf("classifyTerminatedPane(%d, %v, %q, %q) = %q, want %q",
					tt.exitCode, tt.haveExitCode, tt.tool, tt.hookStatus, got, tt.want)
			}
			if gotSubstate != tt.wantSubstate {
				t.Errorf("classifyTerminatedPane(%d, %v, %q, %q) substate = %q, want %q",
					tt.exitCode, tt.haveExitCode, tt.tool, tt.hookStatus, gotSubstate, tt.wantSubstate)
			}
		})
	}
}

// TestTerminatedPaneStatus_NilTmuxFallsBackToTool guards the no-tmux path: with
// no session to read an exit code from, terminatedPaneStatus must degrade to
// the per-tool heuristic rather than assume a clean exit.
func TestTerminatedPaneStatus_NilTmuxFallsBackToTool(t *testing.T) {
	cases := map[string]Status{
		"opencode": StatusStopped,
		"claude":   StatusError,
		"shell":    StatusError,
		"":         StatusError,
	}
	for tool, want := range cases {
		i := &Instance{Tool: tool}
		if got := i.terminatedPaneStatus(); got != want {
			t.Errorf("terminatedPaneStatus() nil tmux, tool %q = %q, want %q", tool, got, want)
		}
	}
}

// TestTerminatedPaneStatus_HookCompletionOverridesError is the issue #2091
// regression: a hook-emitting session (Claude/Codex) whose Stop-edge hook
// already recorded a clean turn completion must not read as StatusError just
// because its tmux pane vanished with no captured exit code (no
// remain-on-exit) — the TOCTOU the issue reports. Also pins the companion
// "we genuinely don't know" case: no hook record at all keeps the historical
// StatusError default but surfaces SubstateUnknownExit instead of silently
// asserting a crash that was never observed.
func TestTerminatedPaneStatus_HookCompletionOverridesError(t *testing.T) {
	tests := []struct {
		name         string
		tool         string
		hookStatus   string
		wantStatus   Status
		wantSubstate Substate
	}{
		{name: "claude Stop already fired", tool: "claude", hookStatus: "waiting", wantStatus: StatusStopped},
		{name: "codex turn already completed", tool: "codex", hookStatus: "waiting", wantStatus: StatusStopped},
		{name: "claude crash mid-turn", tool: "claude", hookStatus: "running", wantStatus: StatusError},
		{name: "claude pane vanished, no hook ever seen", tool: "claude", hookStatus: "", wantStatus: StatusError, wantSubstate: SubstateUnknownExit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := &Instance{Tool: tt.tool, hookStatus: tt.hookStatus, Status: StatusRunning}
			if got := i.terminatedPaneStatus(); got != tt.wantStatus {
				t.Errorf("terminatedPaneStatus() tool %q hookStatus %q = %q, want %q",
					tt.tool, tt.hookStatus, got, tt.wantStatus)
			}
			i.Status = StatusError // gate getTerminatedPaneSubstate checks this
			if got := i.getTerminatedPaneSubstate(); got != tt.wantSubstate {
				t.Errorf("getTerminatedPaneSubstate() tool %q hookStatus %q = %q, want %q",
					tt.tool, tt.hookStatus, got, tt.wantSubstate)
			}
		})
	}
}
