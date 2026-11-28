package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// `list --json` carries waiting_since for a waiting session from the
// state.db status-change stamp (PR 3170 re-review F1), so needs-me can say
// how long it has waited and sort longest first.
func TestListJSONWaitingSince(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	at := time.Date(2026, 11, 17, 21, 30, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status session.Status
		stamp  *statedb.StatusStamp
		want   string
	}{
		{name: "waiting with a waiting stamp", status: session.StatusWaiting, stamp: &statedb.StatusStamp{Status: "waiting", At: at}, want: "2026-11-17T21:30:00Z"},
		{name: "waiting, stamp is for another status", status: session.StatusWaiting, stamp: &statedb.StatusStamp{Status: "running", At: at}, want: ""},
		{name: "waiting, no stamp", status: session.StatusWaiting, want: ""},
		{name: "idle with an old waiting stamp", status: session.StatusIdle, stamp: &statedb.StatusStamp{Status: "waiting", At: at}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := &session.Instance{ID: "w1", Title: "w1", Tool: "claude", Status: tc.status}
			stamps := map[string]statedb.StatusStamp{}
			if tc.stamp != nil {
				stamps[inst.ID] = *tc.stamp
			}
			all := []*session.Instance{inst}
			out, err := buildListJSONRows("default", all, all, map[*session.Instance]bool{inst: true}, stamps)
			if err != nil {
				t.Fatal(err)
			}
			var rows []map[string]any
			if err := json.Unmarshal(out, &rows); err != nil {
				t.Fatal(err)
			}
			got, present := rows[0]["waiting_since"]
			if tc.want == "" {
				if present {
					t.Fatalf("waiting_since = %v, want absent", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("waiting_since = %v, want %s", got, tc.want)
			}
		})
	}
}
