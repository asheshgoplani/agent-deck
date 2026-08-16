package main

import (
	"strings"
	"testing"
)

// agent-deck shells out to `tmux` by bare name. When launched from a minimal
// environment — most importantly a `terminal-notifier -execute` notification
// click, whose PATH is the launchd default /usr/bin:/bin:/usr/sbin:/sbin —
// Homebrew's /opt/homebrew/bin is absent and every tmux call fails (no session
// switch, no detach, status flips to error). resolveTmuxPATH appends the first
// known tmux install dir so those calls resolve.

func TestResolveTmuxPATH_AppendsMissingDir(t *testing.T) {
	hasTmux := func(dir string) bool { return dir == "/opt/homebrew/bin" }
	got := resolveTmuxPATH("/usr/bin:/bin", false, []string{"/opt/homebrew/bin", "/usr/local/bin"}, hasTmux)
	want := "/usr/bin:/bin:/opt/homebrew/bin"
	if got != want {
		t.Fatalf("resolveTmuxPATH = %q, want %q", got, want)
	}
}

// The repaired PATH is process-wide (os.Setenv) and therefore inherited by
// every agent CLI agent-deck launches into a session. Appending keeps the
// user's own resolution order intact; prepending would let a broad candidate
// like /usr/bin shadow their version-managed toolchain inside those sessions
// while only ever being needed for a name that did not resolve at all.
//
// This pins the direction rather than just the concatenation: the candidate
// must land after every dir the caller already had.
func TestResolveTmuxPATH_DoesNotReorderExistingPATH(t *testing.T) {
	hasTmux := func(dir string) bool { return dir == "/usr/bin" }
	// a user PATH whose own bin dirs must keep winning
	path := "/home/u/.local/bin:/home/u/.nvm/versions/node/v22/bin"
	got := resolveTmuxPATH(path, false, []string{"/usr/bin"}, hasTmux)

	want := path + ":/usr/bin"
	if got != want {
		t.Fatalf("resolveTmuxPATH = %q, want %q", got, want)
	}
	// the load-bearing property, stated independently of the exact string:
	// nothing the caller already had may be displaced by the candidate.
	if !strings.HasPrefix(got, path) {
		t.Errorf("candidate dir must not be inserted ahead of the existing PATH: %q", got)
	}
}

func TestResolveTmuxPATH_NoopWhenAlreadyResolvable(t *testing.T) {
	hasTmux := func(string) bool { return true }
	got := resolveTmuxPATH("/usr/bin:/bin", true, []string{"/opt/homebrew/bin"}, hasTmux)
	if got != "/usr/bin:/bin" {
		t.Fatalf("resolveTmuxPATH must not change PATH when tmux already resolvable, got %q", got)
	}
}

func TestResolveTmuxPATH_NoopWhenCandidateAlreadyOnPath(t *testing.T) {
	hasTmux := func(string) bool { return true }
	path := "/opt/homebrew/bin:/usr/bin"
	got := resolveTmuxPATH(path, false, []string{"/opt/homebrew/bin"}, hasTmux)
	if got != path {
		t.Fatalf("resolveTmuxPATH must not duplicate a dir already on PATH, got %q", got)
	}
}

func TestResolveTmuxPATH_NoopWhenNoCandidateHasTmux(t *testing.T) {
	hasTmux := func(string) bool { return false }
	path := "/usr/bin:/bin"
	got := resolveTmuxPATH(path, false, []string{"/opt/homebrew/bin", "/usr/local/bin"}, hasTmux)
	if got != path {
		t.Fatalf("resolveTmuxPATH must not change PATH when no candidate has tmux, got %q", got)
	}
}

func TestResolveTmuxPATH_EmptyPathBecomesDir(t *testing.T) {
	hasTmux := func(dir string) bool { return dir == "/opt/homebrew/bin" }
	got := resolveTmuxPATH("", false, []string{"/opt/homebrew/bin"}, hasTmux)
	if got != "/opt/homebrew/bin" {
		t.Fatalf("resolveTmuxPATH with empty PATH = %q, want %q", got, "/opt/homebrew/bin")
	}
}
