package session

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The shim runs this helper before the remote command. It observes actual
// sockets at the SSH process boundary, rather than checking source text.
func TestRunIOSocketProcessHelper(t *testing.T) {
	stale := os.Getenv("RUNIO_STALE_SOCKET")
	if stale == "" {
		return
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale socket still present before ssh: %v", err)
	}
	conn, err := net.DialTimeout("unix", os.Getenv("RUNIO_LIVE_SOCKET"), time.Second)
	if err != nil {
		t.Fatalf("live socket unavailable before ssh: %v", err)
	}
	conn.Close()
	if b, err := os.ReadFile(os.Getenv("RUNIO_REGULAR_FILE")); err != nil || string(b) != "keep" {
		t.Fatalf("regular file changed: %q %v", b, err)
	}
}

func TestRunIOCleanupBeforeSSHAndLiteralStreams(t *testing.T) {
	if err := os.MkdirAll(sshControlDir, 0700); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(sshControlDir, fmt.Sprintf("runio-%d", time.Now().UnixNano()))
	stale, live, regular := prefix+"-stale", prefix+"-live", prefix+"-file"
	recreateOrphanSocket(t, stale)
	t.Cleanup(func() { _ = os.Remove(stale); _ = os.Remove(regular) })
	listener, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-drained })
	if err := os.WriteFile(regular, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argvPath, invocationPath := filepath.Join(dir, "argv"), filepath.Join(dir, "invocations")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNIO_TEST_BINARY", binary)
	t.Setenv("RUNIO_STALE_SOCKET", stale)
	t.Setenv("RUNIO_LIVE_SOCKET", live)
	t.Setenv("RUNIO_REGULAR_FILE", regular)
	t.Setenv("RUNIO_ARGV", argvPath)
	t.Setenv("RUNIO_INVOCATIONS", invocationPath)
	t.Setenv("RUNIO_HELPER_LOG", filepath.Join(dir, "helper.log"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "ssh"), `#!/bin/sh
printf 'called\n' >> "$RUNIO_INVOCATIONS"
"$RUNIO_TEST_BINARY" -test.run '^TestRunIOSocketProcessHelper$' > "$RUNIO_HELPER_LOG" 2>&1 || { cat "$RUNIO_HELPER_LOG" >&2; exit 91; }
for arg do command=$arg; done
exec /bin/sh -c "$command"
`)
	remoteBinary := filepath.Join(dir, "deck 'quoted'")
	write(remoteBinary, `#!/bin/sh
printf '%s\000' "$@" > "$RUNIO_ARGV"
cat
printf 'remote diagnostic\n' >&2
`)
	runner := &SSHRunner{Host: "fixture-host", AgentDeckPath: remoteBinary, Profile: "profile 'quoted'"}
	args := []string{"session", "send", "two words", "quote' newline\n;$(printf SHOULD_NOT_RUN)", "--message-file", "-"}
	input := "Unicode 日本語\n" + strings.Repeat("message ", 512)
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.RunIO(ctx, strings.NewReader(input), &stdout, &stderr, args...); err != nil {
		t.Fatalf("RunIO: %v stderr=%s", err, &stderr)
	}
	if stdout.String() != input || stderr.String() != "remote diagnostic\n" {
		t.Fatalf("streams changed: stdout=%q stderr=%q", &stdout, &stderr)
	}
	b, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	want := append([]string{"-p", runner.Profile}, args...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remote argv=%q want=%q", got, want)
	}
	if b, err := os.ReadFile(invocationPath); err != nil || string(b) != "called\n" {
		t.Fatalf("SSH invocations=%q err=%v", b, err)
	}
	runner.Host = "-oProxyCommand=printf unexpected"
	if err := runner.RunIO(ctx, nil, &stdout, &stderr, "list"); err == nil {
		t.Fatal("option-shaped SSH host accepted")
	}
	if b, err := os.ReadFile(invocationPath); err != nil || string(b) != "called\n" {
		t.Fatalf("invalid host invoked SSH: %q err=%v", b, err)
	}
}
