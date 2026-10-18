package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Review M2/M3: only a plain, unclipped `session output` (the way an agent
// reads a child from its Bash tool) records a result read. -q, --json,
// --copy, --pane and a clipped read do not. Driven through the real binary,
// so removing the noteResultRead call or its gate fails here.

// resultReadFiles returns the result-read files written under home.
func resultReadFiles(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(filepath.Dir(path)) == "result-reads" && strings.HasSuffix(path, ".json") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// writeClaudeTurnWithUUID writes a one-turn transcript whose assistant
// record carries a uuid, as Claude Code writes them.
func writeClaudeTurnWithUUID(t *testing.T, projectsDir, sessionID, uuid, reply string) {
	t.Helper()
	recs := []map[string]any{
		{"sessionId": sessionID, "type": "user", "uuid": "u-" + uuid, "timestamp": "2026-10-04T08:00:00Z",
			"message": map[string]any{"role": "user", "content": "go"}},
		{"sessionId": sessionID, "type": "assistant", "uuid": uuid, "timestamp": "2026-10-04T08:00:01Z",
			"message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": reply}}}},
	}
	var lines []string
	for _, r := range recs {
		b, _ := json.Marshal(r)
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(filepath.Join(projectsDir, sessionID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOutput_OnlyPlainUnclippedReadRecordsResultRead(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	reply := strings.Repeat("All tests pass and the branch is pushed. ", 40)
	cases := []struct {
		name   string
		args   []string
		record bool
	}{
		{"plain read", nil, true},
		{"quiet", []string{"-q"}, false},
		{"quiet long", []string{"--quiet"}, false},
		{"json", []string{"--json"}, false},
		{"copy", []string{"--copy"}, false},
		{"pane", []string{"--pane"}, false},
		{"clipped", []string{"--max-tokens", "20"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			project := filepath.Join(home, "proj")
			id := sessionMoveAddSession(t, home, project, "worker")
			const claudeID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
			if stdout, stderr, code := runAgentDeck(t, home, "session", "set", id, "claude-session-id", claudeID); code != 0 {
				t.Fatalf("set claude-session-id (exit %d)\n%s\n%s", code, stdout, stderr)
			}
			resolved := project
			if r, err := filepath.EvalSymlinks(project); err == nil {
				resolved = r
			}
			projectsDir := filepath.Join(home, ".claude", "projects", session.ConvertToClaudeDirName(resolved))
			if err := os.MkdirAll(projectsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeClaudeTurnWithUUID(t, projectsDir, claudeID, "assistant-final-1", reply)

			args := append([]string{"session", "output", id}, tc.args...)
			_, _, _ = runAgentDeckEnv(t, home, "", []string{"AGENTDECK_INSTANCE_ID=reader-parent-1"}, args...)
			files := resultReadFiles(t, home)
			if !tc.record {
				if len(files) != 0 {
					t.Fatalf("session output %v recorded a result read: %v", tc.args, files)
				}
				return
			}
			if len(files) != 1 {
				t.Fatalf("plain session output recorded %d result-read files, want 1", len(files))
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "assistant-final-1") || !strings.Contains(string(data), id) {
				t.Fatalf("result read does not name the child and its turn: %s", data)
			}
		})
	}
}

func TestResultReadShown(t *testing.T) {
	if !resultReadShown(false, false, false, true) {
		t.Fatal("a plain unclipped read must count")
	}
	for _, c := range [][4]bool{{true, false, false, true}, {false, true, false, true}, {false, false, true, true}, {false, false, false, false}} {
		if resultReadShown(c[0], c[1], c[2], c[3]) {
			t.Fatalf("resultReadShown(json=%v quiet=%v copy=%v unclipped=%v) = true", c[0], c[1], c[2], c[3])
		}
	}
}

func TestReportWithdrawnRecords(t *testing.T) {
	var none strings.Builder
	reportWithdrawnRecords(&none, nil, true)
	if none.Len() != 0 {
		t.Fatalf("nothing withdrawn must print nothing: %q", none.String())
	}
	recs := []session.TransitionNotificationEvent{{ChildSessionID: "c1", ChildTitle: "worker", TurnUUID: "u1"}, {ChildSessionID: "c2", TurnUUID: "u2"}}
	var text strings.Builder
	reportWithdrawnRecords(&text, recs, false)
	if got := text.String(); !strings.Contains(got, "withdrawn 2 record(s)") || !strings.Contains(got, "c1@u1") || !strings.Contains(got, "c2@u2") {
		t.Fatalf("text report = %q", got)
	}
	var js strings.Builder
	reportWithdrawnRecords(&js, recs, true)
	var out struct {
		Withdrawn int `json:"withdrawn"`
		Records   []struct {
			Child string `json:"child_session_id"`
			Turn  string `json:"turn_uuid"`
		} `json:"withdrawn_records"`
	}
	if err := json.Unmarshal([]byte(js.String()), &out); err != nil || out.Withdrawn != 2 || len(out.Records) != 2 || out.Records[0].Child != "c1" || out.Records[1].Turn != "u2" {
		t.Fatalf("json report = %q (%v)", js.String(), err)
	}
}
