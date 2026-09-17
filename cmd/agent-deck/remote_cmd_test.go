package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIsValidRemoteName(t *testing.T) {
	t.Parallel()

	valid := []string{"dev", "prod_us", "us-west-2"}
	invalid := []string{
		"",
		"dev env",
		"dev/env",
		"dev\\env",
		"dev.env",
		"dev:env",
	}

	for _, name := range valid {
		if !isValidRemoteName(name) {
			t.Fatalf("expected %q to be valid", name)
		}
	}

	for _, name := range invalid {
		if isValidRemoteName(name) {
			t.Fatalf("expected %q to be invalid", name)
		}
	}
}

func TestShouldProceedWithRemoteUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		readErr  error
		want     bool
	}{
		{name: "default yes on empty line", response: "\n", readErr: nil, want: true},
		{name: "yes lower", response: "y\n", readErr: nil, want: true},
		{name: "yes word", response: "yes\n", readErr: nil, want: true},
		{name: "no lower", response: "n\n", readErr: nil, want: false},
		{name: "other value", response: "nope\n", readErr: nil, want: false},
		{name: "eof empty fails closed", response: "", readErr: io.EOF, want: false},
		{name: "eof with explicit yes", response: "y", readErr: io.EOF, want: true},
		{name: "read error fails closed", response: "", readErr: io.ErrClosedPipe, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldProceedWithRemoteUpdate(tc.response, tc.readErr)
			if got != tc.want {
				t.Fatalf("shouldProceedWithRemoteUpdate(%q, %v) = %v, want %v", tc.response, tc.readErr, got, tc.want)
			}
		})
	}
}

func TestRemoteSessionsAcceptsJSONAfterRemoteName(t *testing.T) {
	remoteName, jsonOutput, envelope, err := parseRemoteSessionsArgs([]string{"clio", "--json"})
	if err != nil {
		t.Fatalf("parseRemoteSessionsArgs: %v", err)
	}
	if remoteName != "clio" || !jsonOutput || envelope {
		t.Fatalf("parsed (%q, %t, %t), want (clio, true, false)", remoteName, jsonOutput, envelope)
	}
}

func TestRemoteSessionsWithErrorsImpliesEnvelopeAndJSON(t *testing.T) {
	remoteName, jsonOutput, envelope, err := parseRemoteSessionsArgs([]string{"--json", "--with-errors", "clio"})
	if err != nil {
		t.Fatalf("parseRemoteSessionsArgs: %v", err)
	}
	if remoteName != "clio" || !jsonOutput || !envelope {
		t.Fatalf("parsed (%q, %t, %t), want (clio, true, true)", remoteName, jsonOutput, envelope)
	}
}

func TestRemoteSessionsJSONEnvelopeFlagImpliesJSON(t *testing.T) {
	remoteName, jsonOutput, envelope, err := parseRemoteSessionsArgs([]string{"--json-envelope"})
	if err != nil {
		t.Fatalf("parseRemoteSessionsArgs: %v", err)
	}
	if remoteName != "" || !jsonOutput || !envelope {
		t.Fatalf("parsed (%q, %t, %t), want (\"\", true, true)", remoteName, jsonOutput, envelope)
	}
}

func TestRemoteSessionFetchKeepsStructuredFailureAndStampsSuccess(t *testing.T) {
	output := remoteSessionsOutput{
		Sessions: []session.RemoteSessionInfo{},
		Errors:   []remoteSessionError{},
	}
	if addRemoteSessionFetch(&output, "clio", "johnw@clio", nil, errors.New("ssh unavailable")) {
		t.Fatal("failed fetch reported success")
	}
	if len(output.Errors) != 1 || output.Errors[0].Name != "clio" || output.Errors[0].Host != "johnw@clio" || output.Errors[0].Error != "ssh unavailable" {
		t.Fatalf("structured error = %#v", output.Errors)
	}

	sessions := []session.RemoteSessionInfo{{ID: "remote-session"}}
	if !addRemoteSessionFetch(&output, "clio", "johnw@clio", sessions, nil) {
		t.Fatal("successful fetch reported failure")
	}
	if len(output.Sessions) != 1 || output.Sessions[0].RemoteName != "clio" {
		t.Fatalf("sessions = %#v", output.Sessions)
	}
}

