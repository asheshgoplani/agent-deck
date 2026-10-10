package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBuildListJSONTranscriptMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	inst := &session.Instance{ID: "fixture", Tool: "claude", ProjectPath: filepath.Join(home, "project"), ClaudeSessionID: "22222222-2222-2222-2222-222222222222"}
	path := session.ClaudeTranscriptPathForInstance(inst)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := buildListJSON("test", []*session.Instance{inst}, map[*session.Instance]bool{inst: true})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["transcript_path"] != path || rows[0]["claude_session_id"] != inst.ClaudeSessionID {
		t.Fatalf("metadata lost: %s", data)
	}
}

// CORE-CHANGES 5: the public `list --json` and `list --all --json` carry the
// transcript location and the native id, so a client opens the conversation
// without a session show round trip or a find on the host.
func TestListJSONTranscriptPathCLI(t *testing.T) {
	home, id, transcript := rowsTestSession(t)
	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list: %d %s %s", code, stdout, stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("list JSON: %v %s", err, stdout)
	}
	found := false
	for _, row := range rows {
		if row["id"] == id {
			found = true
			if row["transcript_path"] != transcript || row["claude_session_id"] != "11111111-2222-3333-4444-555555555555" {
				t.Fatalf("list row lacks transcript metadata: %v", row)
			}
		}
	}
	if !found {
		t.Fatalf("session %s missing from list: %s", id, stdout)
	}
	stdout, stderr, code = runAgentDeck(t, home, "list", "--all", "--json")
	if code != 0 || !strings.Contains(stdout, `"transcript_path"`) || !strings.Contains(stdout, filepath.Base(transcript)) {
		t.Fatalf("list --all: %d %s %s", code, stdout, stderr)
	}
}
