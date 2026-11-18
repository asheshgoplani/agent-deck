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

// waitingSeed is one waiting Claude session for the waiting_since command
// tests: stampAgo is when its status changed to waiting (0: no stamp, a row
// waiting since before status_changes existed); activityAgo is its last
// activity (its Stop hook).
type waitingSeed struct {
	id, title   string
	stampAgo    time.Duration
	activityAgo time.Duration
}

// seedWaitingSessions writes waiting Claude sessions on live, inert tmux
// panes with their status-change stamps, and returns the sandbox HOME and
// each session's expected stamp time.
func seedWaitingSessions(t *testing.T, seeds []waitingSeed) (string, map[string]time.Time) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

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
	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("waitsince%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	now := time.Now()
	want := map[string]time.Time{}
	for _, s := range seeds {
		row := &statedb.InstanceRow{
			ID: s.id, Title: s.title, ProjectPath: project, GroupPath: "team/alex",
			Tool: "claude", Status: string(session.StatusWaiting), TmuxSession: "agentdeck_" + s.id,
			TmuxSocketName: socket, CreatedAt: now.Add(-2 * time.Hour), ToolData: json.RawMessage(`{}`),
		}
		if err := db.SaveInstance(row); err != nil {
			t.Fatal(err)
		}
		// The insert trigger stamped "now"; move it to when the session
		// turned waiting, or drop it for a pre-upgrade row.
		if s.stampAgo > 0 {
			at := now.Add(-s.stampAgo)
			if _, err := db.DB().Exec(`UPDATE status_changes SET changed_at = ? WHERE id = ?`, at.UnixMilli(), s.id); err != nil {
				t.Fatal(err)
			}
			want[s.id] = time.UnixMilli(at.UnixMilli()).UTC()
		} else if _, err := db.DB().Exec(`DELETE FROM status_changes WHERE id = ?`, s.id); err != nil {
			t.Fatal(err)
		}
		hook, _ := json.Marshal(map[string]any{"status": "waiting", "event": "Stop", "cwd": project, "ts": now.Add(-s.activityAgo).Unix()})
		if err := os.WriteFile(filepath.Join(hooksDir, s.id+".json"), hook, 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"-L", socket, "new-session", "-d", "-s", row.TmuxSession, "sleep", "300"}
		if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session: %v %s", err, out)
		}
	}
	return home, want
}

// The default `list --json` (the core registry path, no
// AGENT_DECK_CORE_REGISTRY override) carries each waiting session's
// waiting_since from state.db, the same as the legacy handler. PR 3170
// re-review B1: only the legacy handler emitted it.
func TestListJSONCmd_DefaultPathCarriesWaitingSince(t *testing.T) {
	home, want := seedWaitingSessions(t, []waitingSeed{
		{id: "ws-alpha", title: "alpha", stampAgo: 2 * time.Minute, activityAgo: 2 * time.Minute},
		{id: "ws-bravo", title: "bravo", stampAgo: 10 * time.Minute, activityAgo: 10 * time.Minute},
	})
	for _, env := range []struct {
		name  string
		extra []string
	}{
		{name: "default (core registry)"},
		{name: "legacy handler", extra: []string{envCoreRegistry + "=0"}},
	} {
		t.Run(env.name, func(t *testing.T) {
			stdout, stderr, code := runAgentDeckEnv(t, home, "", env.extra, "list", "--json")
			if code != 0 {
				t.Fatalf("list --json: exit %d\n%s\n%s", code, stdout, stderr)
			}
			var rows []struct {
				ID           string `json:"id"`
				Status       string `json:"status"`
				WaitingSince string `json:"waiting_since"`
			}
			if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
				t.Fatalf("list --json output: %v\n%s", err, stdout)
			}
			if len(rows) != len(want) {
				t.Fatalf("got %d rows, want %d:\n%s", len(rows), len(want), stdout)
			}
			for _, r := range rows {
				if r.Status != "waiting" {
					t.Fatalf("%s status = %q, want waiting (seed did not take):\n%s", r.ID, r.Status, stdout)
				}
				got, err := time.Parse(time.RFC3339Nano, r.WaitingSince)
				if err != nil {
					t.Fatalf("%s waiting_since = %q, want %s:\n%s", r.ID, r.WaitingSince, want[r.ID].Format(time.RFC3339Nano), stdout)
				}
				if !got.Equal(want[r.ID]) {
					t.Fatalf("%s waiting_since = %s, want %s", r.ID, got, want[r.ID])
				}
			}
		})
	}
}

// needs-me through the real binary and the default `list --json`: waiting
// sessions come back longest-waiting first (not in title order), with since
// and waiting_seconds filled. A session waiting since before status_changes
// existed (no stamp) is placed by its last activity, not sorted last.
func TestNeedsMeCmd_LongestWaitingFirst(t *testing.T) {
	home, want := seedWaitingSessions(t, []waitingSeed{
		{id: "ws-alpha", title: "alpha", stampAgo: 2 * time.Minute, activityAgo: 2 * time.Minute},
		{id: "ws-bravo", title: "bravo", stampAgo: 10 * time.Minute, activityAgo: 10 * time.Minute},
		{id: "ws-charlie", title: "charlie", activityAgo: 30 * time.Minute},
	})
	stdout, stderr, code := runAgentDeck(t, home, "needs-me", "--json")
	if code != 0 {
		t.Fatalf("needs-me --json: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var res struct {
		Sessions []struct {
			ID             string `json:"id"`
			Since          string `json:"since"`
			WaitingSeconds int64  `json:"waiting_seconds"`
		} `json:"sessions"`
		Errors []any `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("needs-me --json output: %v\n%s", err, stdout)
	}
	var order []string
	for _, s := range res.Sessions {
		order = append(order, s.ID)
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{"ws-charlie", "ws-bravo", "ws-alpha"}) {
		t.Fatalf("needs-me order = %v, want [ws-charlie ws-bravo ws-alpha] (longest waiting first)\n%s", order, stdout)
	}
	for _, s := range res.Sessions {
		if s.Since == "" || s.WaitingSeconds <= 0 {
			t.Fatalf("%s since=%q waiting_seconds=%d, want both set\n%s", s.ID, s.Since, s.WaitingSeconds, stdout)
		}
		if at, ok := want[s.ID]; ok {
			wantSecs := int64(time.Since(at).Seconds())
			if d := s.WaitingSeconds - wantSecs; d < -5 || d > 5 {
				t.Fatalf("%s waiting_seconds = %d, want about %d", s.ID, s.WaitingSeconds, wantSecs)
			}
		}
	}
}
