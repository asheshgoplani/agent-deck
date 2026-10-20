package tmux

import "testing"

// agent-deck starts a tool at the end of a setup chain (cd, env files,
// unset/export, mkdir). #2683 put `unset CLAUDE_JOB_DIR` into that chain for
// every hook-status tool, and a Codex launch carries the identity block, which
// mentions claude, in its arguments. Neither may decide which tool runs. The
// chains below are the ones 1.16.29 builds, with values shortened.
const launchChainSetup = `cd -- /src/project && export COLORFGBG='15;0' && [ -f "/cfg/agent-deck/agent-cat.env" ] && source "/cfg/agent-deck/agent-cat.env" && `

func TestDetectToolFromCommand_LaunchChain(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "pi after unset CLAUDE_JOB_DIR",
			command: launchChainSetup + `unset CLAUDE_JOB_DIR && export AGENTDECK_IDENTITY_FILE=/rt/identity.md && session_dir=/rt/pi; mkdir -p "$session_dir" && AGENTDECK_INSTANCE_ID=f7407e3b AGENTDECK_PROFILE=default pi --continue --session-dir "$session_dir" --append-system-prompt /rt/identity.md`,
			want:    "pi",
		},
		{
			name:    "codex whose identity block mentions claude",
			command: launchChainSetup + `export AGENTDECK_IDENTITY_FILE=/rt/identity.md && AGENTDECK_INSTANCE_ID=e78b9a6d CODEX_HOME=/cfg/codex codex --yolo -c 'developer_instructions="spawn a child with -c claude; see CLAUDE.md"'`,
			want:    "codex",
		},
		{
			name:    "gemini after unset CLAUDE_JOB_DIR",
			command: launchChainSetup + `unset CLAUDE_JOB_DIR && export AGENTDECK_IDENTITY_FILE=/rt/identity.md && gemini --yolo`,
			want:    "gemini",
		},
		{
			name:    "claude through exec env",
			command: launchChainSetup + `unset TELEGRAM_STATE_DIR TELEGRAM_BOT_TOKEN && unset CLAUDE_JOB_DIR && export AGENTDECK_IDENTITY_FILE=/rt/identity.md && export CLAUDE_CONFIG_DIR=/cfg/claude; exec env -u TELEGRAM_STATE_DIR claude --session-id 1234 --dangerously-skip-permissions`,
			want:    "claude",
		},
		{
			name:    "project path naming another tool",
			command: `cd -- /src/claude-tools && export AGENTDECK_IDENTITY_FILE=/rt/identity.md && pi --continue`,
			want:    "pi",
		},
		{
			name:    "exit-receipt wrapper",
			command: `bash -c 'cd -- /src && unset CLAUDE_JOB_DIR && pi --continue'; code=$?; (umask 077; printf '%s\n' "$code" > /rt/exit/f7407e3b); exit "$code"`,
			want:    "pi",
		},
		{
			name:    "setup only",
			command: `cd -- /src/claude && unset CLAUDE_JOB_DIR`,
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

// DetectTool's command step is what the TUI shows (Instance.DisplayToolThreadSafe
// prefers the observed tool), so a pi chain must observe pi.
func TestDetectToolObservesPiForItsLaunchChain(t *testing.T) {
	s := &Session{
		Name:    "agentdeck_launch_chain_pi",
		Command: launchChainSetup + `unset CLAUDE_JOB_DIR && pi --continue`,
	}
	if got := s.DetectTool(); got != "pi" {
		t.Fatalf("DetectTool() = %q, want pi", got)
	}
}
