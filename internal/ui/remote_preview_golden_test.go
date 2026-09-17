package ui

// Golden frames for the remote preview panel: what the right-side pane shows
// when a `remotes/<name>` row is selected, across the version-compare states
// (same/older/newer/unknown) and the stats block (unknown vs. present).
// Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run TestRemotePreview_Golden
//
// and review the diff like any other test change.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// goldenRemotePreviewHome builds a Home parked on a single remote group
// header ("remotes/lab") with a fixed set of sessions, so the sessions-by-
// status and harnesses lines are deterministic across the golden steps.
func goldenRemotePreviewHome(t *testing.T) *Home {
	t.Helper()
	forceTrueColorProfile()
	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	withControllerVersion(t, "1.16.10")

	home := NewHome()
	home.width = 100
	home.height = 30
	home.initialLoading = false
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"lab": {
			{ID: "r1", Title: "one", Group: "work", Status: "running", Tool: "claude"},
			{ID: "r2", Title: "two", Group: "work", Status: "waiting", Tool: "claude"},
			{ID: "r3", Title: "three", Group: "", Status: "idle", Tool: "codex"},
			{ID: "r4", Title: "four", Group: "", Status: "stopped", Tool: "codex"},
			{ID: "r5", Title: "five", Group: "", Status: "error", Tool: "pi"},
		},
	}
	home.flatItems = []session.Item{
		{Type: session.ItemTypeRemoteGroup, RemoteName: "lab", Path: "remotes/lab", Level: 0},
	}
	home.cursor = 0
	return home
}

func TestRemotePreview_Golden(t *testing.T) {
	// Anchored to "now" (not a fixed calendar date) so the rendered relative
	// time ("Last poll ... N ago") stays the same delta no matter when this
	// test runs; CheckedAt itself never appears in the rendered text for a
	// known version, only for the unknown state (which reads "never").
	fixedPollTime := time.Now().Add(-5 * time.Minute)

	steps := []struct {
		name  string
		setup func(t *testing.T, h *Home)
	}{
		{"01-version-same", func(t *testing.T, h *Home) {
			h.remoteVersions = map[string]session.RemoteVersionState{
				"lab": {Version: "1.16.10", Found: true, CheckedAt: fixedPollTime},
			}
		}},
		{"02-version-older", func(t *testing.T, h *Home) {
			h.remoteVersions = map[string]session.RemoteVersionState{
				"lab": {Version: "1.16.9", Found: true, CheckedAt: fixedPollTime},
			}
		}},
		{"03-version-newer", func(t *testing.T, h *Home) {
			h.remoteVersions = map[string]session.RemoteVersionState{
				"lab": {Version: "1.16.11", Found: true, CheckedAt: fixedPollTime},
			}
		}},
		{"04-version-unknown", func(t *testing.T, h *Home) {
			// Never checked: no entry in the map at all.
		}},
		{"05-stats-known", func(t *testing.T, h *Home) {
			h.remoteVersions = map[string]session.RemoteVersionState{
				"lab": {Version: "1.16.10", Found: true, CheckedAt: fixedPollTime},
			}
			h.remoteHostStats = map[string]remoteHostStatsResult{
				"lab": {
					Stats: session.RemoteHostStats{
						Ok:              true,
						CPUAvailable:    true,
						CPUUsagePercent: 28,
						MemAvailable:    true,
						MemUsedBytes:    38_200_000_000,
						MemTotalBytes:   48_000_000_000,
						DiskAvailable:   true,
						DiskUsedBytes:   715_000_000_000,
						DiskTotalBytes:  926_000_000_000,
					},
					Latency:   1200 * time.Millisecond,
					FetchedAt: fixedPollTime,
				},
			}
		}},
		// 06: a custom [ui.remote_preview].fields list — harnesses first,
		// then only memory (dropping load/disk/last_poll), then version
		// last. Pins that a non-default field list both reorders the panel
		// and drops the fields it omits, and that a single sub-field out of
		// load/memory/disk renders alone rather than as the combined line.
		{"06-custom-fields", func(t *testing.T, h *Home) {
			writeXDGTestConfig(t, os.Getenv("HOME"), `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"

[ui.remote_preview]
fields = ["harnesses", "memory", "version"]
`)
			h.remoteVersions = map[string]session.RemoteVersionState{
				"lab": {Version: "1.16.9", Found: true, CheckedAt: fixedPollTime},
			}
			h.remoteHostStats = map[string]remoteHostStatsResult{
				"lab": {
					Stats: session.RemoteHostStats{
						Ok:              true,
						CPUAvailable:    true,
						CPUUsagePercent: 28,
						MemAvailable:    true,
						MemUsedBytes:    38_200_000_000,
						MemTotalBytes:   48_000_000_000,
						DiskAvailable:   true,
						DiskUsedBytes:   715_000_000_000,
						DiskTotalBytes:  926_000_000_000,
					},
					Latency:   1200 * time.Millisecond,
					FetchedAt: fixedPollTime,
				},
			}
		}},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			home := goldenRemotePreviewHome(t)
			step.setup(t, home)
			got := strings.TrimRight(stripAnsi(home.renderRemotePreview(home.flatItems[0], 100, 30)), "\n") + "\n"
			path := filepath.Join("testdata", "remote_preview", step.name+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
			}
			if string(want) != got {
				t.Fatalf("golden %s differs from the rendered preview.\n--- want\n%s\n--- got\n%s", path, want, got)
			}
		})
	}
}
