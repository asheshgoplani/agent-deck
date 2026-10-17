package session

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// RunIO forwards the CLI's streams without buffering or combining diagnostics
// with JSON output. Unlike background status probes, an explicit CLI command
// may legitimately run longer than the probe timeout (for example send --wait).
func (r *SSHRunner) RunIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if err := ValidateSSHHost(r.Host); err != nil {
		return err
	}
	if err := os.MkdirAll(sshControlDir, 0700); err != nil {
		return err
	}
	CleanStaleSSHSockets()
	sshArgs := r.sshChannelArgs(r.buildRemoteCommand(args...))
	// #nosec G204 -- Literal ssh executable; fixed connection options and a
	// validated non-option host. Remote executable/profile/arguments are each
	// shell-quoted by buildRemoteCommand, with no local shell evaluation.
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

// RunFollowIO owns one dedicated connection for the stream, with no retries
// (replaying frames needs an explicit cursor chosen by the consumer). SSH's
// connection timeout and keepalives bound broken connections. The watchdog
// makes stdin EOF a remote cancellation even on cores predating EOF handling.
func (r *SSHRunner) RunFollowIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if err := ValidateSSHHost(r.Host); err != nil {
		return err
	}
	remoteCmd := followRemoteCommand(r.buildRemoteCommand(args...))
	sshArgs := append([]string{"-T", "-o", "ControlPath=none", "-o", "ControlMaster=no", "-o", "ControlPersist=no"}, r.sshChannelArgs(remoteCmd)...)
	// #nosec G204 -- Literal ssh; validated host and individually quoted remote argv.
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if stdin != nil {
		// Force an owned pipe instead of inheriting the controller stdin FD.
		// Even SIGKILL then closes the SSH input when the controller dies.
		owned, closeOwned, err := ownedSSHInput(stdin)
		if err != nil {
			return err
		}
		defer closeOwned()
		cmd.Stdin = owned
	}
	cmd.WaitDelay = 3 * time.Second
	return cmd.Run()
}

// Both child PIDs belong to this shell invocation. Cleanup terminates only
// those children, reaps them, and also runs on remote HUP/TERM or stdin EOF.
// exec replaces the command subshell so child is the core PID, not an
// intermediate shell that could strand it when a consumer switches away.
func followRemoteCommand(command string) string {
	command = strings.TrimPrefix(command, "exec ")
	script := `child= watcher=
cleanup() {
 trap '' HUP INT TERM USR1
 if [ -n "$watcher" ]; then kill "$watcher" 2>/dev/null || :; wait "$watcher" 2>/dev/null || :; fi
 if [ -n "$child" ]; then
  kill -TERM "$child" 2>/dev/null || :
  sleep 1
  kill -KILL "$child" 2>/dev/null || :
  wait "$child" 2>/dev/null || :
 fi
}
trap 'cleanup; exit 0' HUP INT TERM USR1
exec 3<&0
(exec ` + command + `) </dev/null 3<&- &
child=$!
owner=$$
(while IFS= read -r line; do :; done; kill -USR1 "$owner" 2>/dev/null || :) <&3 3<&- &
watcher=$!
exec 3<&-
wait "$child"
result=$?
child=
cleanup
exit "$result"`
	return "exec sh -c " + shellQuote(script)
}

const readReady = "\x1eagent-deck-ready\n"

// readReadyWriter consumes only the fixed transport handshake. Payload bytes
// go straight to the caller; no conversation or diagnostic is accumulated.
type readReadyWriter struct {
	out     io.Writer
	prefix  []byte
	ready   func()
	decided bool
}

func (w *readReadyWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.decided {
		return w.out.Write(p)
	}
	for len(p) > 0 && !w.decided {
		w.prefix = append(w.prefix, p[0])
		p = p[1:]
		if !bytes.HasPrefix([]byte(readReady), w.prefix) {
			w.decided = true
			if _, err := w.out.Write(w.prefix); err != nil {
				return 0, err
			}
		} else if len(w.prefix) == len(readReady) {
			w.decided = true
			w.ready()
		}
	}
	if w.decided {
		w.prefix = nil
		if len(p) > 0 {
			if _, err := w.out.Write(p); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

// RunReadIO negotiates flags on the owner host in the same SSH round trip as
// the read. Help output is discarded there. The handshake bounds negotiation
// to 20 seconds, then the caller's snapshot deadline or stream lifetime owns
// the channel. A stream never retries, so cursors and duplicates remain under
// the consumer's control.
func (r *SSHRunner) RunReadIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, unsupported []byte, follow bool, args ...string) error {
	if err := ValidateSSHHost(r.Host); err != nil {
		return err
	}
	help := false
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			help = true
		}
	}
	command := r.buildRemoteCommand(args...)
	if !help {
		helpArgs := append(append([]string(nil), args...), "--help")
		gate := `probe=
trap 'if [ -n "$probe" ]; then kill -KILL "$probe" 2>/dev/null || :; wait "$probe" 2>/dev/null || :; fi; exit 0' HUP INT TERM
` + r.buildRemoteCommand(helpArgs...) + ` </dev/null >/dev/null 2>&1 &
probe=$!
wait "$probe"
result=$?
probe=
if [ "$result" -ne 0 ]; then printf '%s' ` + shellQuote(string(unsupported)) + `; exit 1; fi
printf '%s' ` + shellQuote(readReady) + `
exec ` + command
		command = "exec sh -c " + shellQuote(gate)
	}
	if follow && !help {
		command = followRemoteCommand(command)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	timer := time.AfterFunc(20*time.Second, func() { once.Do(cancel) })
	defer timer.Stop()
	ready := func() { once.Do(func() { timer.Stop() }) }
	var writer *readReadyWriter
	if !help {
		writer = &readReadyWriter{out: stdout, ready: ready}
		stdout = writer
	} else {
		ready()
	}
	sshArgs := append([]string{"-T", "-o", "ControlPath=none", "-o", "ControlMaster=no", "-o", "ControlPersist=no"}, r.sshChannelArgs(command)...)
	// #nosec G204 -- Literal ssh and validated destination; all remote values shell quoted.
	cmd := exec.CommandContext(streamCtx, "ssh", sshArgs...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Finite reads and help never consume stdin. Leaving it nil also avoids
	// waiting on an input copier after a successful response with caller input open.
	if follow && !help && stdin != nil {
		// Force an owned pipe instead of inheriting the controller stdin FD.
		// Even SIGKILL then closes the SSH input when the controller dies.
		owned, closeOwned, err := ownedSSHInput(stdin)
		if err != nil {
			return err
		}
		defer closeOwned()
		cmd.Stdin = owned
	}
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	if writer != nil && len(writer.prefix) > 0 {
		_, _ = writer.out.Write(writer.prefix)
	}
	if streamCtx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("remote read capability negotiation timed out")
	}
	return err
}

// ownedSSHInput owns both ends of the SSH input pipe, never the caller's
// reader. This makes controller death close SSH stdin, while a naturally
// finished stream can return without os/exec waiting on a borrowed reader.
// A generic reader cannot be interrupted without closing it: its copy
// goroutine can remain blocked until the caller closes it or the CLI exits.
func ownedSSHInput(input io.Reader) (*os.File, func(), error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	go func() {
		defer writer.Close()
		_, _ = io.Copy(writer, input)
	}()
	return reader, func() { _ = reader.Close(); _ = writer.Close() }, nil
}
