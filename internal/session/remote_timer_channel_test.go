package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// The remote agent refuses every `update` verb over the persistent channel
// (remoteAgentArgsAllowed). The TUI keeps that channel up to every remote,
// so the update timer read and heal must go over exec even while it is
// connected, or they read "unknown" and never heal (#2472 review).
func TestSSHRunner_UpdateVerbsBypassAConnectedChannel(t *testing.T) {
	t.Setenv("AGENT_DECK_REMOTE_CHANNEL", "1")
	t.Cleanup(CloseRemoteChannels)

	// The exec seam: a fake ssh on PATH that answers like a current remote
	// and records every remote command it was given.
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "ssh-calls")
	script := `#!/bin/sh
for last; do :; done
printf '%s\n' "$last" >> "$FAKE_SSH_CALLS"
case "$last" in
  *--timer-status*) printf '%s\n' '{"installed":true,"kind":"systemd","active":true,"path":"/u/agent-deck-autoupdate.timer"}' ;;
  *--ensure-timer*) printf '%s\n' '{"action":"none","status":{"installed":true,"kind":"systemd","active":true}}' ;;
  *--unattended*) printf 'updated\n' ;;
  *) echo "unexpected remote command: $last" >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SSH_CALLS", calls)

	agent := newFakeAgent(t, true, false)
	r := &SSHRunner{name: "timer-channel-test", Host: "user@remote", Profile: "default", AgentDeckPath: "agent-deck", commandTimeout: 10 * time.Second, lastStderr: &lastStderrBox{}, dialChannelFn: agent.dial}
	ch := channelFor(r)
	if ch == nil {
		t.Fatal("channelFor returned nil")
	}
	if !waitUntil(t, 2*time.Second, ch.Connected) {
		t.Fatal("channel never connected")
	}
	// The channel itself works: a listing goes over it, not exec.
	if out, err := r.Run(context.Background(), "list", "--json"); err != nil || string(out) != "out:list --json" {
		t.Fatalf("a listing must go over the connected channel, got %q, %v", out, err)
	}

	ctx := context.Background()
	st := r.FetchTimerStatus(ctx)
	if st.Kind != update.TimerKindSystemd || !st.Active || st.Note != "" {
		t.Fatalf("timer status over a connected channel = %+v, want the remote's systemd answer", st)
	}
	inst, err := r.InstallUpdateTimer(ctx, true)
	if err != nil || inst.Action != update.TimerActionNone || inst.Status.Kind != update.TimerKindSystemd {
		t.Fatalf("timer heal over a connected channel = %+v, %v; want the remote's answer", inst, err)
	}
	if out, err := r.FallbackUpdate(ctx); err != nil || strings.TrimSpace(string(out)) != "updated" {
		t.Fatalf("fallback update over a connected channel = %q, %v", out, err)
	}

	if n := agent.denied.Load(); n != 0 {
		t.Fatalf("the agent was sent %d update verbs it refuses", n)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("exec was never used: %v", err)
	}
	got := string(data)
	for _, want := range []string{"'update' '--timer-status' '--json'", "'update' '--ensure-timer' '--json'", "'update' '--unattended'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("exec calls %q lack %s", got, want)
		}
	}
	if strings.Contains(got, "'list'") {
		t.Fatalf("the listing must not have gone over exec: %q", got)
	}
}

// The client and the agent share one deny list, so a verb the agent
// refuses is never sent over the channel.
func TestRemoteChannelArgsSafe_SkipsVerbsTheAgentDenies(t *testing.T) {
	for _, args := range [][]string{
		{"update", "--timer-status", "--json"},
		{"update", "--ensure-timer", "--json"},
		{"update", "--install-timer", "--json"},
		{"update", "--unattended", "--trigger", "nudge-fallback"},
		{"uninstall"},
		{"web"},
		{"remote-agent"},
	} {
		if remoteChannelArgsSafe(args) {
			t.Errorf("remoteChannelArgsSafe(%q) = true; the agent refuses this verb", args)
		}
	}
	if !remoteChannelArgsSafe([]string{"list", "--json"}) {
		t.Error("a listing must stay on the channel")
	}
}
