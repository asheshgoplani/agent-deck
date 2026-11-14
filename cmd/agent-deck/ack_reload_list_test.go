package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// `agent-deck list --json` rebuilds every session from state.db and runs a
// status pass that reads each hook file from disk. These tests drive the
// built binary: a session the user already looked at must stay seen (idle)
// when its Stop hook is older than the visit, and turn waiting again when the
// Stop is newer.

// seedAckedClaudeSession writes one waiting Claude session on a live, inert
// tmux pane, marks it seen visitAgo ago, and leaves a Stop hook file from
// stopAgo ago. It returns the sandbox HOME and the session id.
func seedAckedClaudeSession(t *testing.T, visitAgo, stopAgo time.Duration) (home, id string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	home = t.TempDir()
	// Resolve paths with the env agentDeckTestEnv gives the binary.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	id = "ack-reload"
	socket := fmt.Sprintf("ackreload%d", time.Now().UnixNano())
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPath, err := session.GetDBPathForProfile("ch_support_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	row := &statedb.InstanceRow{
		ID: id, Title: "ack reload", ProjectPath: project, GroupPath: "my-sessions",
		Tool: "claude", Status: string(session.StatusWaiting), TmuxSession: "agentdeck_" + id,
		TmuxSocketName: socket, CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAcknowledged(id, true); err != nil {
		t.Fatal(err)
	}
	// SetAcknowledged stamps the visit now; move it back for an earlier visit.
	if visitAgo > 0 {
		if _, err := db.DB().Exec(`UPDATE instances SET acknowledged_at = ? WHERE id = ?`, time.Now().Add(-visitAgo).UnixMilli(), id); err != nil {
			t.Fatal(err)
		}
	}

	hook, _ := json.Marshal(map[string]any{"status": "waiting", "event": "Stop", "cwd": project, "ts": time.Now().Add(-stopAgo).Unix()})
	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, id+".json"), hook, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", row.TmuxSession, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v %s", err, out)
	}
	return home, id
}

// listJSONStatus returns the status `list --json` reports for id.
func listJSONStatus(t *testing.T, home, id string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list --json: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("list --json output: %v\n%s", err, stdout)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Status
		}
	}
	t.Fatalf("session %s missing from list --json:\n%s", id, stdout)
	return ""
}

// A Stop recorded before the user's visit is the turn they already saw.
func TestListJSONAck_StopOlderThanVisitStaysIdle(t *testing.T) {
	home, id := seedAckedClaudeSession(t, 0, 30*time.Second)
	if got := listJSONStatus(t, home, id); got != "idle" {
		t.Fatalf("list --json status = %q, want idle: the reload undid the user's visit", got)
	}
}

// A Stop recorded after the visit is a turn the user has not seen yet.
func TestListJSONAck_StopNewerThanVisitIsWaiting(t *testing.T) {
	home, id := seedAckedClaudeSession(t, 10*time.Minute, 30*time.Second)
	if got := listJSONStatus(t, home, id); got != "waiting" {
		t.Fatalf("list --json status = %q, want waiting: a turn finished after the visit shows as seen", got)
	}
}
