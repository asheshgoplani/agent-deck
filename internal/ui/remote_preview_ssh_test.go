package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func sshTestSessions(now time.Time) []session.RemoteSSHSession {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return []session.RemoteSSHSession{
		{User: "alice", Count: 3, HasSince: true, Since: day.Add(8*time.Hour + 54*time.Minute), From: "203.0.113.7"},
		{User: "bob", Count: 1, HasSince: true, Since: day.Add(11*time.Hour + 2*time.Minute), From: "10.0.0.9"},
		{User: "carol", Count: 2, HasSince: true, Since: day.Add(9*time.Hour + 10*time.Minute), From: "10.0.0.5"},
	}
}

// TestRenderSSHPreviewBlock pins the ssh field's shapes: one line when it
// fits (most sessions first), a summary plus one row per user when it does
// not, capped with +N more; nobody; and unknown named honestly.
func TestRenderSSHPreviewBlock(t *testing.T) {
	now := time.Now()
	sessions := sshTestSessions(now)
	yesterday := []session.RemoteSSHSession{{User: "ops", Count: 1, HasSince: true, Since: now.Add(-30 * time.Hour), From: "10.0.0.1"}}

	t.Run("one line", func(t *testing.T) {
		got := renderSSHPreviewBlock(sessions, now, previewLayout{width: 120, rows: 10})
		want := "ssh  alice ×3 since 08:54 · carol ×2 since 09:10 · bob ×1 since 11:02"
		if len(got) != 1 || got[0] != want {
			t.Fatalf("block = %q, want %q", got, want)
		}
	})
	t.Run("rows when the line does not fit", func(t *testing.T) {
		got := renderSSHPreviewBlock(sessions, now, previewLayout{width: 50, rows: 10})
		want := []string{
			"ssh  3 users · 6 sessions",
			"  alice  ×3  since 08:54  from 203.0.113.7",
			"  carol  ×2  since 09:10  from 10.0.0.5",
			"  bob    ×1  since 11:02  from 10.0.0.9",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		for _, l := range got {
			if lipgloss.Width(l) > 50 {
				t.Errorf("too wide: %q", l)
			}
		}
	})
	t.Run("capped", func(t *testing.T) {
		got := renderSSHPreviewBlock(sessions, now, previewLayout{width: 50, rows: 3})
		if len(got) != 3 || got[2] != "  +2 more" {
			t.Fatalf("block = %q", got)
		}
	})
	t.Run("older than today shows the date", func(t *testing.T) {
		got := renderSSHPreviewBlock(yesterday, now, previewLayout{width: 120, rows: 10})
		if want := "ssh  ops ×1 since " + yesterday[0].Since.Format("Jan 2 15:04"); got[0] != want {
			t.Fatalf("block = %q, want %q", got, want)
		}
	})
	t.Run("nobody", func(t *testing.T) {
		got := renderSSHPreviewBlock(nil, now, previewLayout{width: 120, rows: 10})
		if len(got) != 1 || got[0] != "ssh  nobody connected" {
			t.Fatalf("block = %q", got)
		}
	})
}

// TestRemotePreviewFieldLines_SSHUnknown pins the unknown states: an older
// remote, a same-version remote that does not send the field, and a remote
// whose `who` failed.
func TestRemotePreviewFieldLines_SSHUnknown(t *testing.T) {
	now := time.Now()
	fields := []string{session.PreviewFieldSSH}
	older := session.RemoteVersionState{Version: "1.16.9", Found: true, CheckedAt: now}
	same := session.RemoteVersionState{Version: "1.16.11", Found: true, CheckedAt: now}
	ok := remoteHostStatsResult{Stats: session.RemoteHostStats{Ok: true}}

	if lines := remotePreviewFieldLines(older, "1.16.11", nil, ok, true, fields, now, previewLayout{}); len(lines) != 1 || lines[0] != "ssh  unknown (remote older than 1.16.11)" {
		t.Errorf("older: %q", lines)
	}
	if lines := remotePreviewFieldLines(same, "1.16.11", nil, ok, true, fields, now, previewLayout{}); len(lines) != 1 || lines[0] != "ssh  unknown (remote does not report ssh sessions)" {
		t.Errorf("same, no field: %q", lines)
	}
	failed := remoteHostStatsResult{Stats: session.RemoteHostStats{Ok: true, SSHError: "who: not found"}}
	if lines := remotePreviewFieldLines(same, "1.16.11", nil, failed, true, fields, now, previewLayout{}); len(lines) != 1 || lines[0] != "ssh  unknown (who: not found)" {
		t.Errorf("who failed: %q", lines)
	}
	if lines := remotePreviewFieldLines(same, "1.16.11", nil, remoteHostStatsResult{}, false, fields, now, previewLayout{}); len(lines) != 1 || lines[0] != "ssh  unknown (stats unknown)" {
		t.Errorf("no stats: %q", lines)
	}
}

// TestRemotePreview_SSHGolden renders the ssh field with three users and
// with an older remote, at width 100.
func TestRemotePreview_SSHGolden(t *testing.T) {
	fixedPollTime := time.Now().Add(-5 * time.Minute)
	sessions := sshTestSessions(fixedPollTime)
	steps := []struct {
		name  string
		setup func(h *Home)
	}{
		{"ssh-3users-w100", func(h *Home) {
			h.remoteVersions = map[string]session.RemoteVersionState{"lab": {Version: "1.16.11", Found: true, CheckedAt: fixedPollTime}}
			h.remoteHostStats = map[string]remoteHostStatsResult{"lab": {
				Stats:     session.RemoteHostStats{Ok: true, SSHAvailable: true, SSHSessions: sessions},
				Latency:   1200 * time.Millisecond,
				FetchedAt: fixedPollTime,
			}}
		}},
		{"ssh-3users-w60", func(h *Home) {
			h.remoteVersions = map[string]session.RemoteVersionState{"lab": {Version: "1.16.11", Found: true, CheckedAt: fixedPollTime}}
			h.remoteHostStats = map[string]remoteHostStatsResult{"lab": {
				Stats:     session.RemoteHostStats{Ok: true, SSHAvailable: true, SSHSessions: sessions},
				Latency:   1200 * time.Millisecond,
				FetchedAt: fixedPollTime,
			}}
		}},
		{"ssh-unknown-w100", func(h *Home) {
			h.remoteVersions = map[string]session.RemoteVersionState{"lab": {Version: "1.16.9", Found: true, CheckedAt: fixedPollTime}}
			h.remoteHostStats = map[string]remoteHostStatsResult{"lab": {
				Stats:     session.RemoteHostStats{Ok: true},
				Latency:   1200 * time.Millisecond,
				FetchedAt: fixedPollTime,
			}}
		}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			home := goldenRemotePreviewHome(t)
			withControllerVersion(t, "1.16.11")
			writeXDGTestConfig(t, os.Getenv("HOME"), `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"

[ui.remote_preview]
fields = ["version", "sessions_by_status", "ssh"]
`)
			step.setup(home)
			width := 100
			if strings.HasSuffix(step.name, "w60") {
				width = 60
			}
			got := strings.TrimRight(stripAnsi(home.renderRemotePreview(home.flatItems[0], width, 30)), "\n") + "\n"
			assertFrameFits(t, got, width)
			path := filepath.Join("testdata", "remote_preview", step.name+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
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

// TestHeaderFields_SSHOptIn pins that "ssh" is opt-in for the local header
// and renders from the local collector's snapshot.
func TestHeaderFields_SSHOptIn(t *testing.T) {
	withTempAgentDeckHome(t, `
[ui.header]
fields = ["version", "ssh"]
`)
	home := NewHome()
	home.width = 200
	home.height = 30
	home.initialLoading = false
	if home.sshCollector == nil {
		t.Fatal("fields=[ssh] must construct the ssh collector")
	}
	// Before the first poll the snapshot is unavailable: unknown, not nobody.
	out := stripAnsi(home.View())
	if !strings.Contains(out, "ssh  unknown") {
		t.Errorf("header before first poll must read ssh unknown: %s", out)
	}

}

// TestHeaderFields_DefaultOmitsSSH: unset config never shows or collects ssh.
func TestHeaderFields_DefaultOmitsSSH(t *testing.T) {
	withTempAgentDeckHome(t, "")
	plain := NewHome()
	plain.width = 200
	plain.height = 30
	plain.initialLoading = false
	if plain.sshCollector != nil || strings.Contains(stripAnsi(plain.View()), "ssh ") {
		t.Errorf("default header must not show or collect ssh")
	}
}
