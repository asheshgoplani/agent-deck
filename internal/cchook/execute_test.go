package cchook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/cchook"
)

func TestExecuteCreate_SingleHook_ReturnsPath(t *testing.T) {
	ctx := context.Background()
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'echo /tmp/test-path'`,
				Level:   cchook.LevelProject,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeCreate",
		Name:           "test",
	}

	path, err := cchook.ExecuteCreate(ctx, hooks, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("ExecuteCreate failed: %v", err)
	}
	if path != "/tmp/test-path" {
		t.Fatalf("got path %q, want /tmp/test-path", path)
	}
}

func TestExecuteCreate_MultipleHooks_HighestPriorityWins(t *testing.T) {
	ctx := context.Background()
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'echo /user/path'`,
				Level:   cchook.LevelUser,
			},
			{
				Command: `bash -c 'echo /project/path'`,
				Level:   cchook.LevelProject,
			},
			{
				Command: `bash -c 'echo /managed/path'`,
				Level:   cchook.LevelManaged,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeCreate",
		Name:           "test",
	}

	path, err := cchook.ExecuteCreate(ctx, hooks, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("ExecuteCreate failed: %v", err)
	}
	if path != "/user/path" {
		t.Fatalf("got path %q, want /user/path (highest priority)", path)
	}
}

func TestExecuteCreate_WinnerFails_ReturnsError(t *testing.T) {
	ctx := context.Background()
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'exit 1'`,
				Level:   cchook.LevelUser,
			},
			{
				Command: `bash -c 'echo /project/path'`,
				Level:   cchook.LevelProject,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeCreate",
		Name:           "test",
	}

	_, err := cchook.ExecuteCreate(ctx, hooks, payload, 5*time.Second)
	if err == nil {
		t.Fatal("expected ExecuteCreate to return error when highest-priority hook fails")
	}
	if !strings.Contains(err.Error(), "WorktreeCreate hook") || !strings.Contains(err.Error(), "user") {
		t.Fatalf("error message should mention WorktreeCreate hook and user level, got: %v", err)
	}
}

func TestExecuteCreate_WinnerNoOutput_ReturnsError(t *testing.T) {
	ctx := context.Background()
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'echo ""'`,
				Level:   cchook.LevelUser,
			},
			{
				Command: `bash -c 'echo /project/path'`,
				Level:   cchook.LevelProject,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeCreate",
		Name:           "test",
	}

	_, err := cchook.ExecuteCreate(ctx, hooks, payload, 5*time.Second)
	if err == nil {
		t.Fatal("expected ExecuteCreate to return error when highest-priority hook produces no output")
	}
	if !strings.Contains(err.Error(), "no output") || !strings.Contains(err.Error(), "user") {
		t.Fatalf("error message should mention no output and user level, got: %v", err)
	}
}

func TestExecuteCreate_Timeout(t *testing.T) {
	ctx := context.Background()
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'sleep 10'`,
				Level:   cchook.LevelUser,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeCreate",
		Name:           "test",
	}

	_, err := cchook.ExecuteCreate(ctx, hooks, payload, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected ExecuteCreate to return error when hook exceeds timeout")
	}
}

func TestExecuteCreate_AllHooksRunToCompletion(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "marker")

	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'echo /user/path'`,
				Level:   cchook.LevelUser,
			},
			{
				Command: `bash -c 'touch "` + markerFile + `" && echo /project/path'`,
				Level:   cchook.LevelProject,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeCreate",
		Name:           "test",
	}

	path, err := cchook.ExecuteCreate(ctx, hooks, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("ExecuteCreate failed: %v", err)
	}
	if path != "/user/path" {
		t.Fatalf("got path %q, want /user/path", path)
	}

	// Even though the user hook won, the project hook should have run to completion
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("marker file was not created, project hook did not run to completion: %v", err)
	}
}

func TestExecuteCreate_PayloadPassedToHook(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.json")

	// Hook that reads stdin and writes it to a file
	command := `bash -c 'cat > "` + outputFile + `" && echo /test/path'`
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: command,
				Level:   cchook.LevelProject,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session-123",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/home/test",
		HookEventName:  "WorktreeCreate",
		Name:           "myworktree",
	}

	_, err := cchook.ExecuteCreate(ctx, hooks, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("ExecuteCreate failed: %v", err)
	}

	// Verify payload was passed correctly
	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var receivedPayload cchook.Payload
	if err := json.Unmarshal(data, &receivedPayload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if receivedPayload.SessionID != payload.SessionID {
		t.Fatalf("SessionID mismatch: got %q, want %q", receivedPayload.SessionID, payload.SessionID)
	}
	if receivedPayload.TranscriptPath != payload.TranscriptPath {
		t.Fatalf("TranscriptPath mismatch: got %q, want %q", receivedPayload.TranscriptPath, payload.TranscriptPath)
	}
	if receivedPayload.Cwd != payload.Cwd {
		t.Fatalf("Cwd mismatch: got %q, want %q", receivedPayload.Cwd, payload.Cwd)
	}
	if receivedPayload.HookEventName != payload.HookEventName {
		t.Fatalf("HookEventName mismatch: got %q, want %q", receivedPayload.HookEventName, payload.HookEventName)
	}
	if receivedPayload.Name != payload.Name {
		t.Fatalf("Name mismatch: got %q, want %q", receivedPayload.Name, payload.Name)
	}
}

func TestExecuteRemove_NonBlocking(t *testing.T) {
	ctx := context.Background()
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'exit 1'`,
				Level:   cchook.LevelUser,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeRemove",
		Name:           "test",
	}

	var stderr bytes.Buffer
	cchook.ExecuteRemove(ctx, hooks, payload, 5*time.Second, &stderr)

	// Should not panic and should log error to stderr
	stderrOutput := stderr.String()
	if stderrOutput == "" {
		t.Fatal("expected ExecuteRemove to log error to stderr")
	}
	if !strings.Contains(stderrOutput, "WorktreeRemove hook") || !strings.Contains(stderrOutput, "user") {
		t.Fatalf("stderr should mention WorktreeRemove hook and user level, got: %s", stderrOutput)
	}
}

func TestExecuteRemove_AllHooksRunToCompletion(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "marker")

	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{
			{
				Command: `bash -c 'exit 1'`,
				Level:   cchook.LevelUser,
			},
			{
				Command: `bash -c 'touch "` + markerFile + `"'`,
				Level:   cchook.LevelProject,
			},
		},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeRemove",
		Name:           "test",
	}

	var stderr bytes.Buffer
	cchook.ExecuteRemove(ctx, hooks, payload, 5*time.Second, &stderr)

	// Even though the user hook failed, the project hook should have run to completion
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("marker file was not created, project hook did not run to completion: %v", err)
	}
}

func TestExecuteRemove_NoHooks(t *testing.T) {
	ctx := context.Background()
	hooks := &cchook.ResolvedHooks{
		Entries: []cchook.HookEntry{},
	}

	payload := cchook.Payload{
		SessionID:      "test-session",
		TranscriptPath: "/tmp/transcript.jsonl",
		Cwd:            "/tmp",
		HookEventName:  "WorktreeRemove",
		Name:           "test",
	}

	var stderr bytes.Buffer
	cchook.ExecuteRemove(ctx, hooks, payload, 5*time.Second, &stderr)

	// Should handle empty hooks gracefully
	stderrOutput := stderr.String()
	if stderrOutput != "" {
		t.Fatalf("expected no stderr output with empty hooks, got: %s", stderrOutput)
	}
}
