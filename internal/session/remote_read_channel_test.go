package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSSHOneLinePerCall puts an ssh on PATH that logs each invocation on one
// line (forwarded reads send multi-line shell scripts) and answers an empty
// listing.
func fakeSSHOneLinePerCall(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s' \"$*\" | tr '\\n' ' ' >> \"$SSH_CALL_LOG\"\nprintf '\\n' >> \"$SSH_CALL_LOG\"\nprintf '[]'\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_CALL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
}

// Forwarded remote reads (recall timeline/follow, events follow,
// session send-status) own exactly one dedicated ssh channel. They must
// never dial the persistent remote-agent channel, whether the process is a
// one-shot CLI call (#2492) or a long-lived process that allows channels.
func TestRemoteReadsNeverDialPersistentChannel(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		calls := fakeSSHOneLinePerCall(t)
		setRemoteChannelsAllowed(t, allowed)
		reads := []struct {
			follow bool
			args   []string
		}{
			{false, []string{"recall", "timeline", "id", "--json", "--tail", "10"}},
			{false, []string{"session", "send-status", "send-id", "--json"}},
			{true, []string{"recall", "follow", "id", "--after", "end", "--jsonl"}},
			{true, []string{"events", "follow", "--jsonl", "--since", "1"}},
		}
		for _, read := range reads {
			r := NewSSHRunner("dev", RemoteConfig{Host: "user@dev.example"})
			r.cleanChannelSocketsFn = func() {}
			var out, diag bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := r.RunReadIO(ctx, strings.NewReader(""), &out, &diag, []byte("unsupported\n"), read.follow, read.args...)
			cancel()
			if err != nil {
				t.Fatalf("allowed=%v %q: %v (%s)", allowed, read.args, err, diag.String())
			}
		}
		time.Sleep(150 * time.Millisecond)
		got := calls()
		if len(got) != len(reads) {
			t.Fatalf("allowed=%v: %d ssh spawns for %d reads: %q", allowed, len(got), len(reads), got)
		}
		for _, call := range got {
			if strings.Contains(call, "remote-agent") || !strings.Contains(call, "ControlPath=none") {
				t.Fatalf("allowed=%v: read used the persistent channel: %q", allowed, call)
			}
		}
		CloseRemoteChannels()
	}
}
