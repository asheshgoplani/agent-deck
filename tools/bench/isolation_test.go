package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The opt-in proof creates a sentinel in another private TMUX_TMPDIR. Neither
// successful nor failed benchmark workers may stop it or leave their fleet up.
func TestFleetIsolationTeardown(t *testing.T) {
	if os.Getenv("AGENTDECK_BENCH_ISOLATION_TEST") != "1" {
		t.Skip("Docker-only opt-in live tmux isolation proof")
	}
	real, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := t.TempDir()
	sentinelEnv := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + sentinel, "TMUX_TMPDIR=" + sentinel, "TERM=xterm"}
	run := func(args ...string) error {
		c := exec.Command(real, append([]string{"-L", "sentinel"}, args...)...)
		c.Env = sentinelEnv
		return c.Run()
	}
	if err = run("new-session", "-d", "-s", "sentinel", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run("kill-server") }()
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "worker")
			body := `#!/bin/sh
tmux new-session -d -s benchmark /bin/sh || exit 21
# A selector escape must fail before reaching any server.
tmux -S /tmp/not-a-benchmark-socket list-sessions && exit 22
tmux -u -S /tmp/not-a-benchmark-socket list-sessions && exit 24
tmux -u -L wrong-socket list-sessions && exit 25
while [ "$#" -gt 0 ]; do
 if [ "$1" = "-out" ]; then shift; output="$1"; fi
 shift
done
printf '[]\n' > "$output"
`
			if failure {
				body += "exit 23\n"
			}
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := isolated(context.Background(), script, real, "unused", "unused", 10, 1, 1)
			if (err != nil) != failure {
				t.Fatalf("failure=%v err=%v", failure, err)
			}
			if err = run("has-session", "-t", "sentinel"); err != nil {
				t.Fatalf("sentinel server affected: %v", err)
			}
		})
	}
}
