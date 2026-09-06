package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
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
		name string
		opts RemoteAddOptions
		want []string
	}{
		{
			name: "defaults (quick shell, remote CWD)",
			want: []string{"add", "--json", "--quick"},
		},
		{
			name: "tool and title from dialog",
			opts: RemoteAddOptions{Tool: "claude", Title: "my task", Path: "."},
			want: []string{"add", "--json", "-t", "my task", "-c", "claude"},
		},
		{
			name: "group from dialog",
			opts: RemoteAddOptions{Tool: "claude", Title: "my task", Group: "work", Path: "."},
			want: []string{"add", "--json", "-t", "my task", "-g", "work", "-c", "claude"},
		},
		{
			name: "explicit remote path",
			opts: RemoteAddOptions{Tool: "codex", Title: "fix", Path: "/srv/project"},
			want: []string{"add", "--json", "-t", "fix", "-c", "codex", "/srv/project"},
		},
		{
			name: "create a missing remote directory only after confirmation",
			opts: RemoteAddOptions{Tool: "codex", Title: "fix", Path: "/srv/new", CreateDir: true},
			want: []string{"add", "--json", "-t", "fix", "-c", "codex", "--create-dir", "/srv/new"},
		},
		{
			name: "tool without title auto-names via --quick",
			opts: RemoteAddOptions{Tool: "pi"},
			want: []string{"add", "--json", "--quick", "-c", "pi"},
		},
		{
			name: "whitespace-only values fall back to defaults",
			opts: RemoteAddOptions{Tool: "  ", Title: " ", Group: " ", Path: " . ", Account: " ", Model: " ", WorktreeBranch: " ", MCPs: []string{" "}, ExtraArgs: []string{""}},
			want: []string{"add", "--json", "--quick"},
		},
		{
			name: "docker sandbox checkbox is forwarded before the path",
			opts: RemoteAddOptions{Tool: "claude", Title: "sandboxed", Path: "/srv/project", Sandbox: true},
			want: []string{"add", "--json", "-t", "sandboxed", "-c", "claude", "-sandbox", "/srv/project"},
		},
		{
			name: "docker sandbox with quick name and remote CWD",
			opts: RemoteAddOptions{Tool: "claude", Sandbox: true},
			want: []string{"add", "--json", "--quick", "-c", "claude", "-sandbox"},
		},
		{
			name: "account slot is forwarded by name for the server to resolve",
			opts: RemoteAddOptions{Tool: "claude", Title: "t", Account: "alice"},
			want: []string{"add", "--json", "-t", "t", "-c", "claude", "--account", "alice"},
		},
		{
			name: "model override is forwarded",
			opts: RemoteAddOptions{Tool: "claude", Title: "t", Model: "opus"},
			want: []string{"add", "--json", "-t", "t", "-c", "claude", "--model", "opus"},
		},
		{
			name: "MCP names are forwarded as repeated --mcp",
			opts: RemoteAddOptions{Tool: "claude", Title: "t", MCPs: []string{"memory", "github"}},
			want: []string{"add", "--json", "-t", "t", "-c", "claude", "--mcp", "memory", "--mcp", "github"},
		},
		{
			name: "resume session id is forwarded",
			opts: RemoteAddOptions{Tool: "claude", Title: "t", ResumeSessionID: "abc-123"},
			want: []string{"add", "--json", "-t", "t", "-c", "claude", "--resume-session", "abc-123"},
		},
		{
			name: "claude toggles travel as repeated --extra-arg tokens",
			opts: RemoteAddOptions{Tool: "claude", Title: "t", ExtraArgs: []string{"--dangerously-skip-permissions", "--effort", "high", "--chrome"}},
			want: []string{"add", "--json", "-t", "t", "-c", "claude", "--extra-arg", "--dangerously-skip-permissions", "--extra-arg", "--effort", "--extra-arg", "high", "--extra-arg", "--chrome"},
		},
		{
			name: "yolo is forwarded for codex and gemini",
			opts: RemoteAddOptions{Tool: "codex", Title: "t", Yolo: true},
			want: []string{"add", "--json", "-t", "t", "-c", "codex", "--yolo"},
		},
		{
			name: "worktree branch is forwarded as -w before the path",
			opts: RemoteAddOptions{Tool: "claude", Title: "t", Path: "/srv/repo", WorktreeBranch: "feature/x"},
			want: []string{"add", "--json", "-t", "t", "-c", "claude", "-w", "feature/x", "/srv/repo"},
		},
		{
			name: "everything at once keeps a stable order",
			opts: RemoteAddOptions{
				Tool: "claude", Title: "all", Path: "/srv/repo", Group: "work", Sandbox: true,
				Account: "alice", Model: "opus", MCPs: []string{"memory"}, ResumeSessionID: "abc",
				ExtraArgs: []string{"--chrome"}, Yolo: true, WorktreeBranch: "feature/all",
			},
			want: []string{"add", "--json", "-t", "all", "-g", "work", "-c", "claude", "-sandbox",
				"--account", "alice", "--model", "opus", "--mcp", "memory", "--resume-session", "abc",
				"--extra-arg", "--chrome", "--yolo", "-w", "feature/all", "/srv/repo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remoteAddArgs(tc.opts)
			if err != nil {
				t.Fatalf("remoteAddArgs(%+v) error = %v", tc.opts, err)
			}
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("remoteAddArgs(%+v) = %q, want %q", tc.opts, got, tc.want)
			}
		})
	}
}

