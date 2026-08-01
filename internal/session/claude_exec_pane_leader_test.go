package session

import (
	"os"
	"path/filepath"
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
			if target := execTarget(rest); target != "claude" {
				t.Errorf("exec must hand off to claude, it runs %q instead: %s", target, cmd)
			}

			// exec only makes the agent the pane leader if nothing follows it:
			// `exec claude …; cleanup` is not an exec'd pane, it is a shell that
			// happens to contain the word. Without this the assertion above
			// passes on exactly the regression it exists to catch.
			for _, sep := range []string{";", "&&", "||", "|", "&"} {
				if strings.Contains(rest, sep) {
					t.Errorf("exec'd invocation must be the final statement, found %q after it: %s", sep, cmd)
				}
			}
		})
	}
}

// "resume" splits on whether canResumeClaudeSession says yes: when it does the
// command is `--resume <id>`, otherwise it falls back to `--session-id <id>`.
// A fresh temp instance satisfies neither half of that gate, so the table above
// only ever reaches the fallback. This sets up both halves so the --resume
// branch is exercised too, and asserts the branch was actually taken rather
// than passing quietly on the fallback again.
//
// Both halves are needed (#1815): the transcript on disk satisfies the
// conversation-data check, and recording the id on the instance satisfies the
// identity check, which refuses any candidate that is not the instance's own
// recorded conversation. That mirrors a real explicit resume, where the id is
// written to the instance before the command is built.
func TestClaudeResumeWithConversationHistoryExecs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	projectDir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		resolved = projectDir
	}

	const sessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	transcriptDir := filepath.Join(home, ".claude", "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("create transcript dir: %v", err)
	}
	// the check scans for a "sessionId" field, so an arbitrary JSON line is
	// not enough to count as conversation history
	transcript := filepath.Join(transcriptDir, sessionID+".jsonl")
	line := `{"sessionId":"` + sessionID + `","type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	inst := NewInstanceWithTool("test", projectDir, "claude")
	inst.ClaudeSessionID = sessionID
	opts := NewClaudeOptions(nil)
	opts.SessionMode = "resume"
	opts.ResumeSessionID = sessionID
	inst.SetClaudeOptions(opts)

	cmd := inst.buildClaudeCommand("claude")

	if !strings.Contains(cmd, "--resume "+sessionID) {
		t.Fatalf("fixture did not reach the --resume branch (still on the --session-id fallback), got: %s", cmd)
	}
	// the two are mutually exclusive: seeing both would mean the branch was
	// taken but the fallback flag leaked in alongside it
	if strings.Contains(cmd, "--session-id") {
		t.Errorf("resume-with-history must not carry the --session-id fallback, got: %s", cmd)
	}
	idx := strings.LastIndex(cmd, "exec ")
	if idx < 0 {
		t.Fatalf("resume-with-history must exec the agent so it leads the pane, got: %s", cmd)
	}
	rest := strings.TrimSpace(cmd[idx+len("exec "):])
	if target := execTarget(rest); target != "claude" {
		t.Errorf("exec must hand off to claude, it runs %q instead: %s", target, cmd)
	}
	for _, sep := range []string{";", "&&", "||", "|", "&"} {
		if strings.Contains(rest, sep) {
			t.Errorf("exec'd invocation must be the final statement, found %q after it: %s", sep, cmd)
		}
	}
}

// execTarget returns the program an exec'd command actually runs, stepping over
// an `env` wrapper and the flags and NAME=value assignments it consumes.
//
// Checking that the command merely starts with "env " and mentions claude
// somewhere would pass on `exec env -u FOO something-else --flag=claude`, which
// leaves the pane led by the wrong process, so the target is resolved instead.
func execTarget(rest string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	if fields[0] != "env" {
		return fields[0]
	}
	for i := 1; i < len(fields); i++ {
		switch {
		case fields[i] == "-u" || fields[i] == "-C" || fields[i] == "-S":
			i++ // consumes the following argument
		case strings.HasPrefix(fields[i], "-"):
			// standalone flag such as -i
		case strings.Contains(fields[i], "="):
			// NAME=value assignment
		default:
			return fields[i]
		}
	}
	return ""
}
