package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #2481: a one-shot CLI call (`agent-deck remote sessions <host>`, run
// by pollers such as a desktop app every minute or two per remote) opened the persistent
// remote-agent channel in the background next to its own ssh exec, then
// exited. Every call cost two remote ssh sessions and a remote agent-deck
// process that never served a request. Only long-lived processes (TUI, web
// server) keep the channel.

// fakeSSHSpawnLog puts an ssh on PATH that records each invocation. Plain
// commands answer an empty listing; a remote-agent dial stays alive reading
// stdin like a real agent waiting for requests, so the test sees it as soon
// as it is spawned.
func fakeSSHSpawnLog(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_CALL_LOG"
case "$*" in
  *remote-agent*) exec cat >/dev/null ;;
esac
printf '[]'
`
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

// setRemoteChannelsAllowed sets the process opt-in for the test's duration and
// tears every channel down afterwards (as a process exit would).
func setRemoteChannelsAllowed(t *testing.T, allowed bool) {
	t.Helper()
	prev := remoteChannelsAllowed.Load()
	remoteChannelsAllowed.Store(allowed)
	t.Cleanup(func() {
		CloseRemoteChannels()
		remoteChannelsAllowed.Store(prev)
	})
}

// oneShotRemoteCalls replays the measured poller pattern: each call is a
// fresh CLI process (CloseRemoteChannels stands in for its exit) running one
// read-only listing against one remote. It returns every ssh spawn seen.
func oneShotRemoteCalls(t *testing.T, calls func() []string, remotes []string, rounds int) []string {
	t.Helper()
	for round := 0; round < rounds; round++ {
		for _, name := range remotes {
			r := NewSSHRunner(name, RemoteConfig{Host: "user@" + name + ".example"})
			r.cleanChannelSocketsFn = func() {}
			if _, err := r.Run(context.Background(), "group", "list", "--json"); err != nil {
				t.Fatalf("remote %s: %v", name, err)
			}
			// A background dial starts within microseconds of the request;
			// give it ample time to show up before the "process" exits.
			time.Sleep(150 * time.Millisecond)
			CloseRemoteChannels()
		}
	}
	return calls()
}

func countRemoteAgentDials(spawns []string) int {
	n := 0
	for _, s := range spawns {
		if strings.Contains(s, "remote-agent") {
			n++
		}
	}
	return n
}

func TestOneShotRemoteCommandOpensNoPersistentChannel2481(t *testing.T) {
	calls := fakeSSHSpawnLog(t)
	t.Setenv("AGENT_DECK_REMOTE_CHANNEL", "")
	os.Unsetenv("AGENT_DECK_REMOTE_CHANNEL")
	setRemoteChannelsAllowed(t, false) // a CLI process never opts in

	remotes := []string{"r1", "r2", "r3", "r4"}
	const rounds = 2
	spawns := oneShotRemoteCalls(t, calls, remotes, rounds)
	want := len(remotes) * rounds
	dials := countRemoteAgentDials(spawns)
	t.Logf("one-shot calls=%d ssh spawns=%d remote-agent dials=%d", want, len(spawns), dials)
	if dials != 0 {
		t.Fatalf("one-shot CLI calls dialled the persistent channel %d times (spawns=%q)", dials, spawns)
	}
	if len(spawns) != want {
		t.Fatalf("one-shot CLI calls made %d ssh spawns, want exactly one per call (%d): %q", len(spawns), want, spawns)
	}
}

func TestLongLivedProcessKeepsPersistentChannel2481(t *testing.T) {
	calls := fakeSSHSpawnLog(t)
	t.Setenv("AGENT_DECK_REMOTE_CHANNEL", "")
	os.Unsetenv("AGENT_DECK_REMOTE_CHANNEL")
	setRemoteChannelsAllowed(t, false)
	EnableRemoteChannels() // what the TUI and web server do at start

	r := NewSSHRunner("tui-remote", RemoteConfig{Host: "user@tui.example"})
	r.cleanChannelSocketsFn = func() {}
	if _, err := r.Run(context.Background(), "group", "list", "--json"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for countRemoteAgentDials(calls()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("long-lived process never dialled the channel: %q", calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRemoteChannelEnvOverridesProcessDefault2481(t *testing.T) {
	cases := []struct {
		env     string // "" behaves as unset
		allowed bool
		want    bool
	}{
		{env: "", allowed: false, want: false},
		{env: "", allowed: true, want: true},
		{env: "1", allowed: false, want: true},
		{env: "true", allowed: false, want: true},
		{env: "0", allowed: true, want: false},
		{env: "false", allowed: true, want: false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("env=%q/allowed=%v", tc.env, tc.allowed), func(t *testing.T) {
			t.Setenv("AGENT_DECK_REMOTE_CHANNEL", tc.env)
			setRemoteChannelsAllowed(t, tc.allowed)
			if got := remoteChannelsEnabled(); got != tc.want {
				t.Errorf("enabled=%v, want %v", got, tc.want)
			}
		})
	}
}
