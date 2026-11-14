package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #2948: pi finished its turn and sat idle at its composer, but a
// "── ⠴ Working ──" banner the redraw missed stayed in the content region of
// the pane. The spinner scan covered the whole frame, so the session read
// running for as long as the residue stayed on screen. The issue quotes the
// banner line and the pi footer shape; the frame is assembled from those
// with neutral text.
func issue2948Pane(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "pane_status", "pi-issue2948-stale-working-banner.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var (
	issue2948Rule   = strings.Repeat("─", 72)
	issue2948Banner = "── ⠴ Working " + strings.Repeat("─", 60)
	issue2948Footer = "~/work/proj (main)\n↑48k ↓1.9k R310k CH97.1% $0.212 (sub) 6.4%/272k (auto)"
)

func TestIssue2948_StaleBannerAboveIdleComposerIsWaiting(t *testing.T) {
	content := issue2948Pane(t)
	if got := ClassifyPaneFrame("pi", content); got != FrameWaiting {
		t.Fatalf("ClassifyPaneFrame = %s, want %s (stale Working banner above an idle composer)", got, FrameWaiting)
	}
	s := &Session{DisplayName: "issue2948-stale", detectedTool: "pi"}
	if s.hasBusyIndicator(content) {
		t.Fatal("stale Working banner in the content region read as busy")
	}
}

// pi draws its live banner as the top border of the composer box; it must
// still count wherever the rest of the frame puts it.
func TestIssue2948_LiveBannerStillBusy(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"live banner on composer with empty draft",
			" answer so far\n\n" + issue2948Banner + "\n\n" + issue2948Rule + "\n" + issue2948Footer},
		{"live banner on composer with multi-line draft",
			" answer so far\n\n" + issue2948Banner + "\nfirst draft line\nsecond draft line\nthird draft line\n" + issue2948Rule + "\n" + issue2948Footer},
		{"live banner with widget under composer",
			" tool output\n" + issue2948Banner + "\n\n" + issue2948Rule + "\n  1 active agent · Async runs 0/∞\n" + issue2948Footer},
		{"live banner below a stale one",
			issue2948Banner + "\n\n old answer\n\n" + issue2948Rule + "\n\n" + issue2948Rule + "\n\n new turn output\n" + issue2948Banner + "\n\n" + issue2948Rule + "\n" + issue2948Footer},
		{"banner with no composer box in frame",
			issue2948Banner + "\n" + strings.Repeat("output line\n", 30) + "footer row"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPaneFrame("pi", tc.content); got != FrameActive {
				t.Fatalf("ClassifyPaneFrame = %s, want %s\n%s", got, FrameActive, tc.content)
			}
		})
	}
}

// The shared spinner scan still covers the whole frame; only pi narrows it.
func TestIssue2948_SharedSpinnerScanUnchanged(t *testing.T) {
	if _, _, found := findSpinnerInContent(issue2948Pane(t), defaultSpinnerChars()); !found {
		t.Fatal("shared whole-frame spinner scan changed")
	}
}

// End to end on a real pane: an idle pi pane carrying the stale banner reads
// waiting through GetStatus, not running.
func TestIssue2948_GetStatusStaleBannerPaneIsWaiting(t *testing.T) {
	skipIfNoTmuxBinary(t)
	s := startPaneWithContent(t, "issue2948-getstatus", issue2948Pane(t), "pi --no-skills", "(auto)")
	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.lastStableStatus = "starting"
	s.mu.Unlock()
	got, err := s.GetStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got != "waiting" {
		t.Fatalf("status=%q, want waiting", got)
	}
}
