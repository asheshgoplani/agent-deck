package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeOwnerHost wires a registered remote "lab" to a local fake: an `ssh`
// shim first on PATH runs the remote command string with sh (stdin passed
// through, like a real channel), and the remote agent-deck is a script that
// answers the forwarded reads. With old=true it rejects the new verbs and
// flags the way an older core does. It returns the ssh call log path and the
// file the streaming fake writes its own PID to.
func fakeOwnerHost(t *testing.T, home string, old bool) (callLog, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	callLog = filepath.Join(dir, "calls.log")
	pidFile = filepath.Join(dir, "follow.pid")
	ssh := `#!/bin/sh
for cmd; do :; done
printf '%s' "$cmd" | tr '\n' ' ' >> '` + callLog + `'
printf '\n' >> '` + callLog + `'
exec sh -c "$cmd"
`
	reject := ""
	if old {
		reject = `echo "flag provided but not defined" >&2; exit 2`
	}
	core := `#!/bin/sh
for a; do case "$a" in --help|-h) ` + func() string {
		if old {
			return reject
		}
		return `echo "Usage of $1 $2"; exit 0`
	}() + ` ;; esac; done
case "$1" in
  version) echo "Agent Deck v1.16.27"; exit 0 ;;
  list) printf '[{"id":"r1","title":"remote one","tool":"claude","status":"idle","claude_session_id":"c1","transcript_path":"/owner/projects/c1.jsonl"}]\n'; exit 0 ;;
esac
case "$1 $2" in
  "recall timeline") printf '{"schema":"agent-deck.recall.rows/v2","turns":[],"before_cursor":"older"}\n' ;;
  "session send-status") printf '{"send_id":"%s","state":"delivered"}\n' "$3" ;;
  "recall follow"|"events follow")
    echo $$ > '` + pidFile + `'
    i=0
    while :; do i=$((i+1)); printf '{"cursor":%d}\n' "$i"; sleep 0.2; done ;;
  *) echo "unexpected remote command: $*" >&2; exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	corePath := filepath.Join(dir, "remote-agent-deck")
	if err := os.WriteFile(corePath, []byte(core), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.lab]\nhost = 'lab-host'\nagent_deck_path = '"+corePath+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return callLog, pidFile
}

func sshCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// CORE-CHANGES 1, 2 and 4: the controller forwards the conversation
// snapshot (with --before paging) and send-status to the owner host in one
// ssh round trip and prints the owner's output unchanged.
func TestRemoteReadForwardingCLI(t *testing.T) {
	home := t.TempDir()
	callLog, _ := fakeOwnerHost(t, home, false)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"recall", "timeline", "sess", "--json", "--tail", "100"}, `"before_cursor":"older"`},
		{[]string{"recall", "timeline", "sess", "--json", "--before", "c", "--limit", "50"}, `"schema":"agent-deck.recall.rows/v2"`},
		{[]string{"session", "send-status", "send-1", "--json"}, `{"send_id":"send-1","state":"delivered"}`},
	} {
		before := len(sshCalls(t, callLog))
		stdout, stderr, code := runAgentDeck(t, home, append([]string{"remote", "lab"}, tc.args...)...)
		if code != 0 || !strings.Contains(stdout, tc.want) {
			t.Fatalf("%q: exit %d\nstdout: %s\nstderr: %s", tc.args, code, stdout, stderr)
		}
		if strings.Contains(stdout, "agent-deck-ready") {
			t.Fatalf("%q: transport handshake leaked: %q", tc.args, stdout)
		}
		if calls := sshCalls(t, callLog)[before:]; len(calls) != 1 {
			t.Fatalf("%q: want one ssh round trip, got %d: %q", tc.args, len(calls), calls)
		}
	}
}

// An owner host whose core predates a forwarded read answers with one JSON
// object naming the remote, exit 1, so clients fall back. Stdin stays open,
// as it does for a live client: closing stdin is how a follow is ended.
func TestRemoteReadOlderOwnerHostRefusal(t *testing.T) {
	home := t.TempDir()
	fakeOwnerHost(t, home, true)
	for _, args := range [][]string{
		{"recall", "timeline", "sess", "--json", "--before", "c", "--limit", "5"},
		{"session", "send-status", "send-1", "--json"},
		{"events", "follow", "--jsonl", "--since", "3"},
		{"recall", "follow", "sess", "--after", "end", "--jsonl"},
	} {
		cmd := exec.Command(channelsCLIBinary(t), append([]string{"remote", "lab"}, args...)...)
		cmd.Env = agentDeckTestEnv(home, nil)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var runErr error
		select {
		case runErr = <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%q: no answer from an older owner host", args)
		}
		_ = stdin.Close()
		code := 0
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		var got struct{ Error, Remote string }
		if code != 1 || json.Unmarshal([]byte(stdout.String()), &got) != nil || got.Remote != "lab" || !strings.Contains(got.Error, "unsupported remote command") {
			t.Fatalf("%q: exit %d (%v)\nstdout: %s\nstderr: %s", args, code, runErr, stdout.String(), stderr.String())
		}
	}
}

// CORE-CHANGES 1 and 3: a follow streams frames as they arrive and ends
// cleanly when the controller's stdin closes; the owner host's core is
// terminated and reaped, so switching away never strands a remote reader.
func TestRemoteFollowEndsOnStdinClose(t *testing.T) {
	for _, args := range [][]string{
		{"recall", "follow", "sess", "--after", "end", "--jsonl", "--status"},
		{"events", "follow", "--jsonl", "--since", "3", "--kind", "session.status"},
	} {
		t.Run(args[0], func(t *testing.T) {
			home := t.TempDir()
			_, pidFile := fakeOwnerHost(t, home, false)
			cmd := exec.Command(channelsCLIBinary(t), append([]string{"remote", "lab"}, args...)...)
			cmd.Env = agentDeckTestEnv(home, nil)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			lines := bufio.NewScanner(stdout)
			for i := 0; i < 2; i++ {
				if !lines.Scan() || !strings.HasPrefix(lines.Text(), `{"cursor":`) {
					_ = cmd.Process.Kill()
					t.Fatalf("frame %d: %q (%v) stderr: %s", i, lines.Text(), lines.Err(), stderr.String())
				}
			}
			go func() {
				for lines.Scan() {
				}
				done <- cmd.Wait()
			}()
			_ = stdin.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("follow did not end cleanly: %v stderr: %s", err, stderr.String())
				}
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("follow kept running after stdin closed; stderr: %s", stderr.String())
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for syscall.Kill(pid, 0) == nil {
				if time.Now().After(deadline) {
					t.Fatalf("owner-host reader %d still alive after the follow ended", pid)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

// CORE-CHANGES 5: remote sessions --json keeps the owner host's transcript
// location and native id, so a remote open needs no lookup round trip.
func TestRemoteSessionsTranscriptPathCLI(t *testing.T) {
	home := t.TempDir()
	fakeOwnerHost(t, home, false)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "sessions", "lab", "--json")
	var rows []map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &rows) != nil || len(rows) != 1 {
		t.Fatalf("remote sessions: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if rows[0]["transcript_path"] != "/owner/projects/c1.jsonl" || rows[0]["claude_session_id"] != "c1" {
		t.Fatalf("remote row lost transcript metadata: %v", rows[0])
	}
}
