package session

import (
	"strings"
	"testing"
)

// The fresh-start path already exec's claude so the agent replaces the wrapper
// shell and becomes the pane's process-group leader. Resume and continue did
// not, leaving bash as the leader with claude as its child. Tools that inspect
// the pane (tmux #{pane_current_command}, anything comparing a pid against the
// tty's foreground pgid) then see a shell rather than the agent, and the two
// spawn paths disagree about what a running session looks like.
func TestClaudeCommandsExecSoAgentLeadsPane(t *testing.T) {
	tests := []struct {
		name  string
		build func(i *Instance) string
	}{
		{
			name: "restart path",
			build: func(i *Instance) string {
				i.ClaudeSessionID = "11111111-2222-3333-4444-555555555555"
				return i.buildClaudeResumeCommand()
			},
		},
		{
			name: "fresh start",
			build: func(i *Instance) string {
				return i.buildClaudeCommand("claude")
			},
		},
		{
			name: "resume by id",
			build: func(i *Instance) string {
				opts := NewClaudeOptions(nil)
				opts.SessionMode = "resume"
				opts.ResumeSessionID = "66666666-7777-8888-9999-000000000000"
				i.SetClaudeOptions(opts)
				return i.buildClaudeCommand("claude")
			},
		},
		{
			name: "continue last session",
			build: func(i *Instance) string {
				opts := NewClaudeOptions(nil)
				opts.SessionMode = "continue"
				i.SetClaudeOptions(opts)
				return i.buildClaudeCommand("claude")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := NewInstanceWithTool("test", t.TempDir(), "claude")
			cmd := tt.build(inst)

			idx := strings.LastIndex(cmd, "exec ")
			if idx < 0 {
				t.Fatalf("command must exec the agent so it leads the pane process group, got: %s", cmd)
			}

			// Everything after the final exec is what replaces the shell. It is
			// either the agent itself or `env …` re-exec'ing it, never another
			// shell builtin that would leave the wrapper in place.
			rest := strings.TrimSpace(cmd[idx+len("exec "):])
			if !strings.HasPrefix(rest, "claude") && !strings.HasPrefix(rest, "env ") {
				t.Errorf("exec must hand off to the agent (optionally via env), got: %s", cmd)
			}
			if !strings.Contains(rest, "claude") {
				t.Errorf("exec'd command must invoke claude, got: %s", cmd)
			}
		})
	}
}
