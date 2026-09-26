package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsSSHChannelExhaustion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"sshd log text alone", "channel 0: open failed: no more sessions", false},
		{"mux client refused", "mux_client_request_session: session request failed: Session open refused by peer", true},
		{"direct channel failure", "channel 1: open failed: connect failed: open failed", false},
		{"unrelated open failure", "channel 2: open failed: administratively prohibited: open failed", false},
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
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
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
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
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

func TestRunExecLeavesUnprovenFailuresUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, status string
	}{
		{"timeout", "ssh: connect to host fixture port 22: Connection timed out", "timeout"},
		{"unrelated channel", "channel 2: open failed: administratively prohibited: open failed", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "calls")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SSH_CALL_LOG\"\nprintf '%s\\n' \"$SSH_FAILURE\" >&2\nexit 255\n"
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SSH_CALL_LOG", logPath)
			t.Setenv("SSH_FAILURE", tc.stderr)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			r := &SSHRunner{Host: "fixture.example"}
			_, err := r.runExec(context.Background(), "agent-deck list --json", true)
			if err == nil || !strings.Contains(err.Error(), tc.stderr) {
				t.Fatalf("original failure lost: %v", err)
			}
			if status, _ := remotePollError(err); status != tc.status {
				t.Fatalf("classification=%q, want %q", status, tc.status)
			}
			calls, readErr := os.ReadFile(logPath)
			if readErr != nil || strings.Count(string(calls), "\n") != 1 {
				t.Fatalf("unproven failure must not retry: calls=%q readErr=%v", calls, readErr)
			}
		})
	}
}

func TestRunExecDedicatedAttemptUsesOriginalDeadlineAndOptions(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*) sleep 1; printf '[]'; exit 0 ;;
esac
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
exit 255
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &SSHRunner{Host: "fixture.example"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := r.runExec(ctx, "agent-deck list --json", true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("dedicated attempt exceeded original deadline: %s", elapsed)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "ControlPath=none") || !strings.Contains(lines[1], "BatchMode=yes") || !strings.Contains(lines[1], "ConnectTimeout=10") {
		t.Fatalf("dedicated args lost options: %q", calls)
	}
}
