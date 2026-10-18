package main

import (
	"os/exec"
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
