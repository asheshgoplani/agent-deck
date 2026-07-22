package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestHandleRemoteAdd_AgentboxPersistsConfig(t *testing.T) {
	withTempHomeAndConfig(t, "")

	output := captureStdout(t, func() {
		handleRemoteAdd([]string{"lab", "--kind", "agentbox", "--url", "https://agentbox.example/agentbox", "--token", "top-secret"})
	})

	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	rc, ok := cfg.Remotes["lab"]
	if !ok {
		t.Fatalf("remotes = %#v, want lab entry", cfg.Remotes)
	}
	if rc.GetKind() != session.RemoteKindAgentbox || rc.GetURL() != "https://agentbox.example/agentbox" || rc.Token != "top-secret" {
		t.Fatalf("remote config = %#v", rc)
	}
	if !strings.Contains(output, "Added remote 'lab'") {
		t.Fatalf("output = %q, want added message", output)
	}
}

func TestHandleRemoteSessions_AgentboxPrintsWorkspaceColumns(t *testing.T) {
	withTempHomeAndConfig(t, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":               "ws-123456789",
			"name":             "research-one",
			"orchestrator":     "wisp",
			"agent":            "pi-fireworks",
			"model":            "accounts/fireworks/models/glm-5p2",
			"runtime":          "docker",
			"status":           "running",
			"attachCommand":    "ssh remote tmux attach -t ws-1",
			"claimedTaskCount": 2,
		}})
	}))
	defer srv.Close()

	if err := session.SaveUserConfig(&session.UserConfig{
		Remotes: map[string]session.RemoteConfig{
			"lab": {Kind: session.RemoteKindAgentbox, URL: srv.URL},
		},
	}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	output := captureStdout(t, func() {
		handleRemoteSessions([]string{"lab"})
	})

	for _, want := range []string{"Remote: lab", "ORCH", "AGENT", "MODEL", "ATTACH", "research-one", "wisp", "pi-fireworks", "yes"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestHandleRemoteCreate_AgentboxPrintsAttachCommandsFromCreateResponse(t *testing.T) {
	withTempHomeAndConfig(t, "")

	var createPayload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspaces" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
			t.Fatalf("decode create payload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                 "ws-new",
			"name":               "research-one",
			"orchestrator":       "wisp",
			"agent":              "pi-fireworks",
			"model":              "accounts/fireworks/models/glm-5p2",
			"runtime":            "docker",
			"status":             "running",
			"attachCommand":      "ssh remote tmux attach -t ws-new",
			"localAttachCommand": "ssh localhost tmux attach -t ws-new",
			"claimedTaskCount":   1,
		})
	}))
	defer srv.Close()

	if err := session.SaveUserConfig(&session.UserConfig{
		Remotes: map[string]session.RemoteConfig{
			"lab": {Kind: session.RemoteKindAgentbox, URL: srv.URL},
		},
	}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	output := captureStdout(t, func() {
		handleRemoteCreate([]string{
			"lab",
			"--name", "research-one",
			"--cwd", "/srv/research",
			"--orchestrator", "wisp",
			"--agent", "pi-fireworks",
			"--model", "accounts/fireworks/models/glm-5p2",
			"--runtime", "docker",
		})
	})

	wantPayload := map[string]string{
		"name":         "research-one",
		"cwd":          "/srv/research",
		"orchestrator": "wisp",
		"agent":        "pi-fireworks",
		"model":        "accounts/fireworks/models/glm-5p2",
		"runtime":      "docker",
	}
	for key, want := range wantPayload {
		if createPayload[key] != want {
			t.Fatalf("payload[%q] = %q, want %q (payload=%#v)", key, createPayload[key], want, createPayload)
		}
	}
	for _, want := range []string{"Created remote agentbox 'lab' (ws-new)", "Attachable: true", "Remote attach:", "Local attach:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	outputCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, reader)
		outputCh <- buf.String()
	}()

	fn()

	_ = writer.Close()
	return <-outputCh
}
