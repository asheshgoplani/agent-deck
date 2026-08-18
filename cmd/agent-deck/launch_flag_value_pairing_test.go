package main

import (
	"flag"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A space-separated string flag must keep its value. reorderArgsForFlagParsing
// used to consult a hand-maintained map of value-taking flag names; a flag
// missing from that map (--base, --idle-timeout) had its value demoted to a
// positional, which left two flags adjacent and let flag.Parse bind the
// FOLLOWING flag as the value. `launch . -w f -b --base dev --title X` came out
// as base="--title" with the title X as the project path
// ("path does not exist: X"), while --title=X worked. Same family as #974/#1928.
func TestLaunchFlagValuePairing_ReorderKeepsEveryValueFlagPaired(t *testing.T) {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	cmd := fs.String("c", "", "tool")
	worktree := fs.String("w", "", "worktree branch")
	newBranch := fs.Bool("b", false, "new branch")
	base := fs.String("base", "", "base revision")
	title := fs.String("title", "", "title")
	idleTimeout := fs.String("idle-timeout", "", "idle timeout")

	in := []string{".", "-c", "codex", "-w", "f", "-b", "--base", "dev", "--title", "X", "--idle-timeout", "30m"}
	got := reorderArgsForFlagParsing(fs, in)

	if got[len(got)-1] != "." {
		t.Errorf("positional path should be last, got %v", got)
	}
	joined := strings.Join(got, " ")
	for _, pair := range []string{"--base dev", "--title X", "--idle-timeout 30m", "-c codex", "-w f"} {
		if !strings.Contains(joined, pair) {
			t.Errorf("reorder separated %q: %v", pair, got)
		}
	}

	if err := fs.Parse(normalizeArgs(fs, got)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *base != "dev" || *title != "X" || *idleTimeout != "30m" || *cmd != "codex" || *worktree != "f" || !*newBranch {
		t.Errorf("parsed wrong: cmd=%q w=%q b=%v base=%q title=%q idle=%q",
			*cmd, *worktree, *newBranch, *base, *title, *idleTimeout)
	}
	if fs.Arg(0) != "." {
		t.Errorf("path = %q, want %q", fs.Arg(0), ".")
	}
}

// "--" must still end flag processing rather than being reordered as a flag.
func TestLaunchFlagValuePairing_ReorderHonorsDoubleDash(t *testing.T) {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	title := fs.String("title", "", "title")

	got := reorderArgsForFlagParsing(fs, []string{"--title", "X", "--", "-not-a-flag"})
	if err := fs.Parse(normalizeArgs(fs, got)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *title != "X" {
		t.Errorf("title = %q, want X", *title)
	}
	if fs.Arg(0) != "-not-a-flag" {
		t.Errorf("positional = %q, want -not-a-flag (full=%v)", fs.Arg(0), got)
	}
}

// End-to-end through the real binary, so the flags handleLaunch/handleAdd
// actually register are the ones under test. A nonexistent path makes both
// commands exit at path validation, before anything is created — and the error
// names whichever argument was parsed as the path, which is exactly the
// symptom: with the bug it reports "X", the value of --title.
func TestLaunchFlagValuePairing_BinaryParsesSpaceSeparatedFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}

	binPath := filepath.Join(t.TempDir(), "agent-deck-flag-pairing")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\noutput: %s", err, out)
	}

	const missingPath = "/nonexistent-agent-deck-flag-pairing"
	cases := map[string][]string{
		"launch_base_then_title": {"launch", missingPath, "-c", "claude", "-w", "feat", "-b", "--base", "dev", "--title", "X"},
		"launch_idle_timeout":    {"launch", missingPath, "-c", "claude", "--idle-timeout", "30m", "--title", "X"},
		"launch_title_only":      {"launch", missingPath, "-c", "claude", "--title", "X"},
		"add_socket_then_title":  {"add", missingPath, "-c", "claude", "--tmux-socket", "iso", "--title", "X"},
	}

	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(binPath, argv...)
			cmd.Dir = t.TempDir()
			cmd.Env = sandboxedCLIEnv(t.TempDir())

			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected failure for a nonexistent path; output: %s", out)
			}
			text := string(out)
			if strings.Contains(text, "path does not exist: X") || strings.Contains(text, "does not exist: X") {
				t.Fatalf("--title's value was parsed as the project path; output: %s", text)
			}
			if !strings.Contains(text, missingPath) {
				t.Fatalf("error should name the path argument %q; output: %s", missingPath, text)
			}
		})
	}
}
