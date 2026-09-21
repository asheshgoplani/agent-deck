package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/charmbracelet/lipgloss"
)

// TestUpdateRunGuardBannerKeyTable is the issue #2336 table: every guard
// state while a newer build is on disk, the banner it must show, and what
// the restart key does. The banner may only promise what the key does.
func TestUpdateRunGuardBannerKeyTable(t *testing.T) {
	progress := progressFrom
	tests := []struct {
		name       string
		auto       bool
		arrange    func(h *Home)
		banner     []string // all must appear at width 200
		notBanner  string   // must not appear
		keyQueued  bool     // key queues the restart
		keyRefused string   // key refused with this reason
		keyRestart bool     // key restarts now
	}{
		{
			name:       "idle",
			auto:       true,
			arrange:    func(h *Home) {},
			banner:     []string{"restarting when idle (ctrl+t now)"},
			keyRestart: true,
		},
		{
			name:       "idle, auto_restart off",
			arrange:    func(h *Home) {},
			banner:     []string{"press ctrl+t to restart"},
			keyRestart: true,
		},
		{
			name: "nudging remotes (default model)",
			auto: true,
			arrange: func(h *Home) {
				h.autoInstallInFlight = "1.16.1"
				h.autoInstallProgress = progress("nudging 4 remote(s) to check for v1.16.1 now\n")
			},
			banner:    []string{"v1.16.1 installed, nudging 4 remotes to update, then restarting"},
			notBanner: "ctrl+t now",
			keyQueued: true,
		},
		{
			name: "opt-in push sweep",
			auto: true,
			arrange: func(h *Home) {
				h.autoInstallInFlight = "1.16.1"
				h.autoInstallProgress = progress("sweep_remotes is on: pushing v1.16.1 to 4 remote(s)\n")
			},
			banner:    []string{"finishing the remote sweep (4 remotes), then restarting"},
			notBanner: "ctrl+t now",
			keyQueued: true,
		},
		{
			name: "run in flight before any remote phase, auto_restart off",
			arrange: func(h *Home) {
				h.autoInstallInFlight = "1.16.1"
			},
			banner:    []string{"finishing the update, then press ctrl+t to restart"},
			notBanner: "ctrl+t now",
			keyQueued: true,
		},
		{
			name: "launchd re-registration running",
			auto: true,
			arrange: func(h *Home) {
				h.autoInstallInFlight = pendingDrainKey
			},
			banner:    []string{"re-registering launchd agents, then restarting"},
			notBanner: "ctrl+t now",
			keyQueued: true,
		},
		{
			name: "queued during the sweep",
			auto: true,
			arrange: func(h *Home) {
				h.autoInstallInFlight = "1.16.1"
				h.restartQueued = true
				h.autoInstallProgress = progress("nudging 4 remote(s) to check for v1.16.1 now\n")
			},
			banner:    []string{"restart queued: nudging 4 remotes to update, then restarting"},
			notBanner: "ctrl+t now",
			keyQueued: true,
		},
		{
			name: "dialog open (footer feedback)",
			auto: true,
			arrange: func(h *Home) {
				h.jumpMode = true
			},
			banner:     []string{"restarting when idle"},
			keyRefused: "close the open dialog first",
		},
		{
			name: "session action running (footer feedback)",
			auto: true,
			arrange: func(h *Home) {
				h.launchingSessions = map[string]time.Time{"x": time.Now()}
			},
			banner:     []string{"restarting when idle"},
			keyRefused: "a session action is still running",
		},
		{
			name: "dialog open during the sweep: dialog wins",
			auto: true,
			arrange: func(h *Home) {
				h.autoInstallInFlight = "1.16.1"
				h.jumpMode = true
			},
			banner:     []string{"finishing the update"},
			keyRefused: "close the open dialog first",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateBannerTestHome(t, 200)
			stubUpdateSettings(t, session.UpdateSettings{AutoRestart: boolPtr(tc.auto)})
			tc.arrange(h)
			got := h.renderUpdateBannerText()
			for _, want := range tc.banner {
				if !strings.Contains(got, want) {
					t.Fatalf("banner = %q, want %q", got, want)
				}
			}
			if tc.notBanner != "" && strings.Contains(got, tc.notBanner) {
				t.Fatalf("banner = %q must not promise %q", got, tc.notBanner)
			}
			_, cmd := h.tryRestartDeck()
			switch {
			case tc.keyRestart:
				if !h.restartRequested || cmd == nil {
					t.Fatalf("key must restart now (requested=%v err=%v)", h.restartRequested, h.err)
				}
			case tc.keyQueued:
				if h.restartRequested || cmd != nil || !h.restartQueued {
					t.Fatalf("key must queue (requested=%v queued=%v)", h.restartRequested, h.restartQueued)
				}
				if h.err == nil || !strings.HasPrefix(h.err.Error(), "restart queued after ") {
					t.Fatalf("queued key needs visible feedback, footer = %v", h.err)
				}
				if h.restartQueued {
					if after := h.renderUpdateBannerText(); !strings.Contains(after, "restart queued") {
						t.Fatalf("banner after queueing = %q", after)
					}
				}
			default:
				if h.restartRequested || h.err == nil || !strings.Contains(h.err.Error(), tc.keyRefused) {
					t.Fatalf("key must be refused with %q, footer = %v", tc.keyRefused, h.err)
				}
			}
		})
	}
}

