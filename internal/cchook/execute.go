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
	SessionID string `json:"session_id"`
	// TranscriptPath exists for schema parity with Claude Code's hook payload;
	// agent-deck does not have access to CC's transcript path, so this is always empty.
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
// highest-priority hook (user > project > local > managed). All hooks run
// to completion, but only the winner's result matters — if it fails or
// produces no output, creation fails (no fallback to lower-priority hooks).
func ExecuteCreate(ctx context.Context, hooks *ResolvedHooks, payload Payload, timeout time.Duration) (string, error) {
	results := executeAll(ctx, hooks, payload, timeout)

	winnerResult := results[0]
	if winnerResult.Err != nil {
		return "", fmt.Errorf("WorktreeCreate hook (%s) failed: %w", winnerResult.Level, winnerResult.Err)
	}
	path := strings.TrimSpace(winnerResult.Output)
	if path == "" {
		return "", fmt.Errorf("WorktreeCreate hook (%s) produced no output", winnerResult.Level)
	}
	return path, nil
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
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("cchook: marshal payload: %v", err))
	}

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

	cmd := exec.CommandContext(ctx, "bash", "-c", entry.Command) //nolint:gosec // hook commands are read from CC settings files, executing them is the purpose
	cmd.Stdin = bytes.NewReader(payloadJSON)
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return hookResult{
		Level:  entry.Level,
		Output: stdout.String(),
		Err:    err,
	}
}
