// Tests for Session.LaunchAs (v1.7.21 defense-in-depth).
//
// LaunchAs is the new config-driven spawn-mode selector that sits ABOVE
// LaunchInUserScope. Values: "scope" | "service" | "direct" | "auto" | "".
// Empty preserves legacy LaunchInUserScope behavior so existing users on
// v1.7.20 get zero behavior change until they opt in.
//
// These tests pin the invariants of startCommandSpec(): exact argv shape
// for each mode, case-insensitivity, whitespace-tolerance, invalid-value
// fallback, and regression guard on the legacy scope argv shape.
//
// See .planning/v1721-scope-to-service/PLAN.md for the full data-flow
// trace and scope boundaries.
package tmux

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartCommandSpec_LaunchAs_Service_UsesServiceForm pins the argv
// shape for service mode. Restart=on-failure and Type=forking are the
// two properties whose absence would silently defeat the entire feature
// (Type=simple was the pre-v1.7.21 pre-check failure mode: service marks
// itself inactive the moment tmux daemonizes, NRestarts stays 0 forever).
func TestStartCommandSpec_LaunchAs_Service_UsesServiceForm(t *testing.T) {
	s := &Session{
		Name:     "agentdeck_test-service_1234abcd",
		WorkDir:  "/tmp/project",
		LaunchAs: "service",
	}

	launcher, args := s.startCommandSpec("/tmp/project", "")
	require.Equal(t, "systemd-run", launcher, "service mode must spawn via systemd-run")

	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--user", "service invocation must be --user scoped")
	assert.Contains(t, joined, "--unit agentdeck-tmux-agentdeck-test-service-1234abcd.service",
		"service mode unit name must carry .service suffix so systemd treats it as a service not a scope")
	assert.Contains(t, joined, "--property=Type=forking",
		"Type=forking is the ONLY Type= that survives tmux daemonization — Type=simple would mark the service dead immediately")
	assert.Contains(t, joined, "--property=Restart=on-failure",
		"Restart=on-failure is the entire point of v1.7.21 — its absence silently breaks the feature")
	assert.Contains(t, joined, "--property=RestartSec=")
	assert.Contains(t, joined, "--property=KillMode=control-group",
		"KillMode=control-group ensures systemctl stop kills tmux cleanly via cgroup")

	assert.NotContains(t, joined, "--scope",
		"service mode MUST NOT include --scope (would conflict and produce an invalid unit)")
	assert.NotContains(t, joined, "--collect",
		"--collect removes the unit when inactive — breaks Restart=on-failure reattach")

	// The tmux args shape after the systemd-run prefix must match the scope
	// form's tmux args shape exactly. We use stripSystemdRunPrefix to verify.
	tmuxArgs := stripSystemdRunPrefix(args)
	assert.Equal(t, []string{"new-session", "-d", "-s", "agentdeck_test-service_1234abcd", "-c", "/tmp/project"}, tmuxArgs)
}

// TestStartCommandSpec_LaunchAs_Scope_UsesScopeForm explicitly pins the
// SCOPE-MODE argv (legacy PR #467 shape) so a refactor to service mode
// can't silently break users who set LaunchAs="scope" to keep the old
// behavior. This is the backward-compat lifeline.
func TestStartCommandSpec_LaunchAs_Scope_UsesScopeForm(t *testing.T) {
	s := &Session{
		Name:     "agentdeck_test-scope_1234abcd",
		WorkDir:  "/tmp/project",
		LaunchAs: "scope",
	}

	launcher, args := s.startCommandSpec("/tmp/project", "")
	require.Equal(t, "systemd-run", launcher)
	require.GreaterOrEqual(t, len(args), 8)
	assert.Equal(t, []string{"--user", "--scope", "--quiet", "--collect", "--unit"}, args[:5])
	assert.Equal(t, "agentdeck-tmux-agentdeck-test-scope-1234abcd", args[5])
	assert.Equal(t, "tmux", args[6])

	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "--property=Type=forking",
		"scope mode must not contain service-only properties")
	assert.NotContains(t, joined, "--property=Restart=",
		"Restart= is invalid on scopes (systemd rejects)")
}

// TestStartCommandSpec_LaunchAs_Direct_UsesDirectTmux pins that an
// explicit LaunchAs="direct" forces direct tmux EVEN IF
// LaunchInUserScope=true. This is the "opt out of isolation" path for
// users on hosts where systemd-run misbehaves.
func TestStartCommandSpec_LaunchAs_Direct_UsesDirectTmux(t *testing.T) {
	s := &Session{
		Name:              "agentdeck_test-direct_1234abcd",
		WorkDir:           "/tmp/project",
		LaunchAs:          "direct",
		LaunchInUserScope: true, // explicit override must WIN
	}

	launcher, args := s.startCommandSpec("/tmp/project", "")
	assert.Equal(t, "tmux", launcher, "LaunchAs=direct must override LaunchInUserScope=true")
	assert.Equal(t, []string{"new-session", "-d", "-s", "agentdeck_test-direct_1234abcd", "-c", "/tmp/project"}, args)
}

