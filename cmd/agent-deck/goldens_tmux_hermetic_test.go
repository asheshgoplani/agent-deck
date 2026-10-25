package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
// pi on the host's PATH must never be seen by the goldens. Whatever the host
// has, `pi` resolves to the sandbox's fake, which prints 1.3.0.
func TestGoldensSandboxPiIsHermetic(t *testing.T) {
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "pi"), []byte("#!/bin/sh\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", host+string(os.PathListSeparator)+os.Getenv("PATH"))
	home, env := goldensSandbox(t)

	var path string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, "pi")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if !strings.HasPrefix(candidate, home+string(os.PathSeparator)) {
			t.Fatalf("the goldens PATH resolves pi to %s, outside the sandbox: a host pi leaks into the goldens", candidate)
		}
		cmd := exec.Command(candidate, "--version")
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != "1.3.0" {
			t.Fatalf("the sandbox pi printed %q (err %v), want 1.3.0", out, err)
		}
		return
	}
	t.Fatal("the goldens PATH has no pi at all; status would print no Pi line")
}
