// Issue #1092 — Configurable Sessions/Preview split ratio.
//
// Reporter: @ddorman-dn. Previously hardcoded to 35% sessions / 65%
// preview. Users want to either bias the split toward preview
// permanently (e.g. 20/80) or adjust it live without restart.
//
// Two delivery surfaces:
//   1. config.toml [ui] preview_pct (10-90)
//   2. Runtime keybindings: < shrinks preview by 5%, > grows by 5%
//
// Bounds [10, 90] keep both panes usable. Adjustments persist back to
// config.toml so they survive restart.

package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setIsolatedAgentDeckDir gives each test its own ~/.agent-deck so
// SaveUserConfig / LoadUserConfig don't clobber the user's real config
// or interfere with each other.
//
// session.GetAgentDeckDir() resolves via os.UserHomeDir(), which honors
// HOME on POSIX, so we point HOME at a temp dir and create
// .agent-deck/ inside it.
func setIsolatedAgentDeckDir(t *testing.T) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	dir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	// LoadUserConfig caches by mtime; reset between tests.
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return dir
}

func TestIssue1092_DefaultSplit_Is65PreviewPct(t *testing.T) {
	setIsolatedAgentDeckDir(t)

	home := NewHome()
	home.width = 100

	got := home.getPreviewPct()
	if got != session.DefaultPreviewPct {
		t.Fatalf("default preview_pct = %d, want %d (preserve historical 35/65 split)",
			got, session.DefaultPreviewPct)
	}

	// sessionsPaneWidth should be 35% of 100 = 35.
	if w := home.sessionsPaneWidth(); w != 35 {
		t.Fatalf("sessionsPaneWidth at width=100, preview_pct=65 = %d, want 35", w)
	}
}

func TestIssue1092_ConfigOverride_PicksUpCustomValue(t *testing.T) {
	dir := setIsolatedAgentDeckDir(t)

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[ui]\npreview_pct = 80\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	session.ClearUserConfigCache()

	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got := cfg.UI.GetPreviewPct(); got != 80 {
		t.Fatalf("cfg.UI.GetPreviewPct() = %d, want 80 — config not parsed", got)
	}

	home := NewHome()
	home.width = 100
	if got := home.getPreviewPct(); got != 80 {
		t.Fatalf("home.previewPct after init = %d, want 80 — config not threaded into Home",
			got)
	}
	// 20% of 100 = 20.
	if w := home.sessionsPaneWidth(); w != 20 {
		t.Fatalf("sessionsPaneWidth at preview_pct=80 = %d, want 20", w)
	}
}

func TestIssue1092_KeybindingShiftsBy5Pct(t *testing.T) {
	setIsolatedAgentDeckDir(t)

	home := NewHome()
	home.previewPct = 60 // start from a clean baseline

	if !home.adjustPreviewPct(previewPctStep) {
		t.Fatalf("expected adjustPreviewPct(+5) to report a change from 60")
	}
	if home.getPreviewPct() != 65 {
		t.Fatalf("after +5: previewPct = %d, want 65", home.getPreviewPct())
	}

	if !home.adjustPreviewPct(-previewPctStep) {
		t.Fatalf("expected adjustPreviewPct(-5) to report a change from 65")
	}
	if home.getPreviewPct() != 60 {
		t.Fatalf("after -5: previewPct = %d, want 60", home.getPreviewPct())
	}
}

func TestIssue1092_KeybindingClampsToBounds(t *testing.T) {
	setIsolatedAgentDeckDir(t)

	home := NewHome()

	// Drive the percentage down past the floor.
	home.previewPct = 12
	home.adjustPreviewPct(-previewPctStep) // 12 -> 10 (clamped, was 7)
	if got := home.getPreviewPct(); got != session.MinPreviewPct {
		t.Fatalf("after -5 from 12: previewPct = %d, want %d (lower bound)",
			got, session.MinPreviewPct)
	}
	// Already at the floor — another nudge should be a no-op for value
	// (but still triggers overlay) and should NOT underflow.
	if home.adjustPreviewPct(-previewPctStep) {
		t.Fatalf("expected adjustPreviewPct at floor to return false")
	}
	if got := home.getPreviewPct(); got != session.MinPreviewPct {
		t.Fatalf("after second -5 at floor: previewPct = %d, want %d",
			got, session.MinPreviewPct)
	}

	// Drive the percentage up past the ceiling.
	home.previewPct = 88
	home.adjustPreviewPct(previewPctStep) // 88 -> 90 (clamped, was 93)
	if got := home.getPreviewPct(); got != session.MaxPreviewPct {
		t.Fatalf("after +5 from 88: previewPct = %d, want %d (upper bound)",
			got, session.MaxPreviewPct)
	}
	if home.adjustPreviewPct(previewPctStep) {
		t.Fatalf("expected adjustPreviewPct at ceiling to return false")
	}
	if got := home.getPreviewPct(); got != session.MaxPreviewPct {
		t.Fatalf("after second +5 at ceiling: previewPct = %d, want %d",
			got, session.MaxPreviewPct)
	}
}

func TestIssue1092_AdjustmentPersistsToConfig(t *testing.T) {
	dir := setIsolatedAgentDeckDir(t)

	home := NewHome()
	home.previewPct = 60
	home.adjustPreviewPct(previewPctStep) // -> 65

	// File should now contain preview_pct = 65.
	cfgPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected config.toml to be written after adjust: %v", err)
	}
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig after adjust: %v", err)
	}
	if got := cfg.UI.GetPreviewPct(); got != 65 {
		t.Fatalf("persisted preview_pct = %d, want 65 — adjustment did not save",
			got)
	}
}

func TestIssue1092_GetPreviewPct_ClampsLegacyValues(t *testing.T) {
	// A user (or a stale Home struct built before this feature) might have
	// previewPct of 0 or out-of-range. getPreviewPct must always return a
	// usable value so layout math never produces a 0-width pane.
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero falls back to default", 0, session.DefaultPreviewPct},
		{"negative falls back to default", -5, session.DefaultPreviewPct},
		{"below min clamps up", 3, session.MinPreviewPct},
		{"above max clamps down", 99, session.MaxPreviewPct},
		{"in-range passes through", 42, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Home{previewPct: tc.in}
			if got := h.getPreviewPct(); got != tc.want {
				t.Fatalf("getPreviewPct(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
