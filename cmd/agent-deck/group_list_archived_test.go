package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `group list --json` (what the remote agent's change probe also pushes)
// leaves archived sessions out of every count, the rule #3127 applied to
// `status`, the TUI header pill, the group preview and remote rows. Archived
// rows from before #3127 can keep a stored `error`, so a raw count reported
// each one as a crash. A group whose sessions are all archived is still
// listed, with a count of 0, like an empty group.
func TestBuildGroupListJSON_LeavesArchivedOut(t *testing.T) {
	type sess struct {
		group    string
		status   session.Status
		archived bool
	}
	type status struct {
		Running, Waiting, Idle, Error, Stopped int
	}
	type want struct {
		count  int
		status *status // nil: no status object (count 0)
	}
	cases := []struct {
		name     string
		sessions []sess
		groups   map[string]want
		total    int
	}{
		{
			name: "old archived error row next to live rows",
			sessions: []sess{
				{"ops", session.StatusRunning, false},
				{"ops", session.StatusWaiting, false},
				{"ops", session.StatusError, true},
			},
			groups: map[string]want{
				"ops": {2, &status{Running: 1, Waiting: 1}},
			},
			total: 2,
		},
		{
			name: "every session archived",
			sessions: []sess{
				{"old", session.StatusError, true},
				{"old", session.StatusStopped, true},
			},
			groups: map[string]want{
				"old": {0, nil},
			},
			total: 0,
		},
		{
			name: "archived rows in a subgroup stay out of the parent",
			sessions: []sess{
				{"deck", session.StatusIdle, false},
				{"deck/workers", session.StatusError, false},
				{"deck/workers", session.StatusError, true},
				{"deck/workers/done", session.StatusRunning, true},
			},
			groups: map[string]want{
				"deck":              {2, &status{Idle: 1, Error: 1}},
				"deck/workers":      {1, &status{Error: 1}},
				"deck/workers/done": {0, nil},
			},
			total: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var instances []*session.Instance
			archived := map[*session.Instance]bool{}
			for i, s := range tc.sessions {
				inst := session.NewInstanceWithGroup("s"+string(rune('a'+i)), "/tmp/s", s.group)
				inst.Status = s.status
				if s.archived {
					inst.SetArchivedAtThreadSafe(time.Now())
					archived[inst] = true
				}
				instances = append(instances, inst)
			}
			tree := session.NewGroupTreeWithGroups(instances, nil)

			orig := groupListRefreshStatus
			t.Cleanup(func() { groupListRefreshStatus = orig })
			groupListRefreshStatus = func(inst *session.Instance) {
				if archived[inst] {
					t.Errorf("archived session %q refreshed; it is not counted", inst.Title)
				}
			}

			out, err := buildGroupListJSON(tree)
			if err != nil {
				t.Fatalf("buildGroupListJSON: %v", err)
			}
			type node struct {
				Path         string  `json:"path"`
				SessionCount int     `json:"session_count"`
				Status       *status `json:"status"`
				Children     []node  `json:"children"`
			}
			var doc struct {
				Groups        []node `json:"groups"`
				TotalSessions int    `json:"total_sessions"`
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("unmarshal: %v\n%s", err, out)
			}
			if doc.TotalSessions != tc.total {
				t.Errorf("total_sessions = %d, want %d", doc.TotalSessions, tc.total)
			}
			nodes := map[string]node{}
			var walk func([]node)
			walk = func(ns []node) {
				for _, n := range ns {
					nodes[n.Path] = n
					walk(n.Children)
				}
			}
			walk(doc.Groups)

			for path, w := range tc.groups {
				n, ok := nodes[path]
				if !ok {
					t.Fatalf("group %q missing; a group of archived sessions is still listed\n%s", path, out)
				}
				if n.SessionCount != w.count {
					t.Errorf("group %q session_count = %d, want %d", path, n.SessionCount, w.count)
				}
				switch {
				case w.status == nil && n.Status != nil:
					t.Errorf("group %q status = %+v, want none (no sessions counted)", path, *n.Status)
				case w.status != nil && n.Status == nil:
					t.Errorf("group %q status missing, want %+v", path, *w.status)
				case w.status != nil && *n.Status != *w.status:
					t.Errorf("group %q status = %+v, want %+v", path, *n.Status, *w.status)
				}
			}
		})
	}
}

// The same rule end to end, through the binary: both `group list`
// implementations (the core registry one and the legacy one behind
// AGENT_DECK_CORE_REGISTRY=0), in text and --json. The archived session's
// stored status is set back to `error` after archiving, as a row archived
// before #3127 still has it.
func TestGroupList_CLILeavesArchivedOut(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := t.TempDir()
	add := func(title string) string {
		t.Helper()
		out, stderr, code := runAgentDeck(t, home, "add", "-c", "claude", "--no-parent", "--json", "-t", title, "-g", "ops", project)
		if code != 0 {
			t.Fatalf("add %s: %d %s %s", title, code, out, stderr)
		}
		var added struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
			t.Fatalf("add response: %v %s", err, out)
		}
		return added.ID
	}
	add("kept")
	oldID := add("old")
	if out, stderr, code := runAgentDeck(t, home, "session", "archive", "old", "--json"); code != 0 {
		t.Fatalf("archive: %d %s %s", code, out, stderr)
	}
	setStoredStatus(t, home, oldID, session.StatusError)

	for _, tc := range []struct {
		name     string
		registry string
		json     bool
	}{
		{"core text", "1", false},
		{"core json", "1", true},
		{"legacy text", "0", false},
		{"legacy json", "0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"group", "list"}
			if tc.json {
				args = append(args, "--json")
			}
			out, stderr, code := runAgentDeckEnv(t, home, "", []string{envCoreRegistry + "=" + tc.registry}, args...)
			if code != 0 {
				t.Fatalf("group list: %d %s %s", code, out, stderr)
			}
			if !tc.json {
				// "ops" holds one counted session; the total agrees.
				var opsLine string
				for _, line := range strings.Split(out, "\n") {
					if f := strings.Fields(line); len(f) >= 2 && f[0] == "ops" {
						opsLine = line
					}
				}
				if f := strings.Fields(opsLine); len(f) < 2 || f[1] != "1" {
					t.Errorf("ops row = %q, want a session count of 1 (archived left out)\n%s", opsLine, out)
				}
				if !strings.Contains(out, ", 1 sessions\n") {
					t.Errorf("total line must count 1 session (archived left out)\n%s", out)
				}
				return
			}
			var doc struct {
				Groups []struct {
					Path         string `json:"path"`
					SessionCount int    `json:"session_count"`
					Status       *struct {
						Error int `json:"error"`
					} `json:"status"`
				} `json:"groups"`
				TotalSessions int `json:"total_sessions"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("decode: %v\n%s", err, out)
			}
			if doc.TotalSessions != 1 {
				t.Errorf("total_sessions = %d, want 1 (archived left out)\n%s", doc.TotalSessions, out)
			}
			found := false
			for _, g := range doc.Groups {
				if g.Path != "ops" {
					continue
				}
				found = true
				if g.SessionCount != 1 {
					t.Errorf("ops session_count = %d, want 1 (archived left out)", g.SessionCount)
				}
				// "kept" has no pane and may read error itself; the archived
				// error row must not add a second one.
				if g.Status == nil || g.Status.Error > 1 {
					t.Errorf("ops status = %+v, want at most the live session's own error", g.Status)
				}
			}
			if !found {
				t.Fatalf("ops group missing\n%s", out)
			}
		})
	}
}
