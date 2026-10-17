package session

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The SSH fixture runs the actual quoted remote shell command, preserving
// its stdin and stdout. EOF must stop a silent or TERM-ignoring remote too.
func TestRunFollowIOStreamingAndEOF(t *testing.T) {
	for _, ignoreTERM := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "ignores-term"}[ignoreTERM], func(t *testing.T) {
			dir := t.TempDir()
			shim := "#!/bin/sh\nfor arg do command=$arg; done\nexec /bin/sh -c \"$command\"\n"
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			pidFile := filepath.Join(dir, "pid")
			script := "#!/bin/sh\nfor arg do if [ \"$arg\" = --help ]; then exit 0; fi; done\nprintf '%s' \"$$\" > " + shellQuote(pidFile) + "\n"
			if ignoreTERM {
				script += "trap '' TERM\n"
			}
			// A POSIX builtin read keeps this process alive without a subprocess.
			script += "printf '{\"kind\":\"row\"}\\n'\nwhile :; do :; done\n"
			binary := filepath.Join(dir, "core")
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			inR, inW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer inR.Close()
			defer inW.Close()
			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer outR.Close()
			defer outW.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			finished := make(chan struct{})
			done := make(chan error, 1)
			runner := &SSHRunner{Host: "fixture", AgentDeckPath: binary}
			defer func() {
				inW.Close()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("fixture cleanup timed out")
				}
				cancel()
			}()
			go func() {
				defer close(finished)
				done <- runner.RunReadIO(ctx, inR, outW, io.Discard, []byte("unsupported\n"), true, "recall", "follow", "id", "--jsonl")
			}()
			_ = outR.SetReadDeadline(time.Now().Add(5 * time.Second))
			frame := make([]byte, len("{\"kind\":\"row\"}\n"))
			if _, err := io.ReadFull(outR, frame); err != nil {
				t.Fatal(err)
			}
			if string(frame) != "{\"kind\":\"row\"}\n" {
				t.Fatalf("frame %q", frame)
			}
			select {
			case err := <-done:
				t.Fatalf("stream ended before stdin EOF: %v", err)
			default:
			}
			inW.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("EOF did not stop stream")
			}
			b, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			// kill -0 inspects only the fixture PID, and cannot affect a live session.
			probe := exec.Command("kill", "-0", strings.TrimSpace(string(b)))
			if probe.Run() == nil {
				t.Fatalf("remote core %s orphaned", b)
			}
		})
	}
}

func TestFollowRemoteCommandTermination(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	cmd := exec.Command("sh", "-c", followRemoteCommand("exec sh -c 'while :; do :; done'"))
	cmd.Stdin = r
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("TERM did not stop remote supervisor")
	}
}

