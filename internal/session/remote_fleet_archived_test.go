package session

import (
	"context"
	"testing"
	"time"
)

// An archived remote session keeps a live-looking status, so the fleet
// counts (the web Fleet pane's RUNNING/WAITING/ERROR tiles and its
// "N sessions" line) must leave it out, the same rule as `remote sessions`
// text output (#2972) and local `status`. The rows themselves stay in the
// snapshot with "archived": true, like `remote sessions --json`.
func TestRemoteFleetCountsLeaveOutArchivedSessions(t *testing.T) {
	scanner := NewRemoteFleetScanner()
	scanner.loadConfig = func() (*UserConfig, error) {
		return &UserConfig{Remotes: map[string]RemoteConfig{"box": {Host: "box@example"}}}, nil
	}
	scanner.newRunner = func(string, RemoteConfig) remoteFleetRunner {
		return fakeFleetRemoteRunner{sessions: []RemoteSessionInfo{
			{ID: "live-1", Title: "live", Status: "running"},
			{ID: "arch-w", Title: "old waiting", Status: "waiting", Archived: true},
			{ID: "arch-e", Title: "old error", Status: "error", Archived: true},
			{ID: "arch-r", Title: "old running", Status: "running", Archived: true},
		}}
	}
	scanner.now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }

	scanner.refreshOnce(context.Background())
	snap := scanner.Snapshot()

	want := RemoteFleetCounts{RemotesOnline: 1, Sessions: 1, Running: 1}
	if snap.Counts != want {
		t.Fatalf("counts = %+v, want %+v (archived remote sessions must not count)", snap.Counts, want)
	}
	if got := len(snap.Remotes[0].Sessions); got != 4 {
		t.Fatalf("snapshot rows = %d, want 4: archived rows stay in the JSON with archived=true", got)
	}
}
