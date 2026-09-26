package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shim emits the client-side text from an OpenSSH mux session refusal.
// Its dedicated branch stands in for a second TCP connection to the same host.
func TestSSHReadOnlyMuxRefusalRecovery2355(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*) printf '[{"id":"last-good"}]'; exit 0 ;;
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
	started := time.Now()
	out, err := r.run(context.Background(), "list", "--json")
	if err != nil || string(out) != `[{"id":"last-good"}]` {
		t.Fatalf("read-only poll: stdout=%q err=%v", out, err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "\n") != 2 || !strings.Contains(string(calls), "ControlPath=none") {
		t.Fatalf("recovery branch not entered: calls=%q", calls)
	}
	t.Logf("mux refusal stderr=%q elapsed=%s classification=ok calls=%d", "mux_client_request_session: session request failed: Session open refused by peer", time.Since(started), strings.Count(string(calls), "\n"))
}

func TestSSHReadOnlyMuxRefusalBeforeFallbackTimeout2355(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*) printf '[]'; exit 0 ;;
esac
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
sleep 1
exit 255
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &SSHRunner{Host: "fixture.example"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	out, err := r.run(ctx, "list", "--json")
	if err != nil || string(out) != "[]" {
		t.Fatalf("refusal before stalled fallback: stdout=%q err=%v elapsed=%s", out, err, time.Since(started))
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "\n") != 2 {
		t.Fatalf("wanted shared and dedicated calls, got %q", calls)
	}
	t.Logf("mux refusal before fallback timeout: stderr=%q elapsed=%s classification=ok", "mux_client_request_session: session request failed: Session open refused by peer", time.Since(started))
}

func TestSSHReadOnlyKeepsSuccessfulSharedFallback2355(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*) printf 'dedicated failed\n' >&2; exit 7 ;;
esac
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
sleep 0.05
printf '[{"id":"shared-success"}]'
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &SSHRunner{Host: "fixture.example"}
	out, err := r.run(context.Background(), "list", "--json")
	if err != nil || string(out) != `[{"id":"shared-success"}]` {
		t.Fatalf("successful shared fallback must win: stdout=%q err=%v", out, err)
	}
}
