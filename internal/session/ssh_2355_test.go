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

// Field check on a saturated master: OpenSSH's own direct fallback answered in
// a few seconds, and racing it with a dedicated link only added logins and
// latency. A fallback that lands inside the grace must be the only connection.
func TestSSHReadOnlyHealthyMuxFallbackOpensNoDedicatedLink2355(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*) printf '[{"id":"dedicated"}]'; exit 0 ;;
esac
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
sleep 0.3
printf '[{"id":"shared-fallback"}]'
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &SSHRunner{Host: "fixture.example"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := r.run(ctx, "list", "--json")
	if err != nil || string(out) != `[{"id":"shared-fallback"}]` {
		t.Fatalf("healthy fallback: stdout=%q err=%v", out, err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(calls), "\n"); n != 1 || strings.Contains(string(calls), "ControlPath=none") {
		t.Fatalf("healthy fallback must not open a dedicated link: calls=%q", calls)
	}
}

func TestMuxFallbackGraceLeavesHalfTheDeadline2355(t *testing.T) {
	if got := muxFallbackGrace(context.Background()); got != sshMuxFallbackGrace {
		t.Fatalf("no deadline: grace=%s, want %s", got, sshMuxFallbackGrace)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got := muxFallbackGrace(ctx); got <= 0 || got > time.Second {
		t.Fatalf("2s deadline: grace=%s, want at most half", got)
	}
}

// Mixed versions: a new controller behind a saturated master polling a remote
// that predates --stats (v1.16.13 rejects the flag with Go's usage text). The
// refusal retry must hand the old remote's rejection to the existing stats
// fallback, learn the capability once, and never loop.
func TestSSHReadOnlyMuxRefusalWithOldRemoteBinary2355(t *testing.T) {
	setupSessionXDGPathEnv(t)
	t.Setenv("AGENT_DECK_REMOTE_CHANNEL", "0")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *ControlPath=none*--stats*)
    printf 'flag provided but not defined: -stats\nUsage of list:\n  -all\n' >&2
    exit 2 ;;
  *ControlPath=none*)
    printf '[{"id":"s1","title":"t","status":"waiting"}]'
    exit 0 ;;
esac
printf 'mux_client_request_session: session request failed: Session open refused by peer\n' >&2
exit 255
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &SSHRunner{Host: "fixture.example", name: "old-remote", lastStderr: &lastStderrBox{}}

	countCalls := func() int {
		calls, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(calls), "\n")
	}
	sessions, stats, err := r.FetchSessions(context.Background())
	if err != nil || len(sessions) != 1 || stats != nil {
		t.Fatalf("first poll: sessions=%v stats=%+v err=%v", sessions, stats, err)
	}
	// --stats: shared refused + dedicated rejected; plain: shared refused + dedicated ok.
	if n := countCalls(); n != 4 {
		t.Fatalf("first poll made %d ssh calls, want 4", n)
	}
	if state, ok := LoadRemoteVersions()["old-remote"]; !ok || state.StatsSupported == nil || *state.StatsSupported {
		t.Fatalf("stats capability not learned: %+v", state)
	}
	if _, _, err := r.FetchSessions(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if n := countCalls(); n != 6 {
		t.Fatalf("second poll must skip --stats: total calls=%d, want 6", n)
	}
}
