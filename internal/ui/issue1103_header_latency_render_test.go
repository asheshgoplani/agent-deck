// Issue #1103 — Remote session latency markers (render layer).
//
// Reporter: @ddorman-dn — `remotes/<name> — <Xms>` in TUI header with color
// thresholds (green <50, yellow 50-200, red >200). This file pins the
// render-side invariants:
//
//   - renderRemoteGroupItem appends ` — Xms` after the count for connected
//     remotes that have been measured.
//   - Color matches the threshold band.
//   - An unmeasured remote (zero-valued RemoteLatency) renders NO marker,
//     so the header doesn't jitter on first paint.
//   - Offline (measurement failed) renders ` — offline` in red.

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

func TestIssue1103_Header_ShowsLatencyMs_ForConnectedRemote(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30
	home.remoteLatency["dev"] = session.RemoteLatency{
		MS:         47,
		MeasuredAt: time.Now(),
	}

	item := session.Item{
		Type:       session.ItemTypeRemoteGroup,
		RemoteName: "dev",
	}
	var b strings.Builder
	home.renderRemoteGroupItem(&b, item, false, 80)
	rendered := b.String()

	if !strings.Contains(stripANSILatency(rendered), "remotes/dev") {
		t.Fatalf("header missing `remotes/dev`: %q", rendered)
	}
	if !strings.Contains(stripANSILatency(rendered), "— 47ms") {
		t.Fatalf("header missing ` — 47ms` marker per #1103: %q", stripANSILatency(rendered))
	}
}

func TestIssue1103_Header_ColorByThreshold(t *testing.T) {
	forceTrueColorProfile()

	cases := []struct {
		name      string
		ms        int
		wantColor string // lipgloss.Color value used in render
	}{
		{"green under 50ms", 12, "2"},
		{"green just under 50ms", 49, "2"},
		{"yellow at 50ms", 50, "3"},
		{"yellow at 200ms", 200, "3"},
		{"red over 200ms", 201, "1"},
		{"red way over", 800, "1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := NewHome()
			home.width = 100
			home.height = 30
			home.remoteLatency["dev"] = session.RemoteLatency{
				MS:         tc.ms,
				MeasuredAt: time.Now(),
			}
			item := session.Item{
				Type:       session.ItemTypeRemoteGroup,
				RemoteName: "dev",
			}
			var b strings.Builder
			home.renderRemoteGroupItem(&b, item, false, 80)
			got := b.String()

			// Build the expected styled fragment with the same lipgloss style
			// the renderer uses, so the assertion is robust to terminfo.
			wantText := " — " + itoa(tc.ms) + "ms"
			wantFragment := lipgloss.NewStyle().
				Foreground(lipgloss.Color(tc.wantColor)).
				Render(wantText)

			if !strings.Contains(got, wantFragment) {
				t.Fatalf("ms=%d must render colored fragment %q; got %q", tc.ms, wantFragment, got)
			}
		})
	}
}

func TestIssue1103_Header_OfflineRendersOfflineMarker(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30
	home.remoteLatency["dev"] = session.RemoteLatency{
		Offline:    true,
		MeasuredAt: time.Now(),
	}
	item := session.Item{
		Type:       session.ItemTypeRemoteGroup,
		RemoteName: "dev",
	}
	var b strings.Builder
	home.renderRemoteGroupItem(&b, item, false, 80)
	rendered := stripANSILatency(b.String())

	if !strings.Contains(rendered, "— offline") {
		t.Fatalf("disconnected remote must show ` — offline` per #1103; got %q", rendered)
	}
	// And critically: never report 0ms for a disconnected remote, which
	// would falsely indicate a healthy remote.
	if strings.Contains(rendered, "0ms") {
		t.Fatalf("offline remote rendered as 0ms — wrong: %q", rendered)
	}
}

func TestIssue1103_Header_NeverMeasured_SuppressesMarker(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30
	// Intentionally do NOT populate remoteLatency["dev"]: simulates a
	// fresh start before the first tick has run.

	item := session.Item{
		Type:       session.ItemTypeRemoteGroup,
		RemoteName: "dev",
	}
	var b strings.Builder
	home.renderRemoteGroupItem(&b, item, false, 80)
	rendered := stripANSILatency(b.String())

	if strings.Contains(rendered, " — ") {
		t.Fatalf("unmeasured remote must NOT render a latency marker (avoids first-paint jitter); got %q", rendered)
	}
	if !strings.Contains(rendered, "remotes/dev") {
		t.Fatalf("header still must contain `remotes/dev`; got %q", rendered)
	}
}

// itoa is a tiny local helper so the assertion doesn't depend on strconv
// or fmt format directives (keeps the diff readable).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// stripANSILatency removes ANSI escape sequences so substring assertions on the
// human-visible text are robust to color codes.
func stripANSILatency(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip through the terminating letter
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				i = j
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
