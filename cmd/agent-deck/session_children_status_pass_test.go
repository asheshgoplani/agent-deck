package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// childRowsFleetSize is the fleet the timing in the test log describes: a
// conductor with a dozen-odd workers.
const childRowsFleetSize = 13

// The children rows are read-only: `session children --json`, `--follow` and
// the parent's UserPromptSubmit/SessionStart hook context only report each
// child's status. They must not run native session-ID discovery per child.
// For a live Codex child that discovery starts ps for the child's process
// tree and again for each pane process, in every fresh CLI process, which is
// every prompt the parent takes.
//
// A recording ps on PATH counts every ps the row build starts. The statuses
// must be the ones the full update path reports for the same children.
func TestChildrenRows_SkipNativeSessionDiscovery(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	realPS, err := exec.LookPath("ps")
	if err != nil {
		t.Skip("ps not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	psLog := filepath.Join(home, "ps.log")
	fakePS := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nexec %q \"$@\"\n", psLog, realPS)
	if err := os.WriteFile(filepath.Join(binDir, "ps"), []byte(fakePS), 0o755); err != nil {
		t.Fatal(err)
	}
	psCalls := func() int {
		data, err := os.ReadFile(psLog)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "\n")
	}

	kids := make([]*session.Instance, 0, childRowsFleetSize)
	for n := 0; n < childRowsFleetSize; n++ {
		kid := session.NewInstanceWithTool(fmt.Sprintf("child-pass-%d", n), home, "codex")
		ts := kid.GetTmuxSession()
		if err := ts.Start("sh -c 'echo working; exec sleep 3600'"); err != nil {
			t.Fatalf("tmux start: %v", err)
		}
		t.Cleanup(func() { _ = ts.Kill() })
		kid.Command = "codex"
		ts.Command = "codex"
		kids = append(kids, kid)
	}
	time.Sleep(2 * time.Second) // past UpdateStatus's tmux grace window

	// No hook file: these are children whose hook is missing or stale, so
	// their status comes from the pane and the full update path goes on to
	// discovery. Once startup is over the pane reads waiting.
	for _, kid := range kids {
		kid.GetTmuxSession().MarkInteractiveAt(time.Now())
	}

	// Only the row build runs with the recording ps on PATH.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, sampling := range []childRowsSampling{cachedChildStatus, sampleChildPanes} {
		session.RefreshInstancesForCLIStatus(kids)
		before := psCalls()
		start := time.Now()
		rows := buildChildRows(kids, sampling)
		elapsed := time.Since(start)
		calls := psCalls() - before
		t.Logf("buildChildRows(sampling=%v): %d children, %d ps processes, %s",
			sampling, len(kids), calls, elapsed.Round(time.Millisecond))
		if len(rows) != len(kids) {
			t.Fatalf("rows = %d, want %d", len(rows), len(kids))
		}
		if calls != 0 {
			t.Fatalf("buildChildRows(sampling=%v) started %d ps processes for %d children, want 0 (rows are read-only; native session-ID discovery belongs to the poller)",
				sampling, calls, len(kids))
		}
	}

	// The read-only pass reports what the full update path reports.
	session.RefreshInstancesForCLIStatus(kids)
	rows := buildChildRows(kids, cachedChildStatus)
	live := 0
	for n, kid := range kids {
		_ = kid.UpdateStatus()
		full := StatusString(kid.Status)
		if rows[n].Status != full {
			t.Fatalf("child %d: row status %q, full UpdateStatus %q", n, rows[n].Status, full)
		}
		if kid.Status == session.StatusRunning || kid.Status == session.StatusWaiting {
			live++
		}
	}
	// Discovery only runs for running or waiting children, so the ps count
	// above proves nothing unless the fleet is live.
	if live != len(kids) {
		t.Fatalf("%d of %d children are running or waiting; the fixture must keep the whole fleet live", live, len(kids))
	}
}
