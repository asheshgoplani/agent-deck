package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestAgentDeckTestEnvStripsSSHSessionVars: the CLI test env must not pass
// the developer's own ssh session to the binary. rowsReadIsEgress reads
// SSH_CONNECTION, SSH_CLIENT and SSH_TTY, so an inherited SSH_CLIENT made a
// local rows read refuse, and an inherited SSH_TTY let the older-relay
// shape through, when go test ran over ssh.
func TestAgentDeckTestEnvStripsSSHSessionVars(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.0.2.1 50000 192.0.2.2 22")
	t.Setenv("SSH_CLIENT", "192.0.2.1 50000 22")
	t.Setenv("SSH_TTY", "/dev/pts/7")
	for _, kv := range agentDeckTestEnv(t.TempDir(), nil) {
		for _, name := range []string{"SSH_CONNECTION=", "SSH_CLIENT=", "SSH_TTY="} {
			if strings.HasPrefix(kv, name) {
				t.Errorf("test env leaks the parent's %s", kv)
			}
		}
	}
	// An explicit extraEnv entry still reaches the binary.
	env := agentDeckTestEnv(t.TempDir(), []string{"SSH_CLIENT=198.51.100.1 1 22"})
	found := false
	for _, kv := range env {
		if kv == "SSH_CLIENT=198.51.100.1 1 22" {
			found = true
		}
	}
	if !found {
		t.Errorf("extraEnv SSH_CLIENT dropped")
	}
}

// TestRecallRowsSSHExecWithTerminalStdinIsServed exercises the real
// term.IsTerminal branch of nonInteractiveSSHExec: an ssh session with
// SSH_CONNECTION set but no SSH_TTY and no TMUX, whose stdin is a real
// terminal, is interactive and is served with recall off. The same call
// with a pipe on stdin is the older-relay shape and refuses.
func TestRecallRowsSSHExecWithTerminalStdinIsServed(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	writeMacappConfig(t, home, "[recall]\nenabled = false\n")
	env := []string{"SSH_CONNECTION=192.0.2.1 50000 192.0.2.2 22"}

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty on this host: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), agentDeckExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, channelsCLIBinary(t), "recall", "timeline", id, "--json")
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = agentDeckTestEnv(home, env)
	cmd.Stdin = tty
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()
	code := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if code != 0 || !strings.Contains(out.String(), `"turns"`) {
		t.Fatalf("ssh session with a terminal on stdin, recall off: want rows served, got exit %d %s %s", code, out.String(), errOut.String())
	}

	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "recall", "timeline", id, "--json")
	if code != 2 || !strings.Contains(stdout, "recall is off") {
		t.Fatalf("same call with a pipe on stdin: want exit 2 recall is off, got %d %s %s", code, stdout, stderr)
	}
}
