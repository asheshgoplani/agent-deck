package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeIssue3032Transcript puts a Claude transcript for sessionID where
// `claude --resume` run from projectDir looks for it, under configDir.
func writeIssue3032Transcript(t *testing.T, configDir, projectDir, sessionID string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		resolved = projectDir
	}
	dir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create transcript dir: %v", err)
	}
	line := `{"sessionId":"` + sessionID + `","type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// Issue #3032: CanFork refused every Claude session whose id was last
// detected more than five minutes ago, although the conversation was on disk
// and `claude --resume <id> --fork-session` would work. A one-shot CLI never
// refreshes ClaudeDetectedAt for a session that already has an id, so the
// CLI fork, `session show` can_fork and the TUI (which all call CanFork)
// refused a healthy session for the rest of its life.
//
// The clock still answers for remote (--ssh) sessions, whose transcript is
// not on this machine, and a stale id with no transcript stays unforkable.
func TestIssue3032_CanForkStaleClaudeIDWithTranscript(t *testing.T) {
	const sid = "3032aaaa-bbbb-4ccc-8ddd-eeeeeeee3032"
	stale := time.Now().Add(-10 * time.Minute)

	tests := []struct {
		name       string
		tool       string
		transcript bool
		ssh        bool
		elsewhere  bool // transcript only under another project dir
		detectedAt time.Time
		want       bool
	}{
		{name: "stale id, transcript on disk", tool: "claude", transcript: true, detectedAt: stale, want: true},
		{name: "never-detected id, transcript on disk", tool: "claude", transcript: true, want: true},
		{name: "fresh id, no transcript (unchanged)", tool: "claude", detectedAt: time.Now(), want: true},
		{name: "stale id, no transcript (unchanged)", tool: "claude", detectedAt: stale, want: false},
		{name: "stale id, transcript only under another project dir", tool: "claude", transcript: true, elsewhere: true, detectedAt: stale, want: false},
		{name: "stale id, remote ssh session (unchanged)", tool: "claude", transcript: true, ssh: true, detectedAt: stale, want: false},
		{name: "non-Claude tool with a stale Claude id (unchanged)", tool: "shell", transcript: true, detectedAt: stale, want: false},
		{name: "gemini (unchanged)", tool: "gemini", transcript: true, detectedAt: stale, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			configDir := filepath.Join(home, ".claude")
			t.Setenv("CLAUDE_CONFIG_DIR", configDir)
			ClearUserConfigCache()
			t.Cleanup(ClearUserConfigCache)

			projectDir := t.TempDir()
			if tt.transcript {
				dir := projectDir
				if tt.elsewhere {
					dir = t.TempDir()
				}
				writeIssue3032Transcript(t, configDir, dir, sid)
			}
			inst := NewInstanceWithTool("issue-3032", projectDir, tt.tool)
			inst.ClaudeSessionID = sid
			inst.ClaudeDetectedAt = tt.detectedAt
			if tt.ssh {
				inst.SSHHost = "remote.example"
			}
			if got := inst.CanFork(); got != tt.want {
				t.Fatalf("CanFork() = %v, want %v", got, tt.want)
			}
		})
	}
}
