package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// antigravityNoWaitStub is a fake `agy` that prints the sign-in menu and
// records its argv plus anything that reaches its stdin. A launch that embeds
// the prompt passes it as --prompt-interactive and types nothing; the older
// unchecked path typed the message and Enter into the menu.
const antigravityNoWaitStub = `#!/bin/sh
printf '%s' "$*" > "$AGY_TEST_ARGV"
printf '\n  Welcome to Antigravity\n\n  1. Sign in with Google\n  2. Use an API key\n'
timeout 2 dd bs=1 count=64 of="$AGY_TEST_STDIN" 2>/dev/null
exec sleep 60
`

// TestLaunchAntigravityNoWaitEmbedsPrompt pins the `launch -m ... --no-wait`
// contract for Antigravity: the prompt rides the command line, so the call
// returns without waiting for a composer and nothing is typed into a sign-in
// menu the process may be showing.
func TestLaunchAntigravityNoWaitEmbedsPrompt(t *testing.T) {
	parallelSubprocessTest(t)
	skipIfNoTmuxBinaryCLI(t)

	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	argvPath := filepath.Join(home, "agy-argv")
	stdinPath := filepath.Join(home, "agy-stdin")
	if err := os.WriteFile(filepath.Join(binDir, "agy"), []byte(antigravityNoWaitStub), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"AGY_TEST_ARGV=" + argvPath,
		"AGY_TEST_STDIN=" + stdinPath,
		// A private tmux socket keeps a sibling test's server environment out
		// of the pane; otherwise the stub agy/PATH can be lost.
		"TMUX_TMPDIR=" + t.TempDir(),
	}
	t.Cleanup(func() {
		_, _, _ = runAgentDeckEnv(t, home, "", env, "session", "stop", "agy-no-wait")
	})

	started := time.Now()
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env,
		"launch", project, "--title", "agy-no-wait", "--no-parent",
		"--cmd", "agy", "--message", "hello", "--no-wait", "--json")
	elapsed := time.Since(started)
	if code != 0 {
		t.Fatalf("launch failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	// Embedding makes the call return after the 3s post-start sync instead of
	// waiting out the composer timeout.
	if elapsed > 30*time.Second {
		t.Fatalf("--no-wait took %s; the prompt should not wait for the composer", elapsed)
	}

	deadline := time.Now().Add(10 * time.Second)
	var argv []byte
	for {
		argv, _ = os.ReadFile(argvPath)
		if len(argv) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := string(argv); !strings.Contains(got, "--prompt-interactive") || !strings.Contains(got, "hello") {
		t.Fatalf("agy argv = %q, want the prompt on the command line", got)
	}

	// The stub reads its stdin for two seconds; give it that plus a margin.
	time.Sleep(3 * time.Second)
	if typed, err := os.ReadFile(stdinPath); err == nil && len(typed) > 0 {
		t.Fatalf("typed into the sign-in menu: %q", typed)
	}
}
