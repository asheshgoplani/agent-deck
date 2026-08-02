// Wiring pin for issue #1713 — the tmux working-directory guards.
//
// tmux does not fail a spawn over a bad start directory: it silently substitutes
// $HOME (missing directory), and a server whose own cwd was unlinked ignores the
// requested -c entirely and births every pane in that dead directory. Both
// produced sessions that agent-deck reported as started while the agent had
// never run, so internal/tmux now refuses such a start.
//
// One session kind must keep starting: an SSH session, whose ProjectPath is only
// a local placeholder — the project lives on the remote host and the pane just
// runs an ssh client. applyLaunchSettingsFromConfig is the single chokepoint the
// three Start() paths share, so the opt-out is pinned here.
package session

import "testing"

func TestApplyLaunchSettings_MarksSSHWorkDirAsPlaceholder(t *testing.T) {
	inst := NewInstance("issue1713-ssh", t.TempDir())
	inst.SSHHost = "build-box"

	inst.applyLaunchSettingsFromConfig()

	if !inst.tmuxSession.WorkDirIsPlaceholder {
		t.Fatal("#1713: an SSH session's local path is a placeholder — the working-directory " +
			"guards must not be able to refuse its start over a local directory nothing reads")
	}
}

func TestApplyLaunchSettings_LocalSessionKeepsWorkDirGuards(t *testing.T) {
	inst := NewInstance("issue1713-local", t.TempDir())

	inst.applyLaunchSettingsFromConfig()

	if inst.tmuxSession.WorkDirIsPlaceholder {
		t.Fatal("#1713: a local session's working directory is load-bearing — running the agent " +
			"in $HOME instead must stay a hard failure, not a silent success")
	}
}
