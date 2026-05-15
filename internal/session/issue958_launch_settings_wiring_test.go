// Regression pin for issue #958 — SSH-logout session loss.
//
// Issue 958 root cause was a too-strict default: launch_in_user_scope=false
// on a fresh Linux+systemd install spawned tmux into the SSH login session
// scope, so `ssh exit` tore down every managed tmux server with the scope.
// The default flip landed in commit 61a2f866 (2026-04-14) by migrating the
// field to *bool with isSystemdUserScopeAvailable() as the implicit default.
//
// The flip alone is not enough. Three call sites in instance.go must each
// copy GetTmuxSettings().GetLaunchInUserScope() / GetLaunchAs() onto
// tmuxSession before tmuxSession.Start() is invoked. The duplication of two
// nearly-identical lines across three Start() sites is fragile: dropping
// any one of them silently regresses the bug at that path while existing
// persistence tests (which exercise GetLaunchInUserScope only, or bypass
// startCommandSpec via helpers) continue to pass.
//
// These tests pin the single-source-of-truth helper
// (*Instance).applyLaunchSettingsFromConfig — they fail to compile if the
// helper is removed, and fail at runtime if the helper stops reading both
// settings.
package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstance_ApplyLaunchSettingsFromConfig_DefaultLinux_PinsScope pins
// the production-default wiring for #958: on a Linux+systemd host with
// no config.toml overrides, applyLaunchSettingsFromConfig MUST copy
// LaunchInUserScope=true onto the tmux session so the spawn argv is
// `systemd-run --user --scope … tmux …`, not bare tmux. Skip cleanly on
// hosts without systemd-run so macOS / minimal-container CI passes.
func TestInstance_ApplyLaunchSettingsFromConfig_DefaultLinux_PinsScope(t *testing.T) {
	requireSystemdRun(t)
	home := isolatedHomeDir(t)
	cfg := filepath.Join(home, ".agent-deck", "config.toml")
	if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstance("issue958-default", "/tmp")
	inst.applyLaunchSettingsFromConfig()

	if !inst.tmuxSession.LaunchInUserScope {
		t.Fatalf("#958 regression: applyLaunchSettingsFromConfig did not wire LaunchInUserScope=true on default Linux+systemd config; spawn would inherit login-session cgroup and die on SSH exit")
	}
	if inst.tmuxSession.LaunchAs != "" {
		t.Fatalf("LaunchAs: default (no override) must be empty so resolveLaunchMode defers to LaunchInUserScope; got %q", inst.tmuxSession.LaunchAs)
	}
}

// TestInstance_ApplyLaunchSettingsFromConfig_ExplicitLaunchAs_ServicePropagates
// pins that an explicit `launch_as = "service"` in config.toml reaches the
// tmux session field — covers the defense-in-depth path where users opt
// into systemd-managed restart on tmux OOM.
func TestInstance_ApplyLaunchSettingsFromConfig_ExplicitLaunchAs_ServicePropagates(t *testing.T) {
	home := isolatedHomeDir(t)
	cfg := filepath.Join(home, ".agent-deck", "config.toml")
	if err := os.WriteFile(cfg, []byte("[tmux]\nlaunch_as = \"service\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstance("issue958-service", "/tmp")
	inst.applyLaunchSettingsFromConfig()

	if got := inst.tmuxSession.LaunchAs; got != "service" {
		t.Fatalf("LaunchAs: want %q, got %q", "service", got)
	}
}

// TestInstance_ApplyLaunchSettingsFromConfig_ExplicitOptOut_HonoredOnLinux
// pins the explicit `launch_in_user_scope = false` opt-out path: even on
// Linux+systemd, a user-set false MUST disable the systemd-run wrap so
// the host-tunings escape hatch in CLAUDE.md keeps working.
func TestInstance_ApplyLaunchSettingsFromConfig_ExplicitOptOut_HonoredOnLinux(t *testing.T) {
	requireSystemdRun(t)
	home := isolatedHomeDir(t)
	cfg := filepath.Join(home, ".agent-deck", "config.toml")
	if err := os.WriteFile(cfg, []byte("[tmux]\nlaunch_in_user_scope = false\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstance("issue958-optout", "/tmp")
	inst.applyLaunchSettingsFromConfig()

	if inst.tmuxSession.LaunchInUserScope {
		t.Fatalf("explicit launch_in_user_scope=false must be honored, got true")
	}
}
