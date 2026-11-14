package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestGoldensSandboxTmuxIsHermetic pins the tmux state the CLI goldens run
// against: whatever the host has (a live default server, a stale socket, or no
// socket file at all), tmux inside the goldens env must answer "no server
// running", the one result the inventory treats as a known-empty server.
// list_json.golden's cached status for golden-sess-4 depends on it.
func TestGoldensSandboxTmuxIsHermetic(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	_, env := goldensSandbox(t)

	for _, kv := range env {
		key := strings.SplitN(kv, "=", 2)[0]
		if key == "TMUX" || key == "TMUX_PANE" {
			t.Fatalf("goldens env leaks %s into the binary", key)
		}
	}

	cmd := exec.Command("tmux", "list-sessions")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("tmux list-sessions succeeded in the goldens env (host server leaked in): %q", out)
	}
	if got := strings.TrimSpace(string(out)); !strings.HasPrefix(got, "no server running on ") {
		t.Fatalf("tmux in the goldens env = %q, want \"no server running on <private socket>\"", got)
	}
}

// TestGoldensSandboxPiIsHermetic: `pi-hooks status` runs `pi --version`, so a
// pi on the host must never be seen by the goldens. Status resolves pi through
// PiStatusEnv, whose PATH puts the spawn-path directories missing from PATH in
// front (on macOS, /opt/homebrew/bin): a host pi on PATH, and one in a
// spawn-path directory (the sandbox's ~/bin stands in for /opt/homebrew/bin,
// which a Linux run has no equivalent of), both lose to the sandbox's fake,
// which prints 1.3.0.
func TestGoldensSandboxPiIsHermetic(t *testing.T) {
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "pi"), []byte("#!/bin/sh\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", host+string(os.PathListSeparator)+os.Getenv("PATH"))
	home, env := goldensSandbox(t)
	spawnDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(spawnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spawnDir, "pi"), []byte("#!/bin/sh\necho 9.9.8\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			t.Setenv("PATH", v)
		}
	}
	statusEnv := session.PiStatusEnv()
	var path string
	for _, kv := range statusEnv {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, "pi")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		cmd := exec.Command(candidate, "--version")
		cmd.Env = statusEnv
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != "1.3.0" {
			t.Fatalf("pi-hooks status resolves pi to %s, which printed %q (err %v); want the sandbox's fake (1.3.0): a host pi leaks into the goldens", candidate, out, err)
		}
		return
	}
	t.Fatal("the status PATH has no pi at all; status would print no Pi line")
}
