package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countShimForks returns how many tmux invocations the PATH shim logged.
func countShimForks(t *testing.T, forkLog string) int {
	t.Helper()
	data, err := os.ReadFile(forkLog) //nolint:gosec
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(strings.TrimSpace(string(data))))
}

// installTmuxCountingShim prepends a PATH shim that logs one line per tmux
// invocation before exec'ing the real binary, and returns the log path.
// exec.Command("tmux", ...) resolves via PATH at call time, so the count is
// an exact fork count, not a sample.
func installTmuxCountingShim(t *testing.T, realTmux string) string {
	t.Helper()
	shimDir := t.TempDir()
	forkLog := filepath.Join(shimDir, "forks.log")
	shim := "#!/bin/sh\necho x >> \"" + forkLog + "\"\nexec \"" + realTmux + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(shim), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return forkLog
}

// Issue #1728: GetEnvironment cached only positive results, so every session
// WITHOUT the requested variable in its tmux env (the steady state for any
// session whose ID never binds via tmux env) forked `tmux show-environment`
// on every single call — at status-poll cadence, across ~60 sessions, that
// was the larger half of a sustained 150-350% CPU subprocess storm.
func TestGetEnvironment_CachesNegativeResult(t *testing.T) {
	skipIfNoTmuxBinary(t)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}

	name := fmt.Sprintf("agentdeck_envneg_%d", time.Now().UnixNano())
	if out, err := exec.Command(realTmux, "new-session", "-d", "-s", name, "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, "kill-session", "-t", name).Run() })

	forkLog := installTmuxCountingShim(t, realTmux)
	s := &Session{Name: name}

	if _, err := s.GetEnvironment("AGENTDECK_TEST_UNSET_VAR"); err == nil {
		t.Fatal("expected error for unset variable")
	}
	if got := countShimForks(t, forkLog); got != 1 {
		t.Fatalf("first miss: %d forks, want 1", got)
	}

	// Second miss inside envNegativeCacheTTL must be served from cache.
	if _, err := s.GetEnvironment("AGENTDECK_TEST_UNSET_VAR"); err == nil {
		t.Fatal("expected cached miss to still report an error")
	}
	if got := countShimForks(t, forkLog); got != 1 {
		t.Fatalf("cached miss re-forked show-environment: %d forks, want 1 (issue #1728)", got)
	}

	// An expired negative entry must re-probe (rewind its timestamp past the TTL).
	s.envCacheMu.Lock()
	entry := s.envCache["AGENTDECK_TEST_UNSET_VAR"]
	entry.time = time.Now().Add(-envNegativeCacheTTL - time.Second)
	s.envCache["AGENTDECK_TEST_UNSET_VAR"] = entry
	s.envCacheMu.Unlock()
	if _, err := s.GetEnvironment("AGENTDECK_TEST_UNSET_VAR"); err == nil {
		t.Fatal("expected error for still-unset variable")
	}
	if got := countShimForks(t, forkLog); got != 2 {
		t.Fatalf("expired negative entry: %d forks, want 2", got)
	}
}

// SetEnvironment must invalidate a cached miss immediately: the bind path
// (hook or capture-resume) writes the variable through this process, and the
// next read has to see it without waiting out envNegativeCacheTTL.
func TestGetEnvironment_SetInvalidatesNegativeEntry(t *testing.T) {
	skipIfNoTmuxBinary(t)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}

	name := fmt.Sprintf("agentdeck_envset_%d", time.Now().UnixNano())
	if out, err := exec.Command(realTmux, "new-session", "-d", "-s", name, "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, "kill-session", "-t", name).Run() })

	s := &Session{Name: name}
	if _, err := s.GetEnvironment("AGENTDECK_TEST_BIND_VAR"); err == nil {
		t.Fatal("expected miss before set")
	}
	if err := s.SetEnvironment("AGENTDECK_TEST_BIND_VAR", "bound-id"); err != nil {
		t.Fatalf("set-environment: %v", err)
	}
	got, err := s.GetEnvironment("AGENTDECK_TEST_BIND_VAR")
	if err != nil {
		t.Fatalf("read after set still served the stale cached miss: %v", err)
	}
	if got != "bound-id" {
		t.Fatalf("got %q, want %q", got, "bound-id")
	}
}