func TestRunReadIODisconnectReapsRemote(t *testing.T) {
	dir := t.TempDir()
	// A local SSH-client stand-in owns the writer of the remote stdin pipe.
	// Killing that client closes the writer, exactly as an SSH disconnect does.
	shim := `#!/usr/bin/env python3
import os,sys,subprocess
p=subprocess.Popen(['sh','-c',sys.argv[-1]],stdin=subprocess.PIPE)
while True:
 b=os.read(0,8192)
 if not b: break
 p.stdin.write(b);p.stdin.flush()
p.stdin.close()
sys.exit(p.wait())
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	pidPath := filepath.Join(dir, "core.pid")
	core := filepath.Join(dir, "core")
	script := "#!/bin/sh\nfor arg do if [ \"$arg\" = --help ]; then exit 0; fi; done\nprintf '%s' \"$$\" > " + shellQuote(pidPath) + "\nprintf 'frame\\n'\nwhile :; do :; done\n"
	if err := os.WriteFile(core, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	runner := &SSHRunner{Host: "fixture", AgentDeckPath: core}
	go func() {
		done <- runner.RunReadIO(ctx, inR, outW, io.Discard, []byte("unsupported\n"), true, "events", "follow", "--jsonl")
	}()
	_ = outR.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame := make([]byte, 6)
	if _, err := io.ReadFull(outR, frame); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("client not stopped on cancel")
	}
	b, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(b))
	deadline := time.Now().Add(5 * time.Second)
	for exec.Command("kill", "-0", pid).Run() == nil {
		if time.Now().After(deadline) {
			t.Fatalf("remote core PID %s survived client disconnect", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReadReadyWriterFragmented(t *testing.T) {
	var out bytes.Buffer
	ready := 0
	w := &readReadyWriter{out: &out, ready: func() { ready++ }}
	for _, s := range []string{readReady[:2], readReady[2:] + "frame\n", strings.Repeat("x", 1024*1024)} {
		if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("write %d %v", n, err)
		}
	}
	if ready != 1 || !strings.HasPrefix(out.String(), "frame\n") || w.prefix != nil {
		t.Fatalf("ready=%d prefix=%q", ready, w.prefix)
	}
	out.Reset()
	w = &readReadyWriter{out: &out, ready: func() { t.Fatal("error triggered ready") }}
	message := "{\"error\":\"unsupported remote command\"}\n"
	for _, b := range []byte(message) {
		_, _ = w.Write([]byte{b})
	}
	if out.String() != message {
		t.Fatalf("error output %q", out.String())
	}
}

func TestRunReadIOControllerProcessHelper(t *testing.T) {
	core := os.Getenv("READIO_HELPER_CORE")
	if core == "" {
		return
	}
	runner := &SSHRunner{Host: "fixture", AgentDeckPath: core}
	if err := runner.RunReadIO(context.Background(), os.Stdin, os.Stdout, os.Stderr, []byte("unsupported\n"), true, "events", "follow", "--jsonl"); err != nil {
		t.Fatal(err)
	}
}

func TestRunReadIOControllerKillOwnsStdin(t *testing.T) {
	dir := t.TempDir()
	sshPID := filepath.Join(dir, "ssh.pid")
	corePID := filepath.Join(dir, "core.pid")
	shim := `#!/usr/bin/env python3
import os,sys,subprocess
open(os.environ['READIO_SSH_PID'],'w').write(str(os.getpid()))
p=subprocess.Popen(['sh','-c',sys.argv[-1]],stdin=subprocess.PIPE)
while True:
 b=os.read(0,8192)
 if not b: break
 p.stdin.write(b);p.stdin.flush()
p.stdin.close()
sys.exit(p.wait())
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(dir, "core")
	script := "#!/bin/sh\nfor arg do if [ \"$arg\" = --help ]; then exit 0; fi; done\nprintf '%s' \"$$\" > " + shellQuote(corePID) + "\nprintf 'frame\\n'\nwhile :; do :; done\n"
	if err := os.WriteFile(core, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("READIO_HELPER_CORE", core)
	t.Setenv("READIO_SSH_PID", sshPID)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()
	cmd := exec.Command(bin, "-test.run=^TestRunReadIOControllerProcessHelper$")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); inW.Close() })
	_ = outR.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame := make([]byte, 6)
	if _, err := io.ReadFull(outR, frame); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// Original caller input remains open: only the controller's owned pipe
	// can signal EOF to SSH. Inspect both scoped fixture PIDs after cleanup.
	for _, path := range []string{corePID, sshPID} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pid := strings.TrimSpace(string(b))
		deadline := time.Now().Add(5 * time.Second)
		for exec.Command("kill", "-0", pid).Run() == nil {
			if time.Now().After(deadline) {
				t.Fatalf("fixture PID %s survived controller SIGKILL while original stdin remained open", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestRunReadIOFiniteReadWithOpenInput(t *testing.T) {
	dir := t.TempDir()
	shim := "#!/bin/sh\nfor arg do command=$arg; done\nexec /bin/sh -c \"$command\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	core := filepath.Join(dir, "core")
	script := "#!/bin/sh\nfor arg do if [ \"$arg\" = --help ]; then printf 'help\\n'; exit 0; fi; done\nprintf 'snapshot\\n'\n"
	if err := os.WriteFile(core, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	runner := &SSHRunner{Host: "fixture", AgentDeckPath: core}
	for _, args := range [][]string{{"recall", "timeline", "id", "--json"}, {"recall", "follow", "--help"}} {
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		err := runner.RunReadIO(ctx, inR, &out, io.Discard, []byte("unsupported\n"), false, args...)
		cancel()
		want := "snapshot\n"
		if args[len(args)-1] == "--help" {
			want = "help\n"
		}
		if err != nil || out.String() != want || time.Since(start) > 2*time.Second {
			t.Fatalf("finite %q returned err=%v output=%q elapsed=%s with original input still open", args, err, out.String(), time.Since(start))
		}
	}
}

func TestRunReadIONaturalFollowEndWithOpenInput(t *testing.T) {
	dir := t.TempDir()
	shim := "#!/bin/sh\nfor arg do command=$arg; done\nexec /bin/sh -c \"$command\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	core := filepath.Join(dir, "core")
	payload := "{\"kind\":\"resync_required\"}\n"
	script := "#!/bin/sh\nfor arg do if [ \"$arg\" = --help ]; then exit 0; fi; done\nprintf '%s' " + shellQuote(payload) + "\n"
	if err := os.WriteFile(core, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	runner := &SSHRunner{Host: "fixture", AgentDeckPath: core}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	start := time.Now()
	err = runner.RunReadIO(ctx, inR, &out, io.Discard, []byte("unsupported\n"), true, "recall", "follow", "id", "--after", "bad-cursor", "--jsonl")
	if err != nil || out.String() != payload || time.Since(start) > 2*time.Second {
		t.Fatalf("natural follow end err=%v payload=%q elapsed=%s while caller input remained open", err, out.String(), time.Since(start))
	}
}
