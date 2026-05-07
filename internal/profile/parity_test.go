package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestProfileResolution_TUIWebParity is the regression guard for issue #881.
//
// Before the fix, the TUI/CLI's profile.DetectCurrentProfile honored
// CLAUDE_CONFIG_DIR (the env var set by the common `cdw` / `cdp` shell
// aliases) while session.GetEffectiveProfile (consumed by web, storage, push,
// and costs) did not. With CLAUDE_CONFIG_DIR=~/.claude-work and
// AGENTDECK_PROFILE unset, the TUI saw profile "work" but the web saw the
// config default — so the same user on the same machine saw different
// sessions in TUI vs web.
//
// The two functions MUST resolve to the same profile for any environment a
// user can produce. If this test ever fails again, every call site (TUI,
// web /api/sessions, push subscriptions, cost dashboards) is at risk of
// drifting back into the divergence.
func TestProfileResolution_TUIWebParity(t *testing.T) {
	// Redirect HOME so config.json and profile dirs land in a temp dir.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Reproduce the exact env that triggers the bug: CLAUDE_CONFIG_DIR set
	// (as `cdw` does), AGENTDECK_PROFILE unset, no config.json default.
	os.Unsetenv("AGENTDECK_PROFILE")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmp, ".claude-work"))

	tui := DetectCurrentProfile()
	web := session.GetEffectiveProfile("")

	if tui != web {
		t.Fatalf("profile divergence between TUI and web: TUI=%q web=%q\n"+
			"This is issue #881 — same user on the same machine sees\n"+
			"different sessions in TUI vs web UI.", tui, web)
	}

	// Sanity: with CLAUDE_CONFIG_DIR=.claude-work, the resolved profile must
	// be "work" (matching what the user expects from their shell context),
	// not the config fallback "default". Without this assertion, both paths
	// could converge on "default" and silently still ignore the user's
	// CLAUDE_CONFIG_DIR.
	if tui != "work" {
		t.Fatalf("expected resolved profile to be %q (inferred from CLAUDE_CONFIG_DIR), got %q",
			"work", tui)
	}
}

// TestProfileResolution_TUIWebParity_AgentdeckProfileWins guards the priority
// order: when AGENTDECK_PROFILE is explicit, both call sites must respect it
// over CLAUDE_CONFIG_DIR. This locks in the contract so a future fix doesn't
// accidentally invert the priority and re-introduce a different divergence.
func TestProfileResolution_TUIWebParity_AgentdeckProfileWins(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	t.Setenv("AGENTDECK_PROFILE", "explicit")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmp, ".claude-work"))

	tui := DetectCurrentProfile()
	web := session.GetEffectiveProfile("")

	if tui != "explicit" || web != "explicit" {
		t.Fatalf("AGENTDECK_PROFILE must win over CLAUDE_CONFIG_DIR for both TUI and web: TUI=%q web=%q",
			tui, web)
	}
}
