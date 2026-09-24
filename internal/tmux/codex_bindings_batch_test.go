package tmux

import (
	"os/exec"
	"testing"
)

func TestListAgentDeckCodexSessionIDsOnSocket(t *testing.T) {
	const socket = "codex-bindings-batch-test"
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	run("new-session", "-d", "-s", "agentdeck_first")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	run("new-session", "-d", "-s", "agentdeck_second")
	run("new-session", "-d", "-s", "unmanaged")
	run("set-environment", "-t", "agentdeck_first", "CODEX_SESSION_ID", "first-id")
	run("set-environment", "-t", "unmanaged", "CODEX_SESSION_ID", "ignored-id")

	ids, err := ListAgentDeckCodexSessionIDsOnSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids["agentdeck_first"] != "first-id" {
		t.Fatalf("batch bindings = %v", ids)
	}
}
