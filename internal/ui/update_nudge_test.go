package ui

// Conductor task #45 — update-available visual nudge in TUI.
//
// Motivation: 4 users today (2026-04-22) posted Feedback Hub comments from
// versions 15-39 releases old; they were hitting bugs we already fixed.
// internal/update/update.go already queries /releases/latest but the
// existing banner was not loud enough / was gated on settings many users
// never saw. The nudge fires only when ReleasesBehind > NudgeThreshold
// (i.e. 6+), is dismissible for the session with shift+u ("U"), and
// honors AGENTDECK_SKIP_UPDATE_CHECK=1 for locked-down environments.

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// TestUpdateNudge_ShowsOnlyWhenSixPlusBehind pins the threshold contract.
// The existing banner (pre-v1.7.59) fired at >=1 behind; the new nudge
// is a separate, louder signal that only fires at >5 behind.
func TestUpdateNudge_ShowsOnlyWhenSixPlusBehind(t *testing.T) {
	tests := []struct {
		name string
		info *update.UpdateInfo
		want bool
	}{
		{"nil info", nil, false},
		{"available, 1 behind", &update.UpdateInfo{Available: true, ReleasesBehind: 1}, false},
		{"available, exactly 5 behind", &update.UpdateInfo{Available: true, ReleasesBehind: 5}, false},
		{"available, 6 behind", &update.UpdateInfo{Available: true, ReleasesBehind: 6}, true},
		{"available, 40 behind", &update.UpdateInfo{Available: true, ReleasesBehind: 40}, true},
		{"not available, 99 behind", &update.UpdateInfo{Available: false, ReleasesBehind: 99}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Home{updateInfo: tt.info}
			got := h.shouldRenderUpdateNudge()
			if got != tt.want {
				t.Fatalf("shouldRenderUpdateNudge=%v, want %v (info=%+v)", got, tt.want, tt.info)
			}
		})
	}
}

// TestUpdateNudge_DismissKeySuppressesBanner pins the session-local
// dismiss: after the user hits "U", the nudge stops rendering until the
// process exits — even if a later update-check refreshes updateInfo.
func TestUpdateNudge_DismissKeySuppressesBanner(t *testing.T) {
	h := &Home{
		updateInfo: &update.UpdateInfo{
			Available:      true,
			CurrentVersion: "1.7.20",
			LatestVersion:  "1.7.58",
			ReleasesBehind: 30,
		},
	}
	if !h.shouldRenderUpdateNudge() {
		t.Fatalf("precondition: 30-behind should render, got false")
	}

	// Simulate pressing shift+U. The key handler must set the session
	// dismiss flag.
	h.handleUpdateNudgeDismiss(tea.KeyMsg{Type: tea.KeyEsc})

	if h.shouldRenderUpdateNudge() {
		t.Fatalf("after dismiss: shouldRenderUpdateNudge must be false; nudge should stay hidden for the rest of the session")
	}
	if !h.updateNudgeDismissed {
		t.Fatalf("dismiss flag not set")
	}
}

// TestUpdateNudge_EnvVarSuppressesBanner pins the kill switch: even if
// the TUI somehow receives a populated updateInfo (stale cache, manual
// injection in tests), AGENTDECK_SKIP_UPDATE_CHECK=1 keeps the nudge
// hidden.
func TestUpdateNudge_EnvVarSuppressesBanner(t *testing.T) {
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")
	h := &Home{
		updateInfo: &update.UpdateInfo{
			Available:      true,
			ReleasesBehind: 40,
		},
	}
	if h.shouldRenderUpdateNudge() {
		t.Fatalf("env-gated: shouldRenderUpdateNudge must be false when AGENTDECK_SKIP_UPDATE_CHECK=1")
	}
}

// TestUpdateNudge_BannerTextIncludesBehindCount asserts the user-visible
// string carries the "N releases behind" phrasing so it is strictly more
// informative than the legacy banner. Users reported from 15-39 versions
// old — they need to see the number to feel the urgency.
func TestUpdateNudge_BannerTextIncludesBehindCount(t *testing.T) {
	h := &Home{
		updateInfo: &update.UpdateInfo{
			Available:      true,
			CurrentVersion: "1.7.20",
			LatestVersion:  "1.7.58",
			ReleasesBehind: 30,
		},
	}
	text := h.renderUpdateNudgeText()
	wantSubstrs := []string{
		"v1.7.20",
		"v1.7.58",
		"30 releases behind",
		"agent-deck update",
	}
	for _, sub := range wantSubstrs {
		if !contains(text, sub) {
			t.Fatalf("nudge text missing %q; got %q", sub, text)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