// tempHomeWithConfig writes contents as the agent-deck user config inside a
// fresh HOME and returns that HOME.
func tempHomeWithConfig(t *testing.T, contents string) string {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestRemoteSessionsPlainJSONStaysBareArray pins the contract that existing
// consumers (conductor scripts, skills) rely on when they pipe
// `agent-deck remote sessions --json` through `jq '.[]'`: a per-remote fetch
// failure must not turn the output into an object. The configured remote is
// deliberately unreachable, so FetchSessions fails and exercises the "some
// remotes errored" branch of the plain (non-envelope) --json path.
//
// walk defect #2: a zero-session result must marshal as `[]`, never `null`
// — `jq '.[]'` and `for row in $(jq -c '.[]')` both choke on `null` (no
// iteration at all, or a "cannot iterate over null" error), while `[]`
// degrades to "no rows" cleanly. `[]` was already the shape for
// --with-errors's "sessions" field; this makes the bare-array form match.
func TestRemoteSessionsPlainJSONStaysBareArray(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := tempHomeWithConfig(t, "[remotes.unreachable]\nhost = 'nobody@203.0.113.1'\n")
	stdout, _, code := runAgentDeck(t, home, "remote", "sessions", "--json")
	if code != 0 {
		t.Fatalf("plain --json must not fail the process on a per-remote error: code=%d stdout=%q", code, stdout)
	}

	if trimmed := strings.TrimSpace(stdout); !strings.HasPrefix(trimmed, "[") {
		t.Fatalf("plain --json must always be a bare array, never null, got: %s", stdout)
	}

	var asArray []session.RemoteSessionInfo
	if err := json.Unmarshal([]byte(stdout), &asArray); err != nil {
		t.Fatalf("plain --json did not decode as an array: %v\n%s", err, stdout)
	}
	if len(asArray) != 0 {
		t.Fatalf("expected no sessions from an unreachable remote, got %#v", asArray)
	}
}

// TestRemoteSessionsNoRemotesConfiguredStaysPlainText pins the long-standing
// quirk that "no remotes configured" is reported as plain text even under
// --json, since there is no session list to report. Scripts parsing that
// output already had to handle this case, so nothing here regresses.
func TestRemoteSessionsNoRemotesConfiguredStaysPlainText(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	stdout, stderr, code := runAgentDeck(t, t.TempDir(), "remote", "sessions", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("empty remotes: code=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "No remotes configured." {
		t.Fatalf("no-remotes output = %q, want the plain text message", stdout)
	}
}

func TestRemoteSessionsConfigErrorStaysPlainTextWithoutEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := tempHomeWithConfig(t, "[invalid")
	stdout, _, code := runAgentDeck(t, home, "remote", "sessions", "--json")
	if code != 1 {
		t.Fatalf("config error exit = %d, stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "Error: failed to load config") {
		t.Fatalf("config error output = %q, want the plain text message", stdout)
	}
	var probe json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &probe); err == nil {
		t.Fatalf("plain --json on config error must stay plain text, not JSON: %s", stdout)
	}
}

// TestRemoteSessionsEnvelopeFlagCoversEmptyAndConfigError exercises the
// opt-in {"sessions":[...],"errors":[...]} envelope via --with-errors and
// --json-envelope. Plain --json never produces this shape.
func TestRemoteSessionsEnvelopeFlagCoversEmptyAndConfigError(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	t.Run("empty", func(t *testing.T) {
		stdout, stderr, code := runAgentDeck(t, t.TempDir(), "remote", "sessions", "--json", "--with-errors")
		if code != 0 || stderr != "" {
			t.Fatalf("empty remotes: code=%d stderr=%q", code, stderr)
		}
		var output remoteSessionsOutput
		if err := json.Unmarshal([]byte(stdout), &output); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
		}
		if output.Sessions == nil || output.Errors == nil || len(output.Sessions) != 0 || len(output.Errors) != 0 {
			t.Fatalf("empty output = %#v", output)
		}
	})

	t.Run("config error", func(t *testing.T) {
		home := tempHomeWithConfig(t, "[invalid")
		stdout, _, code := runAgentDeck(t, home, "remote", "sessions", "--json-envelope")
		if code != 1 {
			t.Fatalf("config error exit = %d, stdout=%s", code, stdout)
		}
		var output remoteSessionsOutput
		if err := json.Unmarshal([]byte(stdout), &output); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
		}
		if len(output.Errors) != 1 || output.Errors[0].Name != "config" || !strings.Contains(output.Errors[0].Error, "config") {
			t.Fatalf("config error output = %#v", output)
		}
	})
}

// TestRemoteForwardedList_JSON_EmptyProfilePrintsEmptyArray is walk defect
// #3's regression test: `agent-deck remote <name> list --json` forwards
// "list --json" verbatim over SSH (remoteCommandArgs, runRemoteExec) and
// prints whatever the remote's own `list` command prints. The walk saw
// plain text ("No sessions found in profile 'default'.") from g14 because
// g14 still runs the pre-fix v1.16.10 binary; this branch's `list --json`
// already prints `[]` for an empty profile (fixed by ac626d0e, an ancestor
// of this branch, well before today's walk). This test proves the fix
// survives the SSH-forwarding path end to end: a fake `ssh` on PATH execs
// this branch's own just-built binary as the "remote", with its own empty
// HOME standing in for an empty remote profile.
func TestRemoteForwardedList_JSON_EmptyProfilePrintsEmptyArray(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	bin := channelsCLIBinary(t)
	remoteHome := t.TempDir()
	binDir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(binDir, "agent-deck")); err != nil {
		t.Fatal(err)
	}
	sshScript := "#!/bin/sh\nfor last; do :; done\nexport HOME=\"$FAKESSH_REMOTE_HOME\"\nexport PATH=\"$FAKESSH_BIN_DIR:$PATH\"\neval \"$last\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(sshScript), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKESSH_REMOTE_HOME", remoteHome)
	t.Setenv("FAKESSH_BIN_DIR", binDir)

	home := tempHomeWithConfig(t, "[remotes.g14]\nhost = 'fake@g14'\n")
	stdout, stderr, code := runAgentDeck(t, home, "remote", "g14", "list", "--json")
	if code != 0 {
		t.Fatalf("forwarded list --json: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if trimmed := strings.TrimSpace(stdout); trimmed != "[]" {
		t.Fatalf("forwarded `remote g14 list --json` on an empty profile = %q, want \"[]\"", trimmed)
	}
}