// TestStartCommandSpec_LaunchAs_Empty_RespectsLegacyLaunchInUserScope
// is the zero-behavior-change guarantee for v1.7.20 users who don't set
// launch_as in config.toml. Empty string = defer to the pre-existing
// LaunchInUserScope flag.
func TestStartCommandSpec_LaunchAs_Empty_RespectsLegacyLaunchInUserScope(t *testing.T) {
	t.Run("empty + LaunchInUserScope=true → scope form (legacy PR #467)", func(t *testing.T) {
		s := &Session{
			Name:              "agentdeck_test-empty-scope_1234abcd",
			WorkDir:           "/tmp/project",
			LaunchInUserScope: true,
			// LaunchAs intentionally unset
		}
		launcher, args := s.startCommandSpec("/tmp/project", "")
		assert.Equal(t, "systemd-run", launcher)
		assert.Contains(t, strings.Join(args, " "), "--scope")
	})

	t.Run("empty + LaunchInUserScope=false → direct", func(t *testing.T) {
		s := &Session{
			Name:              "agentdeck_test-empty-direct_1234abcd",
			WorkDir:           "/tmp/project",
			LaunchInUserScope: false,
		}
		launcher, _ := s.startCommandSpec("/tmp/project", "")
		assert.Equal(t, "tmux", launcher)
	})
}

// TestStartCommandSpec_LaunchAs_Invalid_FallsBackToLegacy asserts an
// unknown string does NOT panic and does NOT silently pick service — it
// falls back to the LaunchInUserScope-driven legacy behavior. A typo in
// config.toml must not put a user on an unintended spawn path.
func TestStartCommandSpec_LaunchAs_Invalid_FallsBackToLegacy(t *testing.T) {
	s := &Session{
		Name:              "agentdeck_test-invalid_1234abcd",
		WorkDir:           "/tmp/project",
		LaunchAs:          "typo-gibberish",
		LaunchInUserScope: true,
	}

	launcher, args := s.startCommandSpec("/tmp/project", "")
	assert.Equal(t, "systemd-run", launcher)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--scope", "invalid LaunchAs value must not accidentally select service mode")
	assert.NotContains(t, joined, "--property=Type=forking")
}

// TestStartCommandSpec_LaunchAs_CaseInsensitive guards against config
// typos where the user writes "Service" or "SERVICE". These must still
// resolve to the service mode, not silently fall through to legacy.
func TestStartCommandSpec_LaunchAs_CaseInsensitive(t *testing.T) {
	for _, variant := range []string{"service", "Service", "SERVICE", " service ", "service\t"} {
		t.Run(variant, func(t *testing.T) {
			s := &Session{
				Name:     "agentdeck_test-ci_1234abcd",
				WorkDir:  "/tmp/project",
				LaunchAs: variant,
			}
			launcher, args := s.startCommandSpec("/tmp/project", "")
			assert.Equal(t, "systemd-run", launcher, "case/whitespace variant %q must still resolve to service", variant)
			assert.Contains(t, strings.Join(args, " "), "--property=Type=forking")
		})
	}
}

// TestStartCommandSpec_LaunchAs_ServiceWithInitialProcess pins that the
// RunCommandAsInitialProcess path (sandbox sessions, custom claude
// commands) composes correctly with service mode. Failure here means
// sandbox sessions silently lose their auto-restart guarantee.
func TestStartCommandSpec_LaunchAs_ServiceWithInitialProcess(t *testing.T) {
	s := &Session{
		Name:                       "agentdeck_test-svcinit_1234abcd",
		WorkDir:                    "/tmp/project",
		LaunchAs:                   "service",
		RunCommandAsInitialProcess: true,
	}
	launcher, args := s.startCommandSpec("/tmp/project", "claude --resume xyz")

	require.Equal(t, "systemd-run", launcher)
	tmuxArgs := stripSystemdRunPrefix(args)
	// Last element must be the bash-wrapped command
	require.NotEmpty(t, tmuxArgs)
	last := tmuxArgs[len(tmuxArgs)-1]
	assert.True(t, strings.HasPrefix(last, "bash -c '"), "initial process must be bash-wrapped")
	assert.Contains(t, last, "claude --resume xyz")
}

// TestStripSystemdRunPrefix_RecoversTmuxArgsFromServiceForm is the
// regression guard for stripSystemdRunPrefix when it's fed SERVICE-mode
// argv (which has ~12 leading elements vs scope's 7). Adding a property
// to the service spawn must not break fallback-to-direct-tmux.
func TestStripSystemdRunPrefix_RecoversTmuxArgsFromServiceForm(t *testing.T) {
	in := []string{
		"--user", "--unit", "agentdeck-tmux-foo.service", "--quiet",
		"--property=Type=forking",
		"--property=Restart=on-failure",
		"--property=RestartSec=5s",
		"--property=StartLimitBurst=10",
		"--property=StartLimitIntervalSec=60",
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=15s",
		"tmux",
		"new-session", "-d", "-s", "name",
	}
	want := []string{"new-session", "-d", "-s", "name"}
	got := stripSystemdRunPrefix(in)
	assert.Equal(t, want, got)
}
