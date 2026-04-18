// Tests for the three-tier fallback chain on service-mode spawn
// (v1.7.21). Service systemd-run fails → scope systemd-run attempted
// → direct tmux attempted. Any tier succeeding short-circuits; all
// tiers failing wraps ALL THREE diagnostics so operators can triage.
//
// Uses the same execCommand swappable-seam + failOnLauncher helpers
// as tmux_fallback_test.go so failures are injected at the process
// boundary without touching host PATH or systemd state.
package tmux

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStart_Service_SuccessPath: service spawn works first time, no
// scope/direct retries attempted. Negative guard: execCommand counter
// asserts exactly one systemd-run invocation.
func TestStart_Service_SuccessPath(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("no tmux binary available: %v", err)
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skipf("no systemd-run available: %v", err)
	}

	original := execCommand
	var calls []string
	execCommand = func(name string, arg ...string) *exec.Cmd {
		calls = append(calls, name)
		return exec.Command(name, arg...)
	}
	t.Cleanup(func() { execCommand = original })

	displayName := "test-svcok-" + randomServerSuffix(t)
	s := NewSession(displayName, "/tmp")
	s.LaunchAs = "service"
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", s.Name).Run()
		// Also best-effort stop the service unit in case
		unitName := "agentdeck-tmux-" + sanitizeSystemdUnitComponent(s.Name) + ".service"
		_ = exec.Command("systemctl", "--user", "stop", unitName).Run()
		_ = exec.Command("systemctl", "--user", "reset-failed", unitName).Run()
	})

	if err := s.Start(""); err != nil {
		t.Fatalf("service-mode spawn failed unexpectedly: %v", err)
	}

	// Successful service spawn must invoke systemd-run exactly once on success
	// (tmux binary probes from inside tmux itself don't go through execCommand).
	var systemdRunCalls int
	for _, c := range calls {
		if c == "systemd-run" {
			systemdRunCalls++
		}
	}
	assert.Equal(t, 1, systemdRunCalls, "service success path must NOT retry; got %d systemd-run invocations", systemdRunCalls)
}

// TestStart_Service_WhenServiceFails_FallbackToScope: first systemd-run
// (service) returns non-zero; fallback tries systemd-run (scope);
// fallback succeeds; session ready.
//
// Simulation: inject a mock that returns exit-1 ONLY when the argv
// contains "--unit *.service" (i.e. service form), pass-through for the
// scope form and direct tmux.
func TestStart_Service_WhenServiceFails_FallbackToScope(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("no tmux binary available: %v", err)
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skipf("no systemd-run available: %v", err)
	}

	original := execCommand
	execCommand = func(name string, arg ...string) *exec.Cmd {
		if name == "systemd-run" && containsServiceUnitFlag(arg) {
			return exec.Command("false")
		}
		return exec.Command(name, arg...)
	}
	t.Cleanup(func() { execCommand = original })

	buf := captureStatusLog(t)

	displayName := "test-svcfallscope-" + randomServerSuffix(t)
	s := NewSession(displayName, "/tmp")
	s.LaunchAs = "service"
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", s.Name).Run() })

	if err := s.Start(""); err != nil {
		t.Fatalf("expected fallback from service → scope to recover, got error: %v\nlog: %s", err, buf.String())
	}

	logs := buf.String()
	assert.Contains(t, logs, "tmux_systemd_run_fallback",
		"service→scope fallback must emit a structured log for observability")
}

// TestStart_Service_WhenAllSystemdFail_FallbackToDirect: service AND
// scope both fail; direct tmux succeeds; session ready.
func TestStart_Service_WhenAllSystemdFail_FallbackToDirect(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("no tmux binary available: %v", err)
	}

	original := execCommand
	execCommand = func(name string, arg ...string) *exec.Cmd {
		if name == "systemd-run" {
			return exec.Command("false") // fail on BOTH service AND scope
		}
		return exec.Command(name, arg...)
	}
	t.Cleanup(func() { execCommand = original })

	displayName := "test-svcfallall-" + randomServerSuffix(t)
	s := NewSession(displayName, "/tmp")
	s.LaunchAs = "service"
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", s.Name).Run() })

	if err := s.Start(""); err != nil {
		t.Fatalf("expected fallback service→scope→direct to recover, got: %v", err)
	}
}

// TestStart_Service_AllThreePathsFail_ErrorIncludesAllThree: when
// every tier fails, the returned error must contain all three
// diagnostics so operators can triage in one log grep.
func TestStart_Service_AllThreePathsFail_ErrorIncludesAllThree(t *testing.T) {
	original := execCommand
	execCommand = func(name string, arg ...string) *exec.Cmd {
		if name == "systemd-run" || name == "tmux" {
			return exec.Command("false")
		}
		return exec.Command(name, arg...)
	}
	t.Cleanup(func() { execCommand = original })

	displayName := "test-svcallfail-" + randomServerSuffix(t)
	s := NewSession(displayName, "/tmp")
	s.LaunchAs = "service"
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", s.Name).Run() })

	err := s.Start("")
	require.Error(t, err, "all three paths fail — Start must surface an error")
	msg := err.Error()
	assert.Contains(t, msg, "service path:",
		"operators must see the service-tier failure to diagnose dbus/manager issues")
	assert.Contains(t, msg, "scope path:",
		"operators must see the scope-tier failure to diagnose permission/linger issues")
	assert.Contains(t, msg, "direct retry:",
		"operators must see the direct-tmux failure to diagnose host-tmux issues")
}

// containsServiceUnitFlag scans argv for a "--unit X.service" pair. Used
// by the mock above to distinguish service-form systemd-run from
// scope-form at inject time.
func containsServiceUnitFlag(args []string) bool {
	for i, a := range args {
		if a == "--unit" && i+1 < len(args) && strings.HasSuffix(args[i+1], ".service") {
			return true
		}
	}
	return false
}
