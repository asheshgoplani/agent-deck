package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeHangingSSH puts an ssh on PATH that records its argv and its own pid,
// starts a background child (standing in for a ProxyCommand or a stuck
// helper) whose pid it records too, and then waits on it. It returns a
// function that reads the recorded pids once both are written. Every pid it
// reports was started by this test and is SIGKILLed by exact pid at cleanup.
func fakeHangingSSH(t *testing.T) (pids func() []int, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	pidLog := filepath.Join(dir, "pids")
	argvLog = filepath.Join(dir, "argv")
	script := `#!/bin/sh
printf '%s\n' "$*" >> '` + argvLog + `'
echo $$ >> '` + pidLog + `'
sleep 30 &
echo $! >> '` + pidLog + `'
wait
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	read := func() []int {
		data, _ := os.ReadFile(pidLog)
		var out []int
		for _, f := range strings.Fields(string(data)) {
			if n, err := strconv.Atoi(f); err == nil && n > 1 {
				out = append(out, n)
			}
		}
		return out
	}
	t.Cleanup(func() {
		for _, pid := range read() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return func() []int {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if p := read(); len(p) >= 2 {
				return p
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("fake ssh never recorded its pids")
		return nil
	}, argvLog
}

// sshPidAlive reports whether pid is a live (non-zombie) process.
func sshPidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return !os.IsNotExist(err)
	}
	// Field 3 (after "pid (comm)") is the state; Z is a zombie.
	if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}

func waitAllDead(t *testing.T, pids []int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var alive []int
		for _, pid := range pids {
			if sshPidAlive(pid) {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ssh process group outlived the command: pids %v still alive", alive)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A one-shot remote command that hits its timeout must take ssh's whole
// process group down with it, not just the ssh pid: nothing it started may
// be left reparented to init/launchd (2026-10 remote list pile-up).
func TestSSHRunTimeoutKillsWholeProcessGroup(t *testing.T) {
	pids, _ := fakeHangingSSH(t)
	r := NewSSHRunner("box", RemoteConfig{Host: "user@box.example", CommandTimeoutSeconds: 1})
	r.cleanChannelSocketsFn = func() {}
	start := time.Now()
	_, err := r.Run(context.Background(), "list", "--json")
	if err == nil {
		t.Fatal("Run against a hanging ssh returned no error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run took %v; the 1s command timeout did not bound it", elapsed)
	}
	waitAllDead(t, pids(), 3*time.Second)
}

// RunIO (the forwarded `remote <host> session output|send ...` path) obeys
// cancellation the same way.
func TestSSHRunIOCancelKillsWholeProcessGroup(t *testing.T) {
	pids, _ := fakeHangingSSH(t)
	r := NewSSHRunner("box", RemoteConfig{Host: "user@box.example"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.RunIO(ctx, nil, &strings.Builder{}, &strings.Builder{}, "session", "output", "s1", "--json")
	}()
	got := pids()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunIO did not return after cancellation")
	}
	waitAllDead(t, got, 3*time.Second)
}

// KillSSHProcesses (the CLI's signal path) kills every live group and
// refuses to start new ones.
func TestKillSSHProcessesKillsTrackedGroups(t *testing.T) {
	pids, _ := fakeHangingSSH(t)
	t.Cleanup(func() {
		sshGroups.Lock()
		sshGroups.closing = false
		sshGroups.Unlock()
	})
	r := NewSSHRunner("box", RemoteConfig{Host: "user@box.example"})
	done := make(chan error, 1)
	go func() {
		done <- r.RunIO(context.Background(), nil, &strings.Builder{}, &strings.Builder{}, "session", "output", "s1")
	}()
	got := pids()
	KillSSHProcesses()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunIO did not return after KillSSHProcesses")
	}
	waitAllDead(t, got, 3*time.Second)
	if err := r.RunIO(context.Background(), nil, &strings.Builder{}, &strings.Builder{}, "session", "output", "s1"); err == nil {
		t.Fatal("an ssh started after KillSSHProcesses must be refused")
	}
}

// One-shot commands carry keepalives (a dead link fails in ~45 s) and keep
// the existing dial bound, each option exactly once.
func TestSSHBaseArgsCarryKeepaliveAndConnectTimeout(t *testing.T) {
	r := NewSSHRunner("box", RemoteConfig{Host: "user@box.example"})
	args := strings.Join(r.sshBaseArgs("true"), " ")
	for _, opt := range []string{"ServerAliveInterval=", "ServerAliveCountMax=", "ConnectTimeout="} {
		if n := strings.Count(args, opt); n != 1 {
			t.Fatalf("want %s exactly once in %q, got %d", opt, args, n)
		}
	}
	channel := strings.Join(r.sshChannelArgs("true"), " ")
	if n := strings.Count(channel, "ServerAliveInterval="); n != 1 {
		t.Fatalf("channel args must not duplicate keepalives: %q", channel)
	}
}

// The remote command of a deadline-bound run is wrapped in the remote's own
// timeout(1), so the remote process dies even when ssh vanished.
func TestSSHRunWrapsRemoteCommandInRemoteTimeout(t *testing.T) {
	_, argvLog := fakeHangingSSH(t)
	r := NewSSHRunner("box", RemoteConfig{Host: "user@box.example", CommandTimeoutSeconds: 1, Profile: "work"})
	r.cleanChannelSocketsFn = func() {}
	_, _ = r.Run(context.Background(), "list", "--json")
	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	for _, want := range []string{"command -v timeout", "timeout -k 5 3", `'agent-deck' -p 'work' 'list' '--json'`} {
		if !strings.Contains(line, want) {
			t.Fatalf("remote command missing %q: %s", want, line)
		}
	}
}

func writeFakeCore(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake core")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// runRemoteShell runs a remote command string the way sshd does (the login
// shell's -c), in its own group, bounded and cleaned up by exact pid.
func runRemoteShell(t *testing.T, command string, env []string, bound time.Duration) (string, time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Env = env
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	return out.String(), time.Since(start), err
}

// With timeout(1) on the remote, a stuck remote command ends at its own
// deadline even though nothing local ever signals it.
func TestRemoteTimeoutCommandEndsStuckRemoteProcess(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout(1) not installed")
	}
	core := writeFakeCore(t, "exec sleep 60\n")
	command := remoteTimeoutCommand(shellQuote(core)+" 'list'", time.Second)
	_, elapsed, err := runRemoteShell(t, command, os.Environ(), 20*time.Second)
	if err == nil {
		t.Fatal("stuck remote command exited cleanly")
	}
	if elapsed > 12*time.Second {
		t.Fatalf("remote timeout did not end the remote process (took %v)", elapsed)
	}
}

// Without timeout(1) the command still runs, unwrapped, with every argument
// intact (spaces and quotes included).
func TestRemoteTimeoutCommandFallsBackWithoutTimeout(t *testing.T) {
	core := writeFakeCore(t, `for a; do printf '[%s]' "$a"; done`+"\n")
	r := &SSHRunner{AgentDeckPath: core, Profile: "it's mine"}
	command := remoteTimeoutCommand(r.buildRemoteCommand("session", "send", "s1", `a "b" $c`), time.Minute)
	out, _, err := runRemoteShell(t, command, []string{"PATH=" + t.TempDir()}, 10*time.Second)
	if err != nil {
		t.Fatalf("fallback failed: %v: %s", err, out)
	}
	if want := `[-p][it's mine][session][send][s1][a "b" $c]`; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}
