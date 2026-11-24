package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run the actual single-channel capability gate against a legacy core.
// Old usage must never reach JSON stdout and no unsupported read may start.
func TestRunReadIOLegacyGate(t *testing.T) {
	for _, args := range [][]string{
		{"recall", "timeline", "id", "--json", "--tail", "100"},
		{"recall", "timeline", "id", "--json", "--before", "cursor", "--limit", "100"},
		{"recall", "follow", "id", "--after", "cursor", "--jsonl"},
		{"events", "follow", "--jsonl", "--since", "100"},
		{"session", "send-status", "send-id", "--json"},
	} {
		t.Run(args[0]+"-"+args[1], func(t *testing.T) {
			dir := t.TempDir()
			shim := "#!/bin/sh\nfor arg do command=$arg; done\nexec sh -c \"$command\"\n"
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			marker := filepath.Join(dir, "read-started")
			core := filepath.Join(dir, "legacy core's binary")
			script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = --help ]; then\n  printf 'Usage: old core\\n'\n  printf 'flag provided but not defined: newer option\\n' >&2\n  exit 2\n fi\ndone\nprintf 'unexpected read' > " + shellQuote(marker) + "\n"
			if err := os.WriteFile(core, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			input, inputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer inputWriter.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runner := &SSHRunner{Host: "fixture", AgentDeckPath: core, Profile: "fixture-profile"}
			want := []byte("{\"error\":\"unsupported remote command\",\"remote\":\"fixture\",\"remote_version\":\"unknown\"}\n")
			var out, diagnostic bytes.Buffer
			err = runner.RunReadIO(ctx, input, &out, &diagnostic, want, args[1] == "follow", args...)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !bytes.Equal(out.Bytes(), want) || diagnostic.Len() != 0 {
				t.Fatalf("legacy refusal err=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsupported read started: %v", err)
			}
		})
	}
}

// A finite read carries the remote deadline, so the remote ends a stuck read
// even when the local ssh is gone and nothing local can signal it.
func TestRunReadIORemoteDeadline(t *testing.T) {
	for _, tool := range []string{"timeout", "setsid"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	dir := t.TempDir()
	// Like sshd, the shim runs the remote command in its own session, out of
	// reach of the local process group kill.
	shim := "#!/bin/sh\nfor arg do command=$arg; done\nsetsid sh -c \"$command\" </dev/null &\nwait $!\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	pidFile := filepath.Join(dir, "read.pid")
	core := filepath.Join(dir, "core")
	script := "#!/bin/sh\nfor arg do [ \"$arg\" = --help ] && exit 0; done\necho $$ > " + shellQuote(pidFile) + "\nexec sleep 60\n"
	if err := os.WriteFile(core, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runner := &SSHRunner{Host: "fixture", AgentDeckPath: core}
	var out, diagnostic bytes.Buffer
	_ = runner.RunReadIO(ctx, nil, &out, &diagnostic, []byte("unsupported"), false, "recall", "timeline", "id", "--json")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("remote read never started: %v (stderr %q)", err, diagnostic.String())
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	t.Fatal("the remote read outlived its deadline")
}
