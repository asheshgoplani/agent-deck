package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentboxRunnerFetchSessions_MapsWorkspaceFieldsAndAuth(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/workspaces" {
			t.Fatalf("path = %q, want /v1/workspaces", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":                 "ws-123456789",
			"name":               "research-one",
			"orchestrator":       "wisp",
			"agent":              "pi-fireworks",
			"model":              "accounts/fireworks/models/glm-5p2",
			"runtime":            "docker",
			"cwd":                "/srv/research",
			"status":             "running",
			"attachCommand":      "ssh host tmux attach -t ws-123",
			"localAttachCommand": "ssh localhost tmux attach -t ws-123",
			"claimedTaskCount":   3,
			"createdAt":          "2026-07-22T00:00:00.000Z",
		}})
	}))
	defer srv.Close()

	runner := NewAgentboxRunner("lab", RemoteConfig{
		Kind:  RemoteKindAgentbox,
		URL:   srv.URL,
		Token: "top-secret",
	})

	sessions, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions unexpected error: %v", err)
	}
	if authHeader != "Bearer top-secret" {
		t.Fatalf("Authorization = %q, want bearer token", authHeader)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	got := sessions[0]
	if got.Title != "research-one" || got.Orchestrator != "wisp" || got.Agent != "pi-fireworks" {
		t.Fatalf("mapped identity fields wrong: %+v", got)
	}
	if got.Model != "accounts/fireworks/models/glm-5p2" || got.Runtime != "docker" || got.Path != "/srv/research" {
		t.Fatalf("mapped model/runtime/path wrong: %+v", got)
	}
	if got.Tool != "pi" {
		t.Fatalf("Tool = %q, want pi", got.Tool)
	}
	if got.Status != "running" || got.LifecycleStatus != "running" {
		t.Fatalf("status mapping wrong: %+v", got)
	}
	if !got.Attachable {
		t.Fatalf("Attachable = false, want true: %+v", got)
	}
	if got.ClaimedTaskCount != 3 || got.RemoteName != "lab" {
		t.Fatalf("task-count/remote mapping wrong: %+v", got)
	}
}

func TestAgentboxRunnerCreateSession_RequiresExplicitFields(t *testing.T) {
	runner := NewAgentboxRunner("lab", RemoteConfig{Kind: RemoteKindAgentbox, URL: "http://127.0.0.1:1"})

	cases := []struct {
		name string
		opts RemoteCreateOptions
		want string
	}{
		{name: "missing name", opts: RemoteCreateOptions{Orchestrator: "wisp", Agent: "pi-fireworks", ModelID: "m", Runtime: "docker"}, want: "--name"},
		{name: "missing orchestrator", opts: RemoteCreateOptions{Title: "x", Agent: "pi-fireworks", ModelID: "m", Runtime: "docker"}, want: "--orchestrator"},
		{name: "missing agent", opts: RemoteCreateOptions{Title: "x", Orchestrator: "wisp", ModelID: "m", Runtime: "docker"}, want: "--agent"},
		{name: "unsupported agent", opts: RemoteCreateOptions{Title: "x", Orchestrator: "wisp", Agent: "pi", ModelID: "m", Runtime: "docker"}, want: "claude-code, codex, or pi-fireworks"},
		{name: "missing model", opts: RemoteCreateOptions{Title: "x", Orchestrator: "wisp", Agent: "pi-fireworks", Runtime: "docker"}, want: "--model"},
		{name: "missing runtime", opts: RemoteCreateOptions{Title: "x", Orchestrator: "wisp", Agent: "pi-fireworks", ModelID: "m"}, want: "--runtime"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runner.CreateSession(context.Background(), tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CreateSession error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestAgentboxRunnerCreateSession_PostsExpectedPayload(t *testing.T) {
	var payload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspaces" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                 "ws-new",
			"status":             "running",
			"attachCommand":      "ssh remote-host tmux attach -t ws-new",
			"localAttachCommand": "ssh localhost tmux attach -t ws-new",
		})
	}))
	defer srv.Close()

	runner := NewAgentboxRunner("lab", RemoteConfig{Kind: RemoteKindAgentbox, URL: srv.URL})
	result, err := runner.CreateSession(context.Background(), RemoteCreateOptions{
		Title:        "research-one",
		Path:         "/srv/research",
		ModelID:      "accounts/fireworks/models/glm-5p2",
		Orchestrator: "wisp",
		Agent:        "pi-fireworks",
		Runtime:      "docker",
	})
	if err != nil {
		t.Fatalf("CreateSession unexpected error: %v", err)
	}
	if result.SessionID != "ws-new" {
		t.Fatalf("CreateSession session id = %q, want ws-new", result.SessionID)
	}
	if !result.Attachable {
		t.Fatalf("CreateSession result should preserve attachability: %+v", result)
	}
	if result.AttachCommand != "ssh remote-host tmux attach -t ws-new" {
		t.Fatalf("AttachCommand = %q, want create response attach command", result.AttachCommand)
	}
	if result.LocalAttachCommand != "ssh localhost tmux attach -t ws-new" {
		t.Fatalf("LocalAttachCommand = %q, want create response local attach command", result.LocalAttachCommand)
	}
	want := map[string]string{
		"name":         "research-one",
		"cwd":          "/srv/research",
		"model":        "accounts/fireworks/models/glm-5p2",
		"orchestrator": "wisp",
		"agent":        "pi-fireworks",
		"runtime":      "docker",
	}
	if len(payload) != len(want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
	for key, wantValue := range want {
		if payload[key] != wantValue {
			t.Fatalf("payload[%q] = %q, want %q (payload=%#v)", key, payload[key], wantValue, payload)
		}
	}
}

