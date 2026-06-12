package session

import (
	"context"
	"errors"
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

// TestSSHRunnerCreateSession_CleansOrphanOnStartFailure asserts that when the
// remote `add` succeeds but the subsequent `session start` fails (tmux death,
// network blip, timeout), CreateSession issues a compensating `remove` so the
// remote DB doesn't accumulate orphan rows pointing at non-existent tmux.
func TestSSHRunnerCreateSession_CleansOrphanOnStartFailure(t *testing.T) {
	var calls [][]string
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			switch {
			case len(args) > 0 && args[0] == "add":
				return []byte(`{"id":"orphan-abc","title":"x"}`), nil
			case len(args) >= 2 && args[0] == "session" && args[1] == "start":
				return nil, errors.New("simulated tmux death")
			case len(args) > 0 && args[0] == "remove":
				return []byte(""), nil
			}
			return nil, errors.New("unexpected runner call")
		},
	}

	_, err := runner.CreateSession(context.Background())
	if err == nil {
		t.Fatal("expected CreateSession to surface the start failure, got nil")
	}

	var sawRemove bool
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "remove" && c[1] == "orphan-abc" {
			sawRemove = true
			break
		}
	}
	if !sawRemove {
		t.Fatalf("expected compensating remove call for orphan-abc; calls=%v", calls)
	}
}

// TestSSHRunnerCreateSession_NoCleanupOnSuccess asserts the happy path doesn't
// issue a spurious remove call when both add and session start succeed.
func TestSSHRunnerCreateSession_NoCleanupOnSuccess(t *testing.T) {
	var calls [][]string
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			switch {
			case len(args) > 0 && args[0] == "add":
				return []byte(`{"id":"good-abc","title":"x"}`), nil
			case len(args) >= 2 && args[0] == "session" && args[1] == "start":
				return []byte(`{"success":true,"id":"good-abc","title":"x"}`), nil
			}
			return nil, errors.New("unexpected runner call")
		},
	}

	id, err := runner.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession unexpected error: %v", err)
	}
	if id != "good-abc" {
		t.Fatalf("CreateSession id = %q, want good-abc", id)
	}
	for _, c := range calls {
		if len(c) > 0 && c[0] == "remove" {
			t.Fatalf("unexpected remove call on success path: %v", c)
		}
	}
}

// TestRemoteAddArgs covers the `agent-deck add` argument builder used when the
// new-session dialog targets a remote (#1353): the chosen tool must be passed
// via -c (previously every remote `n` create was a bare `add --quick` shell),
// an explicit title uses -t while an empty one falls back to --quick, -g carries
// the selected group, and "." / empty path means "remote CWD" so no path
// argument is sent.
func TestRemoteAddArgs(t *testing.T) {
	cases := []struct {
		name                     string
		tool, title, path, group string
		want                     []string
	}{
		{
			name: "defaults (quick shell, remote CWD)",
			want: []string{"add", "--json", "--quick"},
		},
		{
			name: "tool and title from dialog",
			tool: "claude", title: "my task", path: ".",
			want: []string{"add", "--json", "-t", "my task", "-c", "claude"},
		},
		{
			name: "group from dialog",
			tool: "claude", title: "my task", group: "work", path: ".",
			want: []string{"add", "--json", "-t", "my task", "-g", "work", "-c", "claude"},
		},
		{
			name: "explicit remote path",
			tool: "codex", title: "fix", path: "/srv/project",
			want: []string{"add", "--json", "-t", "fix", "-c", "codex", "/srv/project"},
		},
		{
			name: "tool without title auto-names via --quick",
			tool: "pi",
			want: []string{"add", "--json", "--quick", "-c", "pi"},
		},
		{
			name: "whitespace-only values fall back to defaults",
			tool: "  ", title: " ", group: " ", path: " . ",
			want: []string{"add", "--json", "--quick"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteAddArgs(tc.tool, tc.title, tc.path, tc.group)
			if len(got) != len(tc.want) {
				t.Fatalf("remoteAddArgs(%q,%q,%q,%q) = %v, want %v", tc.tool, tc.title, tc.path, tc.group, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("remoteAddArgs(%q,%q,%q,%q) = %v, want %v", tc.tool, tc.title, tc.path, tc.group, got, tc.want)
				}
			}
		})
	}
}

func TestSSHRunnerCreateSessionWithOptions_UsesDialogValues(t *testing.T) {
	var calls [][]string
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			switch {
			case len(args) > 0 && args[0] == "add":
				return []byte(`{"id":"remote-abc","title":"Remote Work"}`), nil
			case len(args) >= 2 && args[0] == "session" && args[1] == "start":
				return []byte(`{"success":true,"id":"remote-abc","title":"Remote Work"}`), nil
			}
			return nil, errors.New("unexpected runner call")
		},
	}

	id, err := runner.CreateSessionWithOptions(context.Background(), "codex", "Remote Work", "~/project", "work")
	if err != nil {
		t.Fatalf("CreateSessionWithOptions unexpected error: %v", err)
	}
	if id != "remote-abc" {
		t.Fatalf("id = %q, want remote-abc", id)
	}
	if len(calls) < 2 {
		t.Fatalf("calls = %v, want add and start", calls)
	}
	add := strings.Join(calls[0], " ")
	for _, want := range []string{"add", "--json", "-t", "Remote Work", "-g", "work", "-c", "codex", "~/project"} {
		if !strings.Contains(add, want) {
			t.Fatalf("remote add call = %q, want token %q", add, want)
		}
	}
	if strings.Contains(add, "--quick") {
		t.Fatalf("remote add call = %q, did not expect --quick with explicit title", add)
	}
	start := strings.Join(calls[1], " ")
	for _, want := range []string{"session", "start", "--json", "remote-abc"} {
		if !strings.Contains(start, want) {
			t.Fatalf("remote start call = %q, want token %q", start, want)
		}
	}
}

func TestSSHRunnerCreateSessionWithOptions_QueuedStartIsNotAttachable(t *testing.T) {
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			switch {
			case len(args) > 0 && args[0] == "add":
				return []byte(`{"id":"queued-abc","title":"queued-title"}`), nil
			case len(args) >= 2 && args[0] == "session" && args[1] == "start":
				return []byte(`{"success":true,"id":"queued-abc","title":"queued-title","status":"queued"}`), nil
			}
			return nil, errors.New("unexpected runner call")
		},
	}

	_, err := runner.CreateSessionWithOptions(context.Background(), "claude", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("CreateSessionWithOptions error = %v, want queued error", err)
	}
}
