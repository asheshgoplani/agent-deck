package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #2769: GetJSONLPath resolved the transcript under the process-wide
// GetClaudeConfigDir(), so a session whose account has its own config_dir
// never found its transcript. Turn classification and the comms ingest then
// saw every turn as unclassified (no text, no tier, no journal line).

const issue2769Config = `
[profiles.alpha.claude]
config_dir = "~/.claude-alpha"
`

func issue2769WriteTranscript(t *testing.T, configDir, projectPath, sessionID string) string {
	t.Helper()
	resolved := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolved = r
	}
	dir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func issue2769Instance(t *testing.T, home, sessionID string) *Instance {
	t.Helper()
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithGroupAndTool("w", project, "work", "claude")
	inst.ClaudeSessionID = sessionID
	return inst
}

func TestIssue2769_GetJSONLPathFindsTranscriptUnderAccountConfigDir(t *testing.T) {
	home := withTempAgentDeckHome(t, issue2769Config)
	inst := issue2769Instance(t, home, "issue2769-account-session")
	inst.Account = "alpha"

	want := issue2769WriteTranscript(t, filepath.Join(home, ".claude-alpha"), inst.ProjectPath, inst.ClaudeSessionID)

	if got := inst.GetJSONLPath(); got != want {
		t.Fatalf("GetJSONLPath() = %q, want the transcript under the account config dir %q", got, want)
	}
}

func TestIssue2769_GetJSONLPathKeepsProcessWideDirFallback(t *testing.T) {
	home := withTempAgentDeckHome(t, issue2769Config)
	inst := issue2769Instance(t, home, "issue2769-global-session")
	inst.Account = "alpha"

	// A transcript that only exists under the process-wide dir (for example
	// written before an account switch) is still found.
	want := issue2769WriteTranscript(t, filepath.Join(home, ".claude"), inst.ProjectPath, inst.ClaudeSessionID)

	if got := inst.GetJSONLPath(); got != want {
		t.Fatalf("GetJSONLPath() = %q, want the process-wide transcript %q", got, want)
	}
}
