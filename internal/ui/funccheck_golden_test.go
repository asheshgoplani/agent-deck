package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// These are source-render checks. They do not execute a supplied binary.
func TestFunctionalHomeGolden(t *testing.T) {
	forceTrueColorProfile()
	h := newSeamATestHome()
	h.width, h.height = 100, 30
	instances := []*session.Instance{
		{ID: "running", Title: "API worker", Tool: "shell", Status: session.StatusRunning},
		{ID: "idle", Title: "Notes", Tool: "shell", Status: session.StatusIdle},
	}
	for _, instance := range instances {
		h.flatItems = append(h.flatItems, session.Item{Type: session.ItemTypeSession, Session: instance})
	}
	h.refreshSessionRenderSnapshot(instances)
	assertFunctionalGolden(t, "home-list", h.renderSessionList(100, 12))
}

func TestFunctionalRemoteGolden(t *testing.T) {
	forceTrueColorProfile()
	for _, status := range []string{"running", "waiting", "idle", "error", "stopped", "queued", "archived"} {
		t.Run(status, func(t *testing.T) {
			h := newSeamATestHome()
			h.width = 100
			remote := session.RemoteSessionInfo{ID: "remote", Title: "Remote worker", Tool: "shell", Status: status, RemoteName: "lab", Archived: status == "archived"}
			var frame strings.Builder
			h.renderRemoteSessionItem(&frame, session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "lab"}, true)
			assertFunctionalGolden(t, "remote-"+status, frame.String())
		})
	}
}

func assertFunctionalGolden(t *testing.T, name, rendered string) {
	t.Helper()
	got := strings.TrimRight(stripAnsi(rendered), "\n") + "\n"
	path := filepath.Join("testdata", "funccheck", name+".txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s frame differs\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}
