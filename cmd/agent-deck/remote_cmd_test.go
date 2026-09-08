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
	remoteName, jsonOutput, err := parseRemoteSessionsArgs([]string{"clio", "--json"})
	if err != nil {
		t.Fatalf("parseRemoteSessionsArgs: %v", err)
	}
	if remoteName != "clio" || !jsonOutput {
		t.Fatalf("parsed (%q, %t), want (clio, true)", remoteName, jsonOutput)
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

func TestRemoteSessionsJSONCoversEmptyAndConfigError(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	t.Run("empty", func(t *testing.T) {
		stdout, stderr, code := runAgentDeck(t, t.TempDir(), "remote", "sessions", "--json")
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
		home := t.TempDir()
		configDir := filepath.Join(home, ".config", "agent-deck")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, _, code := runAgentDeck(t, home, "remote", "sessions", "--json")
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
