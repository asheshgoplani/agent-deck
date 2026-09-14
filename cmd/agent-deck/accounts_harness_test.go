package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `accounts --harness codex` lists the codex_home slots, so a controller's
// Edit Session dialog on a remote row can offer the remote's Codex accounts
// the way it offers its Claude ones. The default stays Claude-only, which is
// what every existing caller (and every 1.16.x remote) expects.
func TestConfiguredAccountSlotsForHarness(t *testing.T) {
	cfg := &session.UserConfig{Profiles: map[string]session.ProfileSettings{
		"work":   {Claude: session.ProfileClaudeSettings{ConfigDir: t.TempDir()}},
		"codexy": {Codex: session.ProfileCodexSettings{ConfigDir: t.TempDir()}},
		"both":   {Claude: session.ProfileClaudeSettings{ConfigDir: t.TempDir()}, Codex: session.ProfileCodexSettings{ConfigDir: t.TempDir()}},
	}}
	names := func(entries []accountListEntry) []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Name)
		}
		return out
	}
	if got := names(configuredAccountSlotsForHarness(cfg, "claude")); len(got) != 2 || got[0] != "both" || got[1] != "work" {
		t.Fatalf("claude slots = %v", got)
	}
	if got := names(configuredAccountSlotsForHarness(cfg, "codex")); len(got) != 2 || got[0] != "both" || got[1] != "codexy" {
		t.Fatalf("codex slots = %v", got)
	}
	if got := names(configuredAccountSlots(cfg)); len(got) != 2 || got[0] != "both" || got[1] != "work" {
		t.Fatalf("default slots = %v", got)
	}
	if _, err := accountsHarnessOK("pi"); err == nil {
		t.Fatal("pi has no named account slots; --harness pi must be refused")
	}
}
