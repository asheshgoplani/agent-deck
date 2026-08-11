package ui

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// uiIsolationReportMarker gates the subprocess entrypoint below.
const uiIsolationReportMarker = "AGENT_DECK_UI_ISOLATION_REPORT_HELPER"

// TestStaleMarkerCannotDisableIsolation guards the lever this package's TestMain
// gained when it learned to skip isolation for re-exec'd helper subprocesses.
//
// The skip exists because TestOrphanWatchdog_ForceExitsHungProcess re-execs the
// test binary and the child dies by os.Exit(2) — the behaviour under test —
// which skips every defer and stranded one ad-home-* dir per run. But the same
// check, if it trusted a bare env var, would let a stale exported marker point
// this package at the REAL ~/.agent-deck. This package was the concrete trigger
// of the 2026-06-04 data-loss incident: an un-sandboxed `go test ./internal/ui/...`
// wiped the live profile index and config.
//
// So: assert both markers with nothing behind them and require isolation anyway.
func TestStaleMarkerCannotDisableIsolation(t *testing.T) {
	// os.UserHomeDir just reads $HOME, which TestMain has already replaced with
	// its sandbox. The passwd database is the independent source of the real
	// home, and the one testutil.HomeAlreadyIsolated corroborates against.
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skipf("cannot resolve the real home from the passwd database: %v", err)
	}

	env := os.Environ()
	for _, drop := range []string{"HOME", "TMUX_TMPDIR", "TMUX", "TMUX_PANE"} {
		env = dropUIEnv(env, drop)
	}
	env = append(env,
		uiIsolationReportMarker+"=1",
		// The lie: both markers claim a sandbox that does not exist.
		"AGENT_DECK_TEST_HOME_ISOLATED=1",
		"AGENT_DECK_TEST_ISOLATED=1",
		"HOME="+u.HomeDir,
		// Keep the watchdog from firing inside the short-lived reporter.
		"AGENTDECK_TEST_HARD_TIMEOUT=120s",
	)

	cmd := exec.Command(os.Args[0], "-test.run=^TestUIIsolationReportProcess$")
	cmd.Env = env

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper process failed: %v\nstdout=%q\nstderr=%q", err, out.String(), errBuf.String())
	}

	childHome := extractUIReported(t, out.String(), "HOME")
	childTmuxTmpdir := extractUIReported(t, out.String(), "TMUX_TMPDIR")

	// The child is made to isolate and then os.Exit past its own cleanup, so
	// the dirs it created are stranded. The parent caused that and is the only
	// thing that knows the paths, so the parent owns them. Registered as a
	// cleanup so it still fires if an assertion below fails.
	t.Cleanup(func() {
		for _, dir := range []string{childHome, childTmuxTmpdir} {
			if dir == "" || dir == u.HomeDir {
				continue
			}
			base := filepath.Base(filepath.Clean(dir))
			if !strings.HasPrefix(base, "ad-home-") && !strings.HasPrefix(base, "ad-tmux-") {
				continue // never remove something outside the owned prefixes
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Logf("could not reap child isolation dir %s: %v", dir, err)
			}
		}
	})

	if childHome == u.HomeDir {
		t.Errorf("a stale AGENT_DECK_TEST_HOME_ISOLATED made TestMain skip HOME isolation; "+
			"this package then resolves the profile index and config under the REAL "+
			"home (%s) and can wipe them — the 2026-06-04 data-loss incident.", u.HomeDir)
	}
	if childTmuxTmpdir == "" || isDefaultTmuxBaseForUITest(childTmuxTmpdir) {
		t.Errorf("a stale AGENT_DECK_TEST_ISOLATED made TestMain skip tmux isolation "+
			"(child TMUX_TMPDIR=%q); UI session-lifecycle tests would spawn tmux on the "+
			"user's DEFAULT socket — the 2026-04-17 incident.", childTmuxTmpdir)
	}
}

// TestUIIsolationReportProcess is not a real test. The test above invokes it via
// -test.run as a subprocess entrypoint that prints the isolation env its
// TestMain ended up with. It is a no-op unless the marker is set.
func TestUIIsolationReportProcess(t *testing.T) {
	if os.Getenv(uiIsolationReportMarker) != "1" {
		return
	}
	os.Stdout.WriteString("REPORT HOME=" + os.Getenv("HOME") + "\n")
	os.Stdout.WriteString("REPORT TMUX_TMPDIR=" + os.Getenv("TMUX_TMPDIR") + "\n")
	os.Exit(0)
}

// isDefaultTmuxBaseForUITest mirrors testutil's unexported isDefaultTmuxBase.
func isDefaultTmuxBaseForUITest(dir string) bool {
	clean := filepath.Clean(dir)
	for _, base := range []string{"/tmp", "/private/tmp", filepath.Clean(os.TempDir())} {
		if clean == base {
			return true
		}
	}
	return strings.HasPrefix(filepath.Base(clean), "tmux-")
}

func dropUIEnv(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func extractUIReported(t *testing.T, out, key string) string {
	t.Helper()
	prefix := "REPORT " + key + "="
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("helper did not report %s; output was:\n%s", key, out)
	return ""
}
