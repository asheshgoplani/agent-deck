package terminal

import (
	"strings"
	"testing"
)

// ASK-201: a Shift+Enter window on a remote session ran a raw
// `ssh … session attach`, so nothing on this machine sat between the keys and
// the host and Ctrl+V could never carry a clipboard image across. With this
// machine's agent-deck known, the window runs `agent-deck remote attach`,
// the same attach as Enter in the TUI (image paste, Ctrl+Q, mosh fallback).
func TestBuildAttachCommand_RemoteViaLocalCLI(t *testing.T) {
	req := AttachRequest{Name: "remote-id-xyz", Remote: &RemoteAttach{
		Host: "alice@lab.example", AgentDeckPath: "/usr/local/bin/agent-deck", Profile: "work",
		LocalCLI: "/Users/a/.local/bin/agent-deck", LocalProfile: "personal", RemoteName: "lab",
	}}
	got := BuildAttachCommand(req)
	want := "'/Users/a/.local/bin/agent-deck' -p 'personal' remote attach 'lab' 'remote-id-xyz'"
	if got != want {
		t.Fatalf("command = %q\nwant      %q", got, want)
	}
	if strings.Contains(got, "ssh") {
		t.Fatalf("no raw ssh: %q", got)
	}

	req.Remote.LocalProfile = "default"
	if got := BuildAttachCommand(req); got != "'/Users/a/.local/bin/agent-deck' remote attach 'lab' 'remote-id-xyz'" {
		t.Fatalf("default profile adds no -p: %q", got)
	}

	// Without a local CLI or remote name, the old ssh line stays.
	req.Remote.LocalCLI = ""
	if got := BuildAttachCommand(req); !strings.HasPrefix(got, "ssh -tt") {
		t.Fatalf("fallback = %q", got)
	}
}
