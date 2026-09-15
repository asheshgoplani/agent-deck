package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemotePollGoldenFrames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	forceTrueColorProfile()
	for _, tc := range []struct {
		name, status, reason string
		active, cached       bool
	}{
		{"unknown", "unknown", "", true, false},
		{"cached", "unknown", "", true, true},
		{"auth-failed", "auth_failed", "auth failed", false, true},
		{"timeout", "timeout", "timeout", false, false},
		{"host-down", "host_down", "host down", false, false},
		{"recovered", "ok", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHomeWithItems(120, 30, nil)
			defer h.cancel()
			h.remotePolls = map[string]session.RemotePollState{"dev": {LastPollStatus: tc.status, LastPollError: tc.reason}}
			h.remotePollActive = map[string]bool{"dev": tc.active}
			h.remoteFromCache = map[string]bool{"dev": tc.cached}
			rs := session.RemoteSessionInfo{ID: "s1", Title: "Remote work", Tool: "claude", Status: "running", RemoteName: "dev"}
			h.remoteSessions = map[string][]session.RemoteSessionInfo{"dev": {rs}}
			h.remoteLatency = map[string]session.RemoteLatency{"dev": {MS: 42, MeasuredAt: time.Now()}}
			var frame strings.Builder
			for _, selected := range []bool{false, true} {
				h.renderRemoteGroupItem(&frame, session.Item{Type: session.ItemTypeRemoteGroup, Path: "remotes/dev", RemoteName: "dev"}, selected)
				h.renderRemoteSessionItem(&frame, session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "dev", RemoteSession: &rs, Level: 1, IsLastInGroup: true}, selected)
			}
			got := stripAnsi(frame.String())
			if tc.status != "ok" && strings.Contains(got, "●") {
				t.Fatal("unreachable/unknown remote must not claim a live session")
			}
			path := filepath.Join("testdata", "remote_poll", tc.name+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != got {
				t.Fatalf("frame changed:\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}
