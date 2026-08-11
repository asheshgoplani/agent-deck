package main

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// helperIsolationReportMarker gates the subprocess entrypoint below and is also
// what makes the child look like a helper to runTestMain.
const helperIsolationReportMarker = "AGENT_DECK_ISOLATION_REPORT_HELPER"

// TestHelperSubprocessDoesNotReIsolate is the regression for the second half of
// the 2026-08-10 temp-leak incident.
//
// Several tests in this package exercise production exit paths by re-execing
// the test binary (`exec.Command(os.Args[0], "-test.run=TestSomeHelperProcess")`)
// and letting the child os.Exit. os.Exit skips every deferred cleanup, so any
// temp dir the CHILD's TestMain created is stranded — permanently, on every
// invocation. runTestMain used to recognise exactly one helper by name
// (Task6's), so the cursor-hooks helper silently isolated and leaked one temp
// HOME plus one ad-tmux-* socket dir per call. 644 ad-tmux-* dirs had piled up
// under /tmp by the time this was found, 592 of them empty.
//
// The fix recognises a helper by its INHERITED sandbox rather than by name, so
// helpers added later are covered without leaking first. This test pins that:
// a child spawned with the parent's environment must report the parent's own
// HOME and TMUX_TMPDIR, proving it created no temp dirs of its own.
func TestHelperSubprocessDoesNotReIsolate(t *testing.T) {
	// The premise: this test binary is itself isolated, so the child inherits
	// markers that prove its sandbox. Without them the child SHOULD isolate,
	// and asserting otherwise would be asserting a data-safety hole.
	if !testutil.HomeAlreadyIsolated() {
		t.Fatal("package TestMain did not isolate HOME; the inheritance this test asserts cannot hold")
	}

	parentHome := os.Getenv("HOME")
	parentTmuxTmpdir := os.Getenv("TMUX_TMPDIR")

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperIsolationReportProcess")
	cmd.Env = append(os.Environ(), helperIsolationReportMarker+"=1")

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper process failed: %v\nstdout=%q\nstderr=%q", err, out.String(), errBuf.String())
	}

	childHome := extractReported(t, out.String(), "HOME")
	childTmuxTmpdir := extractReported(t, out.String(), "TMUX_TMPDIR")

	if childHome != parentHome {
		t.Errorf("helper subprocess re-isolated HOME.\n  parent: %s\n  child:  %s\n\n"+
			"The child os.Exits past every defer, so its temp HOME is stranded on every "+
			"invocation. runTestMain must treat an inherited sandbox as proof the process "+
			"is a helper and skip isolation. See testutil.HomeAlreadyIsolated.",
			parentHome, childHome)
	}
	if childTmuxTmpdir != parentTmuxTmpdir {
		t.Errorf("helper subprocess re-isolated TMUX_TMPDIR.\n  parent: %s\n  child:  %s\n\n"+
			"Same leak, different directory: a stranded ad-tmux-* dir under /tmp per "+
			"invocation. See testutil.TmuxAlreadyIsolated.",
			parentTmuxTmpdir, childTmuxTmpdir)
	}
}

