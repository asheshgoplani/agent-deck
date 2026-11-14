package session

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// A custom tool that runs claude carries the same statusLine and the same
// claude_session_id binding as the built-in claude tool, so its statusline
// record must link to its session and be readable by session id.
func TestClaudeStatuslineOwnerCustomClaudeTool(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.my-claude]
command = "claude --model opus"
`)
	if !IsClaudeCompatible("my-claude") {
		t.Fatal("setup: my-claude should be Claude-compatible")
	}
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, row := range []*statedb.InstanceRow{
		{ID: "s1", Title: "s1", Tool: "my-claude", ToolData: json.RawMessage(`{"claude_session_id":"abc"}`)},
		// A non-Claude tool holding a stale native id must not count as an owner.
		{ID: "s2", Title: "s2", Tool: "codex", ToolData: json.RawMessage(`{"claude_session_id":"abc"}`)},
	} {
		if err := db.SaveInstance(row); err != nil {
			t.Fatalf("save %s: %v", row.ID, err)
		}
	}
	owner, err := db.SaveClaudeStatusline(statedb.ClaudeStatusline{ClaudeSessionID: "abc", Cwd: "/p"})
	if err != nil {
		t.Fatalf("save statusline: %v", err)
	}
	if owner != "s1" {
		t.Fatalf("owner = %q, want s1", owner)
	}
	rec, err := db.ClaudeStatuslineForSession("s1")
	if err != nil {
		t.Fatalf("ClaudeStatuslineForSession(s1): %v", err)
	}
	if rec.Cwd != "/p" {
		t.Fatalf("record cwd = %q, want /p", rec.Cwd)
	}
}
