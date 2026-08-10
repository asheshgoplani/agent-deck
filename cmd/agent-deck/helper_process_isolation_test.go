package main

import (
	"bytes"
	"os"
	"os/exec"
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
