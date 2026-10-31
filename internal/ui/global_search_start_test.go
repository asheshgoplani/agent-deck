package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestGlobalSearchStart_RunsConfiguredClaudeWithConversationID drives the real
// global-search open path: the instance createSessionFromGlobalSearch builds is
// Start()ed in real tmux with [claude].command pointing at a recording wrapper.
//
// Before the fix the TUI baked `exec claude --resume <id>` into inst.Command;
// the resume builder (since #1984) treats any Command other than "claude" as a
// custom binary, so the pane ran `exec exec claude --resume <id> ...` and died
// with `bash: exec: exec: not found` (exit 127). The configured wrapper never
// ran either way.
//
// The hit's SessionID is the "sessionId" field of an indexed transcript, i.e.
// file data, so the hostile row checks it still reaches the wrapper as one
// argument without the shell executing any of it.
func TestGlobalSearchStart_RunsConfiguredClaudeWithConversationID(t *testing.T) {
	testutil.SkipShort(t, "starts real tmux sessions; runs in CI without -short")
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	cases := []struct {
		name       string
		sessionID  string
		transcript bool
		wantFlag   string
	}{
		{"transcript_on_disk_resumes", "3f2b8c1e-9a4d-4e7f-b6c5-0d1e2f3a4b5c", true, "--resume"},
		{"no_transcript_uses_session_id", "8d0c6a2b-1e4f-4a3b-9c7d-2e5f6a7b8c9d", false, "--session-id"},
		// The $(...) sits inside the id word itself, so an unquoted id would
		// run it even though exec replaces the shell before `; touch` runs.
		{"hostile_id_stays_one_argument", "abc$(touch PWNED); touch PWNED", false, "--session-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			wrapper := filepath.Join(binDir, "claude-wrapper")
			record := filepath.Join(t.TempDir(), "record")
			writeGlobalSearchRecordingWrapper(t, wrapper, record)
			writeBadgeConfig(t, "[claude]\ncommand = "+tomlQuoteString(wrapper)+"\n")
			home := os.Getenv("HOME")

			project := t.TempDir()
			if resolved, err := filepath.EvalSymlinks(project); err == nil {
				project = resolved
			}
			if tc.transcript {
				writeGlobalSearchTranscript(t, filepath.Join(home, ".claude"), project, tc.sessionID)
			}

			inst := newGlobalSearchInstance(&GlobalSearchResult{SessionID: tc.sessionID, CWD: project}, session.DefaultGroupPath)
			if err := inst.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { _ = inst.Kill() })

			args := waitForGlobalSearchRecord(t, record, inst)
			if len(args) < 2 || args[0] != tc.wantFlag || args[1] != tc.sessionID {
				t.Errorf("wrapper argv = %q, want %s %q as the first two arguments", args, tc.wantFlag, tc.sessionID)
			}
			if _, err := os.Stat(filepath.Join(project, "PWNED")); err == nil {
				t.Errorf("conversation id was executed by the shell: PWNED created in %s", project)
			}
		})
	}
}

// writeGlobalSearchRecordingWrapper writes a stand-in claude binary that
// records its argv, one per line, then exits. It writes to a temp file and
// renames so a poller never reads a half-written record.
func writeGlobalSearchRecordingWrapper(t *testing.T, path, record string) {
	t.Helper()
	q := shellescape.Quote(record)
	stub := "#!/bin/sh\n{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } > " + q + ".tmp && mv " + q + ".tmp " + q + "\n"
	if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
}

// writeGlobalSearchTranscript puts a transcript with conversation data where
// the resume builder looks for it, so the start resumes rather than starting
// a fresh conversation under the same id.
func writeGlobalSearchTranscript(t *testing.T, configDir, project, sessionID string) {
	t.Helper()
	dir := filepath.Join(configDir, "projects", session.ConvertToClaudeDirName(project))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	line := `{"type":"user","sessionId":"` + sessionID + `","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func waitForGlobalSearchRecord(t *testing.T, record string, inst *session.Instance) []string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(record); err == nil {
			return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("configured claude wrapper never ran\npane command: %s", inst.GetTmuxSession().Command)
	return nil
}

func tomlQuoteString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