func TestAgentboxRunnerResolveAttach_PrefersLocalCommandForLocalhost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                 "ws-1",
			"status":             "running",
			"attachCommand":      "ssh remote-host tmux attach -t ws-1",
			"localAttachCommand": "ssh localhost tmux attach -t ws-1",
		})
	}))
	defer srv.Close()

	runner := NewAgentboxRunner("lab", RemoteConfig{Kind: RemoteKindAgentbox, URL: srv.URL})
	intent, err := runner.ResolveAttach(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ResolveAttach unexpected error: %v", err)
	}
	if !intent.Local {
		t.Fatalf("Local = false, want true")
	}
	if intent.Command != "ssh localhost tmux attach -t ws-1" {
		t.Fatalf("Command = %q, want local attach command", intent.Command)
	}
}

func TestAgentboxRunnerResolveAttach_TranslatesStoppedWorkspace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "workspace_not_running",
			"status": "stopped",
		})
	}))
	defer srv.Close()

	runner := NewAgentboxRunner("lab", RemoteConfig{Kind: RemoteKindAgentbox, URL: srv.URL})
	_, err := runner.ResolveAttach(context.Background(), "ws-1")
	if err == nil || !strings.Contains(err.Error(), "start it before attaching") {
		t.Fatalf("ResolveAttach error = %v, want stopped-before-attach guidance", err)
	}
}

func TestAgentboxRunnerResolveAttach_TranslatesInvalidAttachState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "workspace_not_attachable",
			"status": "cleanup_required",
		})
	}))
	defer srv.Close()

	runner := NewAgentboxRunner("lab", RemoteConfig{Kind: RemoteKindAgentbox, URL: srv.URL})
	_, err := runner.ResolveAttach(context.Background(), "ws-1")
	if err == nil || !strings.Contains(err.Error(), "cannot be attached") {
		t.Fatalf("ResolveAttach error = %v, want invalid-attach guidance", err)
	}
}

func TestAgentboxRunnerAttachCreatedResult_UsesReturnedCommandsWithoutLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("AttachCreatedResult must not call follow-up attach lookup: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	runner := NewAgentboxRunner("lab", RemoteConfig{Kind: RemoteKindAgentbox, URL: srv.URL})
	var gotCommand string
	runner.execCommand = func(command string) error {
		gotCommand = command
		return nil
	}

	err := runner.AttachCreatedResult(RemoteCreateResult{
		SessionID:          "ws-new",
		Attachable:         true,
		AttachCommand:      "ssh remote-host tmux attach -t ws-new",
		LocalAttachCommand: "tmux attach -t ws-new",
	})
	if err != nil {
		t.Fatalf("AttachCreatedResult unexpected error: %v", err)
	}
	if gotCommand != "tmux attach -t ws-new" {
		t.Fatalf("exec command = %q, want local attach command from create response", gotCommand)
	}
}
