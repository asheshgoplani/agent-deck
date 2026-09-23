package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSSHChannelExhaustion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"sshd no more sessions", "channel 0: open failed: no more sessions", true},
		{"mux client refused", "mux_client_request_session: session request failed: Session open refused by peer", true},
		{"direct channel refused", "channel 1: open failed: connect failed: open failed", true},
		{"forwarding denial is not session exhaustion", "administratively prohibited: port forwarding not permitted", false},
		{"ordinary remote failure", "Error: path does not exist", false},
		{"timeout", "ssh: connect to host example.com port 22: Connection timed out", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSSHChannelExhaustion(tc.in); got != tc.want {
				t.Fatalf("isSSHChannelExhaustion(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// writeFakeSSH installs a fake `ssh` on PATH that fails the first (shared)
// attempt with a channel-open refusal and succeeds on the dedicated retry.
// It records every invocation's argv to a log file.
func writeFakeSSH(t *testing.T, dir, logPath string) {
	t.Helper()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*)
    printf '[]'
    exit 0
    ;;
esac
printf 'channel 0: open failed: no more sessions\n' >&2
exit 255
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunExecRetriesOnChannelExhaustion(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	writeFakeSSH(t, dir, logPath)

	r := &SSHRunner{Host: "host-a.example.com"}
	out, err := r.runExec(context.Background(), "agent-deck list --json", true)
	if err != nil {
		t.Fatalf("runExec returned error: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "[]" {
		t.Fatalf("runExec output = %q, want []", got)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(calls) != 2 {
		t.Fatalf("expected 2 ssh calls (shared then dedicated), got %d: %q", len(calls), calls)
	}
	if strings.Contains(calls[0], "ControlPath=none") {
		t.Fatalf("first call should use the shared master: %q", calls[0])
	}
	if !strings.Contains(calls[1], "ControlPath=none") {
		t.Fatalf("retry should bypass the master with ControlPath=none: %q", calls[1])
	}
}

func TestRunExecDoesNotRetryOrdinaryFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
printf 'Error: path does not exist\n' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := &SSHRunner{Host: "host-a.example.com"}
	if _, err := r.runExec(context.Background(), "agent-deck list --json", true); err == nil {
		t.Fatal("expected an error for an ordinary remote failure")
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(log), "\n"); calls != 1 {
		t.Fatalf("ordinary failure must not retry, got %d ssh calls", calls)
	}
}

func TestRunExecDoesNotRetryMutatingVerb(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	writeFakeSSH(t, dir, logPath)

	r := &SSHRunner{Host: "host-a.example.com"}
	if _, err := r.runExec(context.Background(), "agent-deck session restart s1", false); err == nil {
		t.Fatal("expected an error from the refused channel")
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(log), "\n"); calls != 1 {
		t.Fatalf("mutating verb must not retry, got %d ssh calls", calls)
	}
}

func TestRunExecReportsDedicatedAttemptFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*)
    printf 'dedicated attempt failed\n' >&2
    exit 7
    ;;
esac
printf 'channel 0: open failed: no more sessions\n' >&2
exit 255
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := &SSHRunner{Host: "host-a.example.com"}
	_, err := r.runExec(context.Background(), "agent-deck list --json", true)
	if err == nil {
		t.Fatal("expected an error when both attempts fail")
	}
	if !strings.Contains(err.Error(), "dedicated attempt failed") {
		t.Fatalf("error should report the dedicated attempt, got: %v", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if calls := strings.Count(string(log), "\n"); calls != 2 {
		t.Fatalf("expected a shared then a dedicated attempt, got %d ssh calls", calls)
	}
}