// TestStaleMarkerCannotDisableIsolation is the data-safety counterpart to the
// test above, and the one that matters more.
//
// runTestMain skips isolation when it sees an inherited sandbox. That check is
// a lever with a blast radius: convince it wrongly and this package runs
// against the REAL home and the user's DEFAULT tmux socket — and then
// cleanupTestSessions issues `tmux list-sessions` + `kill-session` there. That
// is the 2026-04-17 incident, in which `go test ./...` killed every session in
// the user's live profile.
//
// Isolation markers are ordinary env vars, so they survive in any inherited
// environment: exported by hand, left over from a parent agent-deck session, or
// captured before a cleanup ran. This spawns the test binary with BOTH markers
// asserted but nothing behind them — the real HOME, no TMUX_TMPDIR — and
// requires the child to isolate anyway.
func TestStaleMarkerCannotDisableIsolation(t *testing.T) {
	// os.UserHomeDir just reads $HOME, which this package has already replaced
	// with its sandbox — asking it for "the real home" returns the sandbox and
	// the test asserts nothing. The passwd database is the independent source,
	// and is the same one HomeAlreadyIsolated corroborates against.
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skipf("cannot resolve the real home from the passwd database: %v", err)
	}
	realHome := u.HomeDir

	env := os.Environ()
	for _, drop := range []string{"HOME", "TMUX_TMPDIR", "TMUX", "TMUX_PANE", "AGENT_DECK_TASK6_HELPER_PROCESS"} {
		env = dropEnv(env, drop)
	}
	env = append(env,
		helperIsolationReportMarker+"=1",
		// The lie: both markers claim a sandbox that does not exist.
		"AGENT_DECK_TEST_HOME_ISOLATED=1",
		"AGENT_DECK_TEST_ISOLATED=1",
		"HOME="+realHome,
	)

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperIsolationReportProcess")
	cmd.Env = env

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper process failed: %v\nstdout=%q\nstderr=%q", err, out.String(), errBuf.String())
	}

	childHome := extractReported(t, out.String(), "HOME")
	childTmuxTmpdir := extractReported(t, out.String(), "TMUX_TMPDIR")

	// This test DELIBERATELY makes the child isolate and then os.Exit past its
	// own cleanup, so the dirs it just created are stranded. That is the very
	// leak this branch exists to remove, so the parent — which caused it and is
	// the only thing that knows the paths — owns them.
	reapChildIsolationDirs(t, realHome, childHome, childTmuxTmpdir)

	if childHome == realHome {
		t.Errorf("a stale AGENT_DECK_TEST_HOME_ISOLATED made TestMain skip HOME isolation; "+
			"the suite would resolve config.json, the profile index and worker-scratch "+
			"under the REAL home (%s) and can wipe them — the 2026-06-04 data-loss "+
			"incident. testutil.HomeAlreadyIsolated must corroborate the marker against "+
			"the passwd database.", realHome)
	}
	if childTmuxTmpdir == "" || isDefaultTmuxBaseForTest(childTmuxTmpdir) {
		t.Errorf("a stale AGENT_DECK_TEST_ISOLATED made TestMain skip tmux isolation "+
			"(child TMUX_TMPDIR=%q). cleanupTestSessions then runs `tmux list-sessions` "+
			"and `kill-session` against the user's DEFAULT socket — the 2026-04-17 "+
			"incident. testutil.TmuxAlreadyIsolated must corroborate the marker against "+
			"TMUX_TMPDIR and TMUX.", childTmuxTmpdir)
	}
}

// reapChildIsolationDirs removes temp dirs a re-exec'd child created and then
// abandoned by exiting through os.Exit.
//
// Registered as a cleanup rather than run inline so it still fires when an
// assertion below fails — a failing leak test that itself leaks is worse than
// no test. Every path is checked against the owned prefixes and against the
// real home before removal: this runs in a package whose whole subject is
// deleting the wrong directory, so it refuses anything it does not recognise
// instead of trusting the child's output.
func reapChildIsolationDirs(t *testing.T, realHome string, dirs ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, dir := range dirs {
			if dir == "" || dir == realHome {
				continue
			}
			base := filepath.Base(filepath.Clean(dir))
			owned := false
			for _, prefix := range []string{"agent-deck-cmd-tests-home-", "ad-home-", "ad-tmux-"} {
				if strings.HasPrefix(base, prefix) {
					owned = true
					break
				}
			}
			if !owned {
				continue
			}
			if err := testutil.RemoveTempTree(dir); err != nil {
				t.Logf("could not reap child isolation dir %s: %v", dir, err)
			}
		}
	})
}

// isDefaultTmuxBaseForTest mirrors testutil's unexported isDefaultTmuxBase.
// tmux nests <TMUX_TMPDIR>/tmux-<uid>/<socket>, so a tmp root IS the default
// base and a tmux-<uid> dir is the default base one level in.
func isDefaultTmuxBaseForTest(dir string) bool {
	clean := filepath.Clean(dir)
	for _, base := range []string{"/tmp", "/private/tmp", filepath.Clean(os.TempDir())} {
		if clean == base {
			return true
		}
	}
	return strings.HasPrefix(filepath.Base(clean), "tmux-")
}

func dropEnv(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// TestHelperIsolationReportProcess is not a real test. TestHelperSubprocessDoesNotReIsolate
// invokes it via -test.run as a subprocess entrypoint that prints the isolation
// env it ended up with, then exits through os.Exit — the same defer-skipping
// exit path every real helper in this package takes. It is a no-op unless the
// marker is set.
func TestHelperIsolationReportProcess(t *testing.T) {
	if os.Getenv(helperIsolationReportMarker) != "1" {
		return
	}
	os.Stdout.WriteString("REPORT HOME=" + os.Getenv("HOME") + "\n")
	os.Stdout.WriteString("REPORT TMUX_TMPDIR=" + os.Getenv("TMUX_TMPDIR") + "\n")
	os.Exit(0)
}

func extractReported(t *testing.T, out, key string) string {
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
