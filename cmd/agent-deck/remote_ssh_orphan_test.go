package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeStuckRemote registers remote "lab" and puts an ssh on PATH that
// stands in for a remote that never answers: it records its own pid and the
// pid of a background child, then waits. Every recorded pid was started by
// this test and is SIGKILLed by exact pid at cleanup.
func fakeStuckRemote(t *testing.T, home string) func() []int {
	t.Helper()
	dir := t.TempDir()
	pidLog := filepath.Join(dir, "pids")
	script := `#!/bin/sh
echo $$ >> '` + pidLog + `'
sleep 30 &
echo $! >> '` + pidLog + `'
wait
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.lab]\nhost = 'lab-host'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	return read
}

func pidAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return !os.IsNotExist(err)
	}
	if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}

// A poller that times the CLI out sends it SIGTERM. The CLI must take its
// ssh child (and that child's whole group) down before it exits; before this
// fix the ssh was left running, reparented to init/launchd, holding the
// remote `list` open (2026-10 remote list pile-up).
func TestRemoteCLISIGTERMLeavesNoSSHOrphan(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	for name, args := range map[string][]string{
		"remote_sessions": {"remote", "sessions", "--json", "lab"},
		"session_output":  {"remote", "lab", "session", "output", "s1", "--json"},
		"session_send":    {"remote", "lab", "session", "send", "s1", "hi", "--json"},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			pids := fakeStuckRemote(t, home)
			cmd := exec.Command(channelsCLIBinary(t), args...)
			cmd.Env = agentDeckTestEnv(home, []string{"PATH=" + os.Getenv("PATH")})
			var out strings.Builder
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			deadline := time.Now().Add(10 * time.Second)
			for len(pids()) < 2 {
				if time.Now().After(deadline) {
					t.Fatalf("ssh never started: %s", out.String())
				}
				time.Sleep(20 * time.Millisecond)
			}
			started := pids()
			_ = cmd.Process.Signal(syscall.SIGTERM)
			exited := make(chan struct{})
			go func() { _ = cmd.Wait(); close(exited) }()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatal("CLI did not exit after SIGTERM")
			}
			deadline = time.Now().Add(3 * time.Second)
			for {
				var alive []int
				for _, pid := range started {
					if pidAlive(pid) {
						alive = append(alive, pid)
					}
				}
				if len(alive) == 0 {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("ssh child outlived the CLI: pids %v still alive", alive)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}