// Values that would silently mean something else on the server are refused
// before any SSH round trip, with an error that names the value.
func TestRemoteAddArgs_RejectsUnforwardableValues(t *testing.T) {
	cases := []struct {
		name    string
		opts    RemoteAddOptions
		wantErr string
	}{
		{
			name:    "absolute config directory as account",
			opts:    RemoteAddOptions{Tool: "claude", Account: "/Users/me/.claude-work"},
			wantErr: "config directory",
		},
		{
			name:    "home-relative config directory as account",
			opts:    RemoteAddOptions{Tool: "claude", Account: "~/.claude-work"},
			wantErr: "config directory",
		},
		{
			name:    "relative config directory as account",
			opts:    RemoteAddOptions{Tool: "claude", Account: "./claude-work"},
			wantErr: "config directory",
		},
		{
			name:    "flag and value fused into one extra-arg token",
			opts:    RemoteAddOptions{Tool: "claude", ExtraArgs: []string{"--model opus"}},
			wantErr: "separate --extra-arg tokens",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remoteAddArgs(tc.opts)
			if err == nil {
				t.Fatalf("remoteAddArgs(%+v) = %q, want error containing %q", tc.opts, got, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("remoteAddArgs(%+v) error = %q, want it to contain %q", tc.opts, err, tc.wantErr)
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

	id, err := runner.CreateSessionWithOptions(context.Background(), RemoteAddOptions{
		Tool: "codex", Title: "Remote Work", Path: "~/project", Group: "work", Sandbox: true, Model: "gpt-5-codex", Yolo: true,
	})
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
	for _, want := range []string{"add", "--json", "-t", "Remote Work", "-g", "work", "-c", "codex", "-sandbox", "--model", "gpt-5-codex", "--yolo", "~/project"} {
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

	_, err := runner.CreateSessionWithOptions(context.Background(), RemoteAddOptions{Tool: "claude"})
	if err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("CreateSessionWithOptions error = %v, want queued error", err)
	}
}

func TestParseRemoteVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			// The bug: a binary one release behind advertises an available
			// update, and LastIndex("v") used to return "1.9.55)" instead of the
			// real current version "1.9.49".
			name: "update available suffix returns current version",
			raw:  "Agent Deck v1.9.49 (update available: v1.9.55)",
			want: "1.9.49",
		},
		{
			name: "plain version",
			raw:  "Agent Deck v1.9.55",
			want: "1.9.55",
		},
		{
			name: "trailing newline",
			raw:  "Agent Deck v1.9.55\n",
			want: "1.9.55",
		},
		{
			name: "bare v-prefixed version",
			raw:  "v1.9.55",
			want: "1.9.55",
		},
		{
			name: "bare version",
			raw:  "1.9.55",
			want: "1.9.55",
		},
		{
			name: "pre-release tail",
			raw:  "Agent Deck v1.9.55-rc.1",
			want: "1.9.55-rc.1",
		},
		{
			// No semver token: fall back to the trimmed raw input so callers
			// still behave.
			name: "garbage falls back to trimmed raw",
			raw:  "  no version here  ",
			want: "no version here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRemoteVersion(tt.raw); got != tt.want {
				t.Errorf("parseRemoteVersion(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRemoteSessionInfoLastActivity(t *testing.T) {
	now := time.Now()

	t.Run("valid RFC3339Nano round-trips exactly, sub-second included", func(t *testing.T) {
		// Sub-second precision matters here: RemoteSessionInfo is formatted
		// with RFC3339Nano (not RFC3339) specifically so a session sitting
		// right on a TimeFilterMode 3/7-day cutoff isn't misclassified by
		// rounding down to the nearest second. now (not now.Truncate(time.
		// Second)) exercises that: it round-trips only if fractional seconds
		// actually survive the format/parse.
		r := RemoteSessionInfo{LastActivityAt: now.Format(time.RFC3339Nano)}
		got, ok := r.LastActivity()
		if !ok {
			t.Fatalf("LastActivity() ok = false, want true")
		}
		if !got.Equal(now) {
			t.Errorf("LastActivity() = %v, want %v (sub-second precision lost)", got, now)
		}
	})

	t.Run("empty is unknown, not zero-time", func(t *testing.T) {
		r := RemoteSessionInfo{}
		if _, ok := r.LastActivity(); ok {
			t.Errorf("LastActivity() ok = true for empty field, want false (older remote, field never sent)")
		}
	})

	t.Run("malformed value is unknown, not an error a caller must handle", func(t *testing.T) {
		r := RemoteSessionInfo{LastActivityAt: "not-a-timestamp"}
		if _, ok := r.LastActivity(); ok {
			t.Errorf("LastActivity() ok = true for malformed field, want false")
		}
	})
}

// TestRemoteSessionInfoLastActivity_BoundaryPrecision covers the scenario a
// review caught: a session active a few hundred milliseconds inside a
// TimeFilterMode 3-day cutoff round-trips as "inside" the window. Formatting
// with RFC3339 (whole seconds only) can round down across the cutoff and
// misclassify it as outside; RFC3339Nano cannot.
func TestRemoteSessionInfoLastActivity_BoundaryPrecision(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-3 * 24 * time.Hour)
	// 300ms inside the 3-day window (i.e. after the cutoff). A whole-second
	// truncation could floor this across the cutoff if the sub-second part
	// were dropped and the second itself landed exactly on it.
	justInside := cutoff.Add(300 * time.Millisecond)

	r := RemoteSessionInfo{LastActivityAt: justInside.Format(time.RFC3339Nano)}
	got, ok := r.LastActivity()
	if !ok {
		t.Fatalf("LastActivity() ok = false, want true")
	}
	if got.Before(cutoff) {
		t.Fatalf("LastActivity() = %v, which is before the 3-day cutoff %v (precision lost)", got, cutoff)
	}
	if !TimeFilter3Days.Matches(got, now) {
		t.Errorf("TimeFilter3Days.Matches(%v, %v) = false, want true (300ms inside the window)", got, now)
	}
}

// A refused value must never reach the remote: no add, no start, no cleanup.
// FetchAccounts asks the remote for its slot names only; config_dir values in
// the answer are dropped and never offered as something to pick.
func TestSSHRunnerFetchAccounts(t *testing.T) {
	var gotArgs []string
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			gotArgs = args
			return []byte(`[{"name":"alice","config_dir":"/home/alice/.claude-work","exists":true},{"name":"bob","config_dir":"/home/bob/.claude","exists":false}]` + "\n"), nil
		},
	}
	names, err := runner.FetchAccounts(context.Background())
	if err != nil {
		t.Fatalf("FetchAccounts: %v", err)
	}
	if strings.Join(gotArgs, " ") != "accounts --json" {
		t.Fatalf("remote command = %q, want \"accounts --json\"", strings.Join(gotArgs, " "))
	}
	if strings.Join(names, ",") != "alice,bob" {
		t.Fatalf("names = %v, want [alice bob]", names)
	}
	for _, n := range names {
		if strings.ContainsAny(n, `/\`) {
			t.Fatalf("name %q looks like a config directory; only slot names may be offered", n)
		}
	}
}

// A remote too old for `accounts` (unknown command) or one that answers with
// something other than a JSON list is an error, never a silent empty list
// that would look like "no slots configured".
func TestSSHRunnerFetchAccounts_ErrorsAreNotEmptyLists(t *testing.T) {
	cases := []struct {
		name   string
		output string
		err    error
	}{
		{name: "unknown command on old remote", output: "", err: errors.New("exit status 2")},
		{name: "human text instead of json", output: "No named account slots configured.\n"},
		{name: "empty stdout", output: ""},
		{name: "malformed json", output: "[{"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &SSHRunner{
				runFn: func(ctx context.Context, args ...string) ([]byte, error) {
					return []byte(tc.output), tc.err
				},
			}
			names, err := runner.FetchAccounts(context.Background())
			if err == nil {
				t.Fatalf("FetchAccounts = %v, want an error", names)
			}
			if len(names) != 0 {
				t.Fatalf("names = %v on error, want none", names)
			}
		})
	}
}

// FetchMCPs asks the remote for its MCP names only; the command, args, env
// and URL of each definition describe processes on the server and are never
// offered as something to pick. Names come back sorted because the remote
// lists them in map order.
func TestSSHRunnerFetchMCPs(t *testing.T) {
	var gotArgs []string
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			gotArgs = args
			return []byte(`{"mcps":[{"name":"memory","transport":"stdio","command":"npx","args":["-y","@modelcontextprotocol/server-memory"]},{"name":"github","transport":"http","url":"http://127.0.0.1:9000/mcp","env":{"TOKEN":"secret"}},{"name":"  ","transport":"stdio"}]}` + "\n"), nil
		},
	}
	names, err := runner.FetchMCPs(context.Background())
	if err != nil {
		t.Fatalf("FetchMCPs: %v", err)
	}
	if strings.Join(gotArgs, " ") != "mcp list --json" {
		t.Fatalf("remote command = %q, want \"mcp list --json\"", strings.Join(gotArgs, " "))
	}
	if strings.Join(names, ",") != "github,memory" {
		t.Fatalf("names = %v, want [github memory]", names)
	}
}

// A remote too old for `mcp list --json`, one whose answer is not the JSON
// object the command prints, or one that says nothing is an error, never a
// silent empty list that would look like "no MCPs configured".
func TestSSHRunnerFetchMCPs_ErrorsAreNotEmptyLists(t *testing.T) {
	cases := []struct {
		name   string
		output string
		err    error
	}{
		{name: "unknown command on old remote", output: "", err: errors.New("exit status 2")},
		{name: "human text instead of json", output: "No MCPs configured.\n"},
		{name: "empty stdout", output: ""},
		{name: "malformed json", output: `{"mcps":[`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &SSHRunner{
				runFn: func(ctx context.Context, args ...string) ([]byte, error) {
					return []byte(tc.output), tc.err
				},
			}
			names, err := runner.FetchMCPs(context.Background())
			if err == nil {
				t.Fatalf("FetchMCPs = %v, want an error", names)
			}
			if len(names) != 0 {
				t.Fatalf("names = %v on error, want none", names)
			}
		})
	}
}

// A remote with no MCPs answers {"mcps":[]}: that is a real, empty list.
func TestSSHRunnerFetchMCPs_EmptyListIsNotAnError(t *testing.T) {
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			return []byte(`{"mcps":[]}` + "\n"), nil
		},
	}
	names, err := runner.FetchMCPs(context.Background())
	if err != nil || len(names) != 0 {
		t.Fatalf("FetchMCPs = %v, %v; want an empty list and no error", names, err)
	}
}

func TestSSHRunnerCreateSessionWithOptions_RefusedValueNeverContactsRemote(t *testing.T) {
	calls := 0
	runner := &SSHRunner{
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			return nil, errors.New("must not be called")
		},
	}
	_, err := runner.CreateSessionWithOptions(context.Background(), RemoteAddOptions{Tool: "claude", Account: "/home/me/.claude"})
	if err == nil || !strings.Contains(err.Error(), "config directory") {
		t.Fatalf("CreateSessionWithOptions error = %v, want config directory refusal", err)
	}
	if calls != 0 {
		t.Fatalf("remote contacted %d times for a refused value, want 0", calls)
	}
}

// TestParseGroupListPaths pins the group-list flattener that backs
// SSHRunner.FetchGroupPaths: `group list --json` returns a recursive tree
// whose paths (including EMPTY groups — session_count 0) must all surface,
// deduped and sorted, for the remote move/create dialogs.
func TestParseGroupListPaths(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty list",
			input: `{"groups":[],"total_groups":0,"total_sessions":0}`,
			want:  []string{},
		},
		{
			name: "flat groups incl. an empty one",
			input: `{"groups":[
				{"name":"emptytest","path":"emptytest","session_count":0},
				{"name":"work","path":"work","session_count":3}
			],"total_groups":2,"total_sessions":3}`,
			want: []string{"emptytest", "work"},
		},
		{
			name: "nested children flattened with full paths",
			input: `{"groups":[
				{"name":"my-sessions","path":"my-sessions","session_count":7,
				 "children":[
					{"name":"done","path":"my-sessions/done","session_count":6,
					 "children":[
						{"name":"deep","path":"my-sessions/done/deep","session_count":0}
					 ]}
				 ]}
			],"total_groups":3,"total_sessions":7}`,
			want: []string{"my-sessions", "my-sessions/done", "my-sessions/done/deep"},
		},
		{
			name: "slashes and whitespace normalized",
			input: `{"groups":[
				{"name":"a","path":"/a/","session_count":0}
			],"total_groups":1,"total_sessions":0}`,
			want: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parsed groupListJSON
			if err := json.Unmarshal([]byte(tt.input), &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := parseGroupListPaths(parsed)
			if len(got) != len(tt.want) {
				t.Fatalf("parseGroupListPaths = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("parseGroupListPaths[%d] = %q, want %q (all: %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// The remote `add` reports a missing project directory on stdout; the TUI
// recognises that refusal to offer creating the directory, and nothing else.
func TestIsRemotePathMissing(t *testing.T) {
	if !IsRemotePathMissing(errors.New("ssh command failed: exit status 1: Error: path does not exist: /srv/new")) {
		t.Fatal("missing-path refusal not recognised")
	}
	if IsRemotePathMissing(errors.New("ssh command failed: exit status 255: connection refused")) {
		t.Fatal("unrelated failure treated as a missing path")
	}
	if IsRemotePathMissing(nil) {
		t.Fatal("nil error treated as a missing path")
	}
}
