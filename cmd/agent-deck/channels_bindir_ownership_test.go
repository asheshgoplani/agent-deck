package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestChannelsCLIBinDirRetryDoesNotOrphan covers the failed-build retry path in
// channelsCLIBinary.
//
// channelsCLIBinary caches its build behind channelsCLIBuildOK. A build that
// fails calls t.Fatalf WITHOUT setting that flag, so the next test needing the
// binary re-enters and builds again. If that second call simply assigned a
// fresh MkdirTemp to channelsCLIBinDir, the first directory would become
// unreachable — removeChannelsCLIBinDir only ever knows about the latest one —
// and would leak permanently. Only one dir is tracked, so ownership has to be
// released before it is reassigned.
//
// This drives the state machine directly rather than forcing a real `go build`
// failure, which would cost ~5s and need the package source sabotaged.
func TestChannelsCLIBinDirRetryDoesNotOrphan(t *testing.T) {
	channelsCLIBuildMu.Lock()
	origDir, origPath, origOK := channelsCLIBinDir, channelsCLIBinPath, channelsCLIBuildOK
	channelsCLIBuildMu.Unlock()
	t.Cleanup(func() {
		channelsCLIBuildMu.Lock()
		defer channelsCLIBuildMu.Unlock()
		// Restoring the saved state would ORPHAN whatever this test built:
		// channelsCLIBinDir tracks exactly one path, so overwriting it with the
		// original leaves the new dir unreachable by removeChannelsCLIBinDir and
		// leaks it — the precise bug this test exists to catch, committed by the
		// test itself. Release it first, then restore.
		if channelsCLIBinDir != "" && channelsCLIBinDir != origDir {
			if err := testutil.RemoveTempTree(channelsCLIBinDir); err != nil {
				t.Logf("could not release the rebuilt dir %s: %v", channelsCLIBinDir, err)
			}
		}
		channelsCLIBinDir, channelsCLIBinPath, channelsCLIBuildOK = origDir, origPath, origOK
	})

	// Stand in for the directory a failed build left behind.
	abandoned, err := os.MkdirTemp("", "agent-deck-channels-bin-*")
	if err != nil {
		t.Fatalf("mkdir abandoned: %v", err)
	}
	// Make it non-trivial so a cleanup that only handles empty dirs is caught.
	if err := os.WriteFile(filepath.Join(abandoned, "agent-deck-test"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale binary: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abandoned) })

	channelsCLIBuildMu.Lock()
	channelsCLIBinDir = abandoned
	channelsCLIBinPath = ""
	channelsCLIBuildOK = false
	channelsCLIBuildMu.Unlock()

	// The retry. It must release `abandoned` before claiming a new directory.
	bin := channelsCLIBinary(t)

	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("the previous failed-build dir %s survived a rebuild (stat err = %v).\n\n"+
			"channelsCLIBinDir tracks exactly one directory, so reassigning it without "+
			"removing the old one orphans that directory forever — nothing can reach it "+
			"to clean it up (2026-08-10 temp-leak incident).", abandoned, err)
	}

	channelsCLIBuildMu.Lock()
	current := channelsCLIBinDir
	channelsCLIBuildMu.Unlock()

	if current == abandoned || current == "" {
		t.Fatalf("channelsCLIBinDir = %q after rebuild; want a fresh owned directory", current)
	}
	if !strings.HasPrefix(filepath.Base(current), "agent-deck-channels-bin-") {
		t.Errorf("rebuilt into %q, which is outside the owned prefix", current)
	}
	if filepath.Dir(bin) != current {
		t.Errorf("returned binary %q does not live in the tracked dir %q", bin, current)
	}
}
