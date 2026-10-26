package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Issue #2921: TestGuardedSendLeavesBusyOperatorDraftAlone failed on its
// TempDir cleanup ("directory not empty"). The Claude-like fake rewrites
// <transcript>.input-state.json through a .tmp file every 50ms, and the
// fixture's cleanup stopped it with the asynchronous tmux Kill, which returns
// as soon as kill-session does, before the fake has exited. TempDir's
// RemoveAll, which runs next, then raced the fake's last write.
//
// The fixture must not hand its directory back while the fake can still write
// into it: once the fixture's cleanup has run, the fake's process is gone. The
// fake here ignores SIGHUP, as Claude Code 2.1.27+ does, which turns the
// scheduling race into a certain one: it outlives kill-session.
func TestIssue2921ClaudeLikeFakeIsGoneWhenItsCleanupReturns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("finds the fake's process through /proc")
	}
	var script string
	t.Run("fixture", func(t *testing.T) {
		_, transcript := claudeLikeFakeControl(t, "busy", map[string]any{claudeLikeFakeIgnoresHangup: true})
		script = filepath.Join(filepath.Dir(transcript), "claude")
	})
	if script == "" {
		t.Fatal("fixture did not start")
	}
	if pids := liveProcessesRunning(script); len(pids) > 0 {
		t.Fatalf("the Claude-like fake (%s) was still running after its fixture's cleanup returned: pids %v", script, pids)
	}
}

// liveProcessesRunning lists the non-zombie processes whose argv names path.
func liveProcessesRunning(path string) []string {
	entries, _ := os.ReadDir("/proc")
	var pids []string
	for _, e := range entries {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		argv, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || !bytes.Contains(argv, []byte(path)) {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // exited between the reads
		}
		if i := bytes.LastIndexByte(stat, ')'); i >= 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
			continue // a zombie cannot write
		}
		pids = append(pids, e.Name())
	}
	return pids
}
