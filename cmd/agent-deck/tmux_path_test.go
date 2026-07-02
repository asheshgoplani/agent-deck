package main

import "testing"

// agent-deck shells out to `tmux` by bare name. When launched from a minimal
// environment — most importantly a `terminal-notifier -execute` notification
// click, whose PATH is the launchd default /usr/bin:/bin:/usr/sbin:/sbin —
// Homebrew's /opt/homebrew/bin is absent and every tmux call fails (no session
// switch, no detach, status flips to error). resolveTmuxPATH prepends the first
// known tmux install dir so those calls resolve.

func TestResolveTmuxPATH_PrependsMissingDir(t *testing.T) {
	hasTmux := func(dir string) bool { return dir == "/opt/homebrew/bin" }
	got := resolveTmuxPATH("/usr/bin:/bin", false, []string{"/opt/homebrew/bin", "/usr/local/bin"}, hasTmux)
	want := "/opt/homebrew/bin:/usr/bin:/bin"
	if got != want {
		t.Fatalf("resolveTmuxPATH = %q, want %q", got, want)
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
