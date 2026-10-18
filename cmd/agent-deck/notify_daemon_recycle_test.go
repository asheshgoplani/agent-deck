package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// writeVersionScript writes an executable at path whose `version` output is
// the given version, in the format parseAgentDeckVersion reads.
func writeVersionScript(t *testing.T, path, version string) {
	t.Helper()
	script := "#!/bin/sh\necho \"Agent Deck v" + version + "\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// swapLikeUpdater replaces the binary at path the way update.InstallUpdate
// does: write <path>.new, rename the running file to <path>.old, rename the
// new file into place, remove <path>.old.
func swapLikeUpdater(t *testing.T, path, version string) {
	t.Helper()
	writeVersionScript(t, path+".new", version)
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".old"); err != nil {
		t.Fatal(err)
	}
}

// A running daemon recycles when its binary is swapped under it. Before the
// fix the probe re-read os.Executable on every tick; on Linux that reports
// "<path>.old (deleted)" after the swap, the probe failed, and the daemon kept
// running the old build forever (seen on two hosts after the v1.16.30 update).
func TestNotifyDaemon_RecyclesWhenBinarySwappedUnderIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	exe := filepath.Join(t.TempDir(), "agent-deck")
	writeVersionScript(t, exe, Version)

	var calls atomic.Int32
	var swapped atomic.Bool
	executable := func() (string, error) {
		calls.Add(1)
		if swapped.Load() {
			return exe + ".old (deleted)", nil
		}
		return exe, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		watchBinaryVersionAt(ctx, cancel, executable, 10*time.Millisecond)
		close(done)
	}()

	// Same version on disk: no recycle.
	time.Sleep(60 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("daemon recycled while the on-disk version matched")
	}

	// From the first rename on, Linux reports the running file as deleted.
	swapped.Store(true)
	swapLikeUpdater(t, exe, Version+"-next")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not recycle after the binary was swapped under it")
	}
	if ctx.Err() == nil {
		t.Fatal("watcher returned without cancelling the daemon context")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("executable looked up %d times, want once at start", n)
	}
}

// A daemon whose path already reads as the swapped-out file still probes the
// install path.
func TestNotifyDaemon_RecyclesWhenStartedFromDeletedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	exe := filepath.Join(t.TempDir(), "agent-deck")
	writeVersionScript(t, exe, Version+"-next")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executable := func() (string, error) { return exe + ".old (deleted)", nil }
	done := make(chan struct{})
	go func() {
		watchBinaryVersionAt(ctx, cancel, executable, 10*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not recycle when its path read as the deleted .old file")
	}
}

func TestInstalledExePath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent-deck")
	writeVersionScript(t, exe, "1.0.0")
	realOld := filepath.Join(dir, "tool.old")
	writeVersionScript(t, realOld, "1.0.0")

	cases := []struct{ in, want string }{
		{exe, exe},
		{exe + " (deleted)", exe},
		{exe + ".old (deleted)", exe},
		{exe + ".old", exe},
		// No install path next to it: keep the real ".old" name.
		{realOld, realOld},
		{realOld + " (deleted)", realOld},
	}
	for _, c := range cases {
		if got := installedExePath(c.in); got != c.want {
			t.Errorf("installedExePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
