package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/charmbracelet/x/ansi"
)

// updateBannerGoldenWidths are the terminal widths every banner variant is
// pinned at: wide, the usual laptop, and the 80-column floor where the
// text is cut.
var updateBannerGoldenWidths = []int{200, 120, 80, 60}

// updateBannerCases are the banner variants an installed update can show,
// each built from a Home in that state.
var updateBannerCases = []struct {
	name    string
	arrange func(h *Home)
	want    string // must appear in the rendered banner at width 200
}{
	{
		name:    "pending",
		arrange: func(h *Home) {},
		want:    "⬆ v1.16.1 installed, restarting when idle (ctrl+t now)",
	},
	{
		name: "overdue",
		arrange: func(h *Home) {
			h.jumpMode = true // blocks the restart: "close the open dialog first"
			h.binaryWatch.installedSince = time.Now().Add(-3*time.Hour - time.Minute)
			_ = h.maybeAutoRestart()
		},
		want: "⚠ v1.16.1 installed 3h ago, restart overdue: close the open dialog first (ctrl+t to restart now)",
	},
	{
		name: "nudging",
		arrange: func(h *Home) {
			h.autoInstallInFlight = "1.16.1"
			h.autoInstallProgress = progressFrom("nudging 4 remote(s) to check for v1.16.1 now\n")
		},
		want: "⬆ v1.16.1 installed, nudging 4 remotes to update, then restarting (ctrl+t queues it)",
	},
	{
		name: "sweep",
		arrange: func(h *Home) {
			h.autoInstallInFlight = "1.16.1"
			h.autoInstallProgress = progressFrom("sweep_remotes is on: pushing v1.16.1 to 4 remote(s)\n")
		},
		want: "⬆ v1.16.1 installed, finishing the remote sweep (4 remotes), then restarting (ctrl+t queues it)",
	},
	{
		name: "queued",
		arrange: func(h *Home) {
			h.autoInstallInFlight = "1.16.1"
			h.restartQueued = true
			h.autoInstallProgress = progressFrom("nudging 4 remote(s) to check for v1.16.1 now\n")
		},
		want: "⬆ v1.16.1 installed, restart queued: nudging 4 remotes to update, then restarting",
	},
}

// progressFrom is an UnattendedProgress fed the given child output.
func progressFrom(out string) *update.UnattendedProgress {
	p := &update.UnattendedProgress{}
	_, _ = p.Write([]byte(out))
	return p
}

// newUpdateBannerTestHome is newAutoRestartTestHome at the given width
// with the per-package persisted state other tests may leave behind (a
// group view mode in the _test profile, the debug overlay) pinned, so the
// frame depends only on the banner state.
func newUpdateBannerTestHome(t *testing.T, width int) *Home {
	t.Helper()
	h := newAutoRestartTestHome(t)
	h.width, h.height = width, 24
	h.groupViewMode = session.GroupViewNormal
	h.debugMode = false
	return h
}

// updateBannerLine returns the banner row of a rendered frame: the row
// after the filter bar.
func updateBannerLine(t *testing.T, frame string) string {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) < 3 {
		t.Fatalf("frame too short:\n%s", frame)
	}
	return lines[2]
}

// TestUpdateBannerGolden pins the full frame of every installed-update
// banner variant at 200, 120 and 80 columns (UPDATE_GOLDEN=1 rewrites
// them). The overdue variant is new in the 2026-09-19 change and had no
// frame; its age must read "3h", not a Duration's "3h0m0s".
func TestUpdateBannerGolden(t *testing.T) {
	for _, tc := range updateBannerCases {
		for _, width := range updateBannerGoldenWidths {
			t.Run(fmt.Sprintf("%s-w%d", tc.name, width), func(t *testing.T) {
				h := newUpdateBannerTestHome(t, width)
				tc.arrange(h)
				if !h.shouldRenderUpdateBanner() {
					t.Fatal("banner must render")
				}
				got := ansi.Strip(h.View()) + "\n"
				if width == 200 && !strings.Contains(updateBannerLine(t, got), tc.want) {
					t.Fatalf("banner line %q lacks %q", updateBannerLine(t, got), tc.want)
				}
				name := fmt.Sprintf("update_banner_%s_w%d.golden", tc.name, width)
				path := filepath.Join("testdata", name)
				if os.Getenv("UPDATE_GOLDEN") == "1" {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Fatalf("%s golden mismatch\nwant:\n%s\ngot:\n%s", name, want, got)
				}
			})
		}
	}
}

// TestUpdateBannerWidthSweep renders every variant at each width from 40
// to 220 columns: the banner takes exactly one row that never exceeds the
// terminal width, and the frame keeps its height, so the list below never
// overlaps it.
func TestUpdateBannerWidthSweep(t *testing.T) {
	for _, tc := range updateBannerCases {
		t.Run(tc.name, func(t *testing.T) {
			for width := 40; width <= 220; width += 4 {
				h := newUpdateBannerTestHome(t, width)
				tc.arrange(h)
				frame := h.View()
				lines := strings.Split(frame, "\n")
				if len(lines) != h.height {
					t.Fatalf("width %d: frame has %d rows, want %d", width, len(lines), h.height)
				}
				for i, line := range lines {
					if w := ansi.StringWidth(line); w > width {
						t.Fatalf("width %d: row %d is %d columns wide:\n%s", width, i, w, ansi.Strip(line))
					}
				}
				banner := ansi.Strip(updateBannerLine(t, frame))
				if !strings.Contains(banner, "v1.16.1 installed") {
					t.Fatalf("width %d: banner row = %q", width, banner)
				}
			}
		})
	}
}
