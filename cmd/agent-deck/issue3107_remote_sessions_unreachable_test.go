package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeSSHDir returns a PATH entry holding an `ssh` stub. A command line that
// mentions "dead@" fails like an unresolvable host (exit 255); anything else
// answers `list --json` with one session.
func fakeSSHDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *dead@*) echo 'ssh: Could not resolve hostname dead.invalid' >&2; exit 255;;\n" +
		"esac\n" +
		"echo '[{\"id\":\"abc12345\",\"title\":\"alive-session\",\"status\":\"idle\"}]'\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestIssue3107_RemoteSessionsUnreachable pins that an unreachable remote is
// never reported as "no sessions": non-zero exit and a stderr message in bare
// --json and text mode, while stdout in --json stays a valid bare array.
func TestIssue3107_RemoteSessionsUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	env := []string{"PATH=" + fakeSSHDir(t) + string(os.PathListSeparator) + os.Getenv("PATH")}
	cfg := "[remotes.dead]\nhost = 'dead@dead.invalid'\n[remotes.live]\nhost = 'ok@live.example'\n"

	run := func(t *testing.T, args ...string) (string, string, int) {
		t.Helper()
		home := tempHomeWithConfig(t, cfg)
		return runAgentDeckEnv(t, home, "", env, append([]string{"remote", "sessions"}, args...)...)
	}
	decodeArray := func(t *testing.T, stdout string) []session.RemoteSessionInfo {
		t.Helper()
		var arr []session.RemoteSessionInfo
		if err := json.Unmarshal([]byte(stdout), &arr); err != nil {
			t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
		}
		return arr
	}

	t.Run("named dead remote, bare json", func(t *testing.T) {
		stdout, stderr, code := run(t, "dead", "--json")
		if code == 0 {
			t.Fatalf("exit = 0, want non-zero\nstdout=%s", stdout)
		}
		if !strings.Contains(stderr, "[dead]") {
			t.Fatalf("stderr = %q, want a message naming the remote", stderr)
		}
		if got := decodeArray(t, stdout); len(got) != 0 {
			t.Fatalf("sessions = %#v, want none", got)
		}
	})

	t.Run("named dead remote, text", func(t *testing.T) {
		_, _, code := run(t, "dead")
		if code == 0 {
			t.Fatal("text mode exit = 0, want non-zero")
		}
	})

	t.Run("all remotes, partial failure, bare json", func(t *testing.T) {
		stdout, stderr, code := run(t, "--json")
		if code == 0 {
			t.Fatalf("exit = 0, want non-zero on partial failure\nstdout=%s", stdout)
		}
		if !strings.Contains(stderr, "[dead]") {
			t.Fatalf("stderr = %q, want a message naming dead", stderr)
		}
		got := decodeArray(t, stdout)
		if len(got) != 1 || got[0].Title != "alive-session" {
			t.Fatalf("stdout sessions = %#v, want the one alive-session", got)
		}
	})

	t.Run("all remotes, partial failure, text", func(t *testing.T) {
		stdout, _, code := run(t)
		if code == 0 {
			t.Fatal("text mode exit = 0, want non-zero when any remote failed")
		}
		if !strings.Contains(stdout, "alive-session") {
			t.Fatalf("text output lost the reachable session: %q", stdout)
		}
	})

	t.Run("reachable remote unchanged", func(t *testing.T) {
		stdout, stderr, code := run(t, "live", "--json")
		if code != 0 || strings.TrimSpace(stderr) != "" {
			t.Fatalf("success path changed: code=%d stderr=%q", code, stderr)
		}
		if got := decodeArray(t, stdout); len(got) != 1 || got[0].Title != "alive-session" {
			t.Fatalf("sessions = %#v", got)
		}
	})
}
