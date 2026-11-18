package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `launch --no-wait -m` on a plain shell, with the real binary in a throwaway
// profile: it returns once the message is typed. Before the fix it ran
// Claude's prompt-consumed check on every tool: two 10 s windows looking for
// a Claude composer a shell never draws, a retry that typed the message a
// second time, then a warning about claude.
//
// The time bound is half the old 20 s and several times the time after the
// fix, so a slow CI host does not trip it.
func TestLaunchNoWaitE2E_ShellReturnsFastWithoutClaudePromptCheck(t *testing.T) {
	for _, tool := range []string{"tmux", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("requires %s", tool)
		}
	}
	e := newIssue2530Env(t)
	t.Cleanup(e.killDetached)
	project := filepath.Join(e.root, "proj", "nowait")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	const marker = "nowait-marker-f6"
	cmd := exec.Command(e.bin, "-p", issue2530Profile, "launch", project, "-c", "bash", "-t", "nowait", "--no-wait", "-m", "echo "+marker, "--json")
	cmd.Env = e.env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	took := time.Since(start)
	t.Logf("launch --no-wait -m on a shell: %d ms", took.Milliseconds())
	if err != nil {
		t.Fatalf("launch: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	var res struct {
		Success bool   `json:"success"`
		Tool    string `json:"tool"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &res); err != nil || !res.Success {
		t.Fatalf("launch --json: %v\n%s", err, stdout.String())
	}
	if res.Tool != "shell" {
		t.Fatalf("tool = %q, want shell", res.Tool)
	}
	// Every warning of the check names the launch prompt; the one a shell
	// got also named claude.
	if text := stderr.String(); strings.Contains(text, "launch prompt") || strings.Contains(text, "consumed by claude") {
		t.Errorf("launch of a shell session ran the launch-prompt check and warned:\n%s", text)
	}
	if limit := 10 * time.Second; took > limit {
		t.Errorf("launch --no-wait -m on a shell took %v, want under %v", took.Round(time.Millisecond), limit)
	}

	// The message reached the shell and ran, once.
	outputLines := func() int {
		n := 0
		for _, line := range strings.Split(e.cli("session", "output", "--pane", "-q", "nowait"), "\n") {
			if strings.TrimSpace(line) == marker {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for outputLines() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the shell never ran the launch message:\n%s", e.cli("session", "output", "--pane", "-q", "nowait"))
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(time.Second)
	if n := outputLines(); n != 1 {
		t.Errorf("the launch message ran %d times, want once:\n%s", n, e.cli("session", "output", "--pane", "-q", "nowait"))
	}
}
