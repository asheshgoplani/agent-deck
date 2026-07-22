package cchook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Name           string `json:"name"`
}

type hookResult struct {
	Level  Level
	Output string
	Err    error
}

// ExecuteCreate fires all hooks in parallel and returns the path from the
// highest-priority hook that exited 0 with non-empty output.
func ExecuteCreate(ctx context.Context, hooks *ResolvedHooks, payload Payload, timeout time.Duration) (string, error) {
	results := executeAll(ctx, hooks, payload, timeout)

	// Walk entries in priority order (they're already sorted user > project > local > managed).
	// Return the path from the first (highest-priority) entry.
	for i, entry := range hooks.Entries {
		r := results[i]
		if r.Err != nil {
			return "", fmt.Errorf("WorktreeCreate hook (%s) failed: %w", entry.Level, r.Err)
		}
		path := strings.TrimSpace(r.Output)
		if path == "" {
			return "", fmt.Errorf("WorktreeCreate hook (%s) produced no output", entry.Level)
		}
		return path, nil
	}

	return "", fmt.Errorf("no WorktreeCreate hook produced a result")
}

// ExecuteRemove fires all hooks in parallel. Failures are logged to stderr
// but do not prevent removal (non-blocking).
func ExecuteRemove(ctx context.Context, hooks *ResolvedHooks, payload Payload, timeout time.Duration, stderr io.Writer) {
	results := executeAll(ctx, hooks, payload, timeout)
	for i, r := range results {
		if r.Err != nil {
			fmt.Fprintf(stderr, "WorktreeRemove hook (%s) failed: %v\n", hooks.Entries[i].Level, r.Err)
		}
	}
}

func executeAll(ctx context.Context, hooks *ResolvedHooks, payload Payload, timeout time.Duration) []hookResult {
	payloadJSON, _ := json.Marshal(payload)

	var wg sync.WaitGroup
	results := make([]hookResult, len(hooks.Entries))

	for i, entry := range hooks.Entries {
		wg.Add(1)
		go func(idx int, e HookEntry) {
			defer wg.Done()
			results[idx] = runHook(ctx, e, payloadJSON, timeout)
		}(i, entry)
	}

	wg.Wait()
	return results
}

func runHook(ctx context.Context, entry HookEntry, payloadJSON []byte, timeout time.Duration) hookResult {
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", entry.Command)
	cmd.Stdin = bytes.NewReader(payloadJSON)
	cmd.WaitDelay = 5 * time.Second

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	return hookResult{
		Level:  entry.Level,
		Output: stdout.String(),
		Err:    err,
	}
}