// TestUpdateRunBanner_WidthAware pins that the in-flight banner picks a
// variant that fits, at every width down to 40 columns.
func TestUpdateRunBanner_WidthAware(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	for _, w := range []int{200, 120, 80, 60, 50} {
		h := newUpdateBannerTestHome(t, w)
		h.autoInstallInFlight = "1.16.14"
		p := &update.UnattendedProgress{}
		_, _ = p.Write([]byte("nudging 4 remote(s) to check for v1.16.14 now\n"))
		h.autoInstallProgress = p
		got := h.renderUpdateBannerText()
		if lipgloss.Width(got) > w {
			t.Fatalf("width %d: banner %q is %d wide", w, got, lipgloss.Width(got))
		}
		if strings.Contains(got, "ctrl+t now") {
			t.Fatalf("width %d: banner %q promises the key works now", w, got)
		}
	}
}

// TestRestartQueued_FiresWhenRunEnds pins the queued path: the key press
// during the run, then the run ends, restarts the deck with no second press
// and even with auto_restart off.
func TestRestartQueued_FiresWhenRunEnds(t *testing.T) {
	for _, auto := range []bool{true, false} {
		h := newUpdateBannerTestHome(t, 120)
		stubUpdateSettings(t, session.UpdateSettings{AutoRestart: boolPtr(auto)})
		h.autoInstallInFlight = "1.16.1"
		assertRestartQueued(t, h, "restart queued after the update")
		// Ticks while the run is going do nothing.
		if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested {
			t.Fatalf("auto=%v: restart fired while the run is in flight", auto)
		}
		cmd := h.handleUnattendedInstallFinished(unattendedInstallFinishedMsg{version: "1.16.1", output: "ok"})
		if cmd == nil || !h.restartRequested || h.restartQueued {
			t.Fatalf("auto=%v: queued restart must fire when the run ends (requested=%v queued=%v)", auto, h.restartRequested, h.restartQueued)
		}
	}
}

// TestRestartQueued_DroppedWhenRunFindsNothing pins that a queue does not
// outlive a run that left no newer build on disk.
func TestRestartQueued_DroppedWhenRunFindsNothing(t *testing.T) {
	h := newRestartTestHome(t) // nothing installed on disk
	h.autoInstallInFlight = "1.16.1"
	h.restartQueued = true
	h.handleUnattendedInstallFinished(unattendedInstallFinishedMsg{version: "1.16.1", err: errors.New("boom")})
	if h.restartQueued || h.restartRequested {
		t.Fatalf("queued=%v requested=%v, want the queue dropped", h.restartQueued, h.restartRequested)
	}
}

// TestUnattendedProgress_ParsesRemotePhase feeds it exactly what main's
// unattended run prints (update_cli.go remoteFollowUpUnattended: the header,
// then every result line at once, including deferred and failure lines) and
// pins that the phase and total come from the header only, never from the
// indented lines.
func TestUnattendedProgress_ParsesRemotePhase(t *testing.T) {
	p := &update.UnattendedProgress{}
	if m, n := p.Phase(); m != update.PhaseNone || n != 0 {
		t.Fatalf("fresh = %v/%d", m, n)
	}
	_, _ = p.Write([]byte("✓ Updated to v1.16.1\nnudging 3 remote(s) to che"))
	if m, _ := p.Phase(); m != update.PhaseNone {
		t.Fatal("partial header line must not count")
	}
	_, _ = p.Write([]byte("ck for v1.16.1 now\n"))
	if m, n := p.Phase(); m != update.PhaseNudge || n != 3 {
		t.Fatalf("nudge header = %v/%d", m, n)
	}
	_, _ = p.Write([]byte("  a: nudged (check now)\n  b: nudge failed: x\n  ⏸ c: still deferred, this run is inside it\n"))
	if m, n := p.Phase(); m != update.PhaseNudge || n != 3 {
		t.Fatalf("result lines must not change the phase: %v/%d", m, n)
	}
	_, _ = p.Write([]byte("sweep_remotes is on: pushing v1.16.1 to 3 remote(s)\n  Platform: linux/amd64\n"))
	if m, n := p.Phase(); m != update.PhaseSweep || n != 3 {
		t.Fatalf("sweep header = %v/%d", m, n)
	}
	var nilP *update.UnattendedProgress
	if m, n := nilP.Phase(); m != update.PhaseNone || n != 0 {
		t.Fatal("nil progress must read none/0")
	}
}

// TestUnattendedProgress_BoundsLineBuffer pins that a child printing
// megabytes without a newline does not grow the buffer, and that a header
// after it is still read.
func TestUnattendedProgress_BoundsLineBuffer(t *testing.T) {
	p := &update.UnattendedProgress{}
	junk := make([]byte, 1<<20)
	for i := range junk {
		junk[i] = 'x'
	}
	_, _ = p.Write(junk)
	if got := p.BufferedLen(); got > 4096 {
		t.Fatalf("buffered %d bytes of a newline-free stream, want at most 4096", got)
	}
	_, _ = p.Write([]byte("\nnudging 2 remote(s) to check for v1 now\n"))
	if m, n := p.Phase(); m != update.PhaseNudge || n != 2 {
		t.Fatalf("header after junk = %v/%d", m, n)
	}
}
