package testutil

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Regression coverage for the post-condition on TMUX_TMPDIR.
//
// Isolation that silently fails to isolate is worse than no isolation, because
// cleanup then runs kill-server against whatever it finds under the "isolated"
// dir. If that dir is tmux's default base, the isolation helper becomes the
// thing that kills the user's whole fleet. assertIsolatedTmuxTmpdir is the
// checked invariant; these tests pin it.

func TestAssertIsolatedTmuxTmpdir_RefusesDefaultBases(t *testing.T) {
	uid := os.Getuid()
	cases := []string{
		"/tmp",
		"/tmp/",
		"/private/tmp",
		filepath.Clean(os.TempDir()),
		filepath.Join("/tmp", "tmux-"+strconv.Itoa(uid)),
		filepath.Join("/private/tmp", "tmux-"+strconv.Itoa(uid)),
	}
	for _, dir := range cases {
		t.Run(dir, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("assertIsolatedTmuxTmpdir(%q) accepted tmux's default socket base — "+
						"tests would spawn on the user's live server and cleanup would kill it", dir)
				}
				msg, ok := r.(string)
				if !ok || !strings.Contains(msg, "TMUX_TMPDIR") {
					t.Fatalf("panic does not explain the refusal: %v", r)
				}
			}()
			assertIsolatedTmuxTmpdir(dir)
		})
	}
}

func TestAssertIsolatedTmuxTmpdir_AcceptsPrivateDirs(t *testing.T) {
	cases := []string{
		"/tmp/ad-tmux-abc123",
		"/private/tmp/ad-tmux-abc123",
		"/var/folders/qx/T/ad-tmux-abc123",
		"/tmp/agent-deck-test-tmux-fallback-4242",
	}
	for _, dir := range cases {
		t.Run(dir, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("assertIsolatedTmuxTmpdir(%q) refused a legitimately private dir: %v", dir, r)
				}
			}()
			assertIsolatedTmuxTmpdir(dir)
		})
	}
}

// TestIsolateTmuxSocketSetsAPrivateDir is the end-to-end of the same
// invariant: whatever the helper actually chooses on this host must satisfy it.
func TestIsolateTmuxSocketSetsAPrivateDir(t *testing.T) {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		t.Fatal("package TestMain did not call IsolateTmuxSocket")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the TMUX_TMPDIR this package is running under (%q) is not private: %v", dir, r)
		}
	}()
	assertIsolatedTmuxTmpdir(dir)
}
