package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestHeaderFields_DefaultMatchesHistoricalOutput pins that leaving
// [ui.header] unset renders the header byte-identical to before this config
// block existed: the version badge and the session-status stats segment are
// both present.
func TestHeaderFields_DefaultMatchesHistoricalOutput(t *testing.T) {
	home := NewHome()
	home.width = 200
	home.height = 30
	home.initialLoading = false

	snap := map[string]sessionRenderState{
		"r1": {status: session.StatusRunning},
		"w1": {status: session.StatusWaiting},
	}
	home.sessionRenderSnapshot.Store(snap)
	home.cachedStatusCounts.valid.Store(false)

	out := home.View()
	if !strings.Contains(out, "1 running") || !strings.Contains(out, "1 waiting") {
		t.Errorf("default header must keep the session-status segment: %s", out)
	}
	if !strings.Contains(out, "v"+Version) {
		t.Errorf("default header must keep the version badge: %s", out)
	}
}

// TestHeaderFields_CustomListDropsUnlistedSegments pins that
// [ui.header].fields = ["version"] hides the session-status segment (and the
// "no sessions" fallback it would otherwise render), keeping only the
// version badge — the field list governs the header the same way it governs
// the remote preview panel.
func TestHeaderFields_CustomListDropsUnlistedSegments(t *testing.T) {
	withTempAgentDeckHome(t, `
[ui.header]
fields = ["version"]
`)

	home := NewHome()
	home.width = 200
	home.height = 30
	home.initialLoading = false

	snap := map[string]sessionRenderState{
		"r1": {status: session.StatusRunning},
		"w1": {status: session.StatusWaiting},
	}
	home.sessionRenderSnapshot.Store(snap)
	home.cachedStatusCounts.valid.Store(false)

	out := home.View()
	if strings.Contains(out, "1 running") || strings.Contains(out, "1 waiting") {
		t.Errorf("fields=[version] must drop the session-status segment: %s", out)
	}
	if strings.Contains(out, "no sessions") {
		t.Errorf("fields=[version] must drop the empty-sessions fallback too: %s", out)
	}
	if !strings.Contains(out, "v"+Version) {
		t.Errorf("fields=[version] must still show the version badge: %s", out)
	}
}

// TestGetHeaderFields_UnknownEntryReportedAndDropped exercises the
// config-check path for [ui.header].fields the same way
// TestLoadUserConfig_NormalizesPreviewFieldsOnLoad does for
// [ui.remote_preview].fields: a typo'd field name loads without erroring and
// is dropped by the time UI.GetHeaderFields is consulted.
func TestGetHeaderFields_UnknownEntryReportedAndDropped(t *testing.T) {
	withTempAgentDeckHome(t, `
[ui.header]
fields = ["version", "made_up_field", "sessions_by_status"]
`)

	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	want := []string{"version", "sessions_by_status"}
	got := cfg.UI.GetHeaderFields()
	if len(got) != len(want) {
		t.Fatalf("GetHeaderFields() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GetHeaderFields() = %v, want %v", got, want)
		}
	}
}
