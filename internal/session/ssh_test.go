package session

import (
	"strings"
	"testing"
)

func TestSSHRunnerBuildRemoteCommand_QuotesAllDynamicArgs(t *testing.T) {
	runner := &SSHRunner{
		AgentDeckPath: "/opt/agent deck/bin/agent-deck",
		Profile:       "work profile",
	}

	got := runner.buildRemoteCommand("rename", "abc123", "new title; rm -rf /", "quote's here")
	want := "'/opt/agent deck/bin/agent-deck' -p 'work profile' 'rename' 'abc123' 'new title; rm -rf /' 'quote'\\''s here'"
	if got != want {
		t.Fatalf("buildRemoteCommand mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestWrapForSSH_QuotesSSHHost(t *testing.T) {
	inst := NewInstance("ssh-test", "/tmp")
	inst.SSHHost = "user@host -oProxyCommand=bad"
	wrapped := inst.wrapForSSH("agent-deck list --json")

	if !strings.Contains(wrapped, "'user@host -oProxyCommand=bad'") {
		t.Fatalf("expected wrapped SSH host to be single-quoted, got: %s", wrapped)
	}
}

func TestParseRemoteSessionOutput(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    string
		wantErr bool
	}{
		{
			name:  "valid json with content",
			input: []byte(`{"content":"hello remote"}`),
			want:  "hello remote",
		},
		{
			name:  "empty payload",
			input: []byte("   \n  "),
			want:  "",
		},
		{
			name:    "invalid json",
			input:   []byte("not-json"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRemoteSessionOutput(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("content mismatch\nwant: %q\ngot:  %q", tc.want, got)
			}
		})
	}
}

func TestSSHRunnerBuildRemoteCommand_QuotesRemoteSessionOutputID(t *testing.T) {
	runner := &SSHRunner{AgentDeckPath: "/usr/local/bin/agent-deck"}

	sessionIDs := []string{
		"x; rm -rf /",
		"$(whoami)",
		`embedded'"quotes`,
	}

	for _, sessionID := range sessionIDs {
		t.Run(sessionID, func(t *testing.T) {
			got := runner.buildRemoteCommand("session", "output", sessionID, "--json")
			want := "'/usr/local/bin/agent-deck' 'session' 'output' " + shellQuote(sessionID) + " '--json'"
			if got != want {
				t.Fatalf("buildRemoteCommand mismatch\nwant: %s\ngot:  %s", want, got)
			}
		})
	}
}
