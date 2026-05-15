package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// CLI-level regression tests for issue #961.
//
// Two bugs were reported together:
//   1. `agent-deck rm` driven by `xargs -P N -n 1` prints "✓ Removed" for
//      every input line but a subset of rows persist in the database
//      ("classic SQLite write race").
//   2. `agent-deck session remove <id>` (and `--force <id>`) reports
//      success but never deletes the row; bogus IDs also exit 0.
//
// The structural fix landed in v1.9.1 (#909) — `RemoveSessionAndVerify`
// performs a targeted DELETE, persists groups WITHOUT a load-modify-write
// instances rewrite, and verifies in a backoff loop that the row really
// is gone. The existing storage-level coverage lives in
// internal/session/rm_lifecycle_test.go (TestRm_ParallelDoesNotLoseRemovals).
//
// What was missing — and what these tests add — is regression coverage at
// the CLI-subprocess layer, where every invocation owns its own *sql.DB
// pool. That is the exact contention pattern `xargs -P N agent-deck rm`
// exercises in production. A future refactor that quietly downgrades
// `handleRemove` to the pre-#909 SaveWithGroups path would re-open the
// silent-loss window; these tests catch it at the user-facing surface
// instead of waiting for a memory-note bug report.

// TestAgentDeckRm_ParallelSafe_RegressionFor961 spawns N concurrent
// `agent-deck rm <title>` subprocesses against a shared HOME (= shared
// state.db). Pre-#909 the load-modify-write race would leak ~3/14 rows
// even though every CLI printed "✓ Removed" with exit 0. Post-fix, every
// row must be gone.
func TestAgentDeckRm_ParallelSafe_RegressionFor961(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}

	home := t.TempDir()

	// Seed N sessions. 14 matches the original bug report; the verify-loop
	// in RemoveSessionAndVerify uses up to ~620ms of backoff, so each
	// goroutine has plenty of time to lose to a resurrecting writer if
	// the fix regresses.
	const N = 14
	titles := make([]string, N)
	for i := range N {
		titles[i] = fmt.Sprintf("rm-race-cli-%02d", i)
		workPath := filepath.Join(home, fmt.Sprintf("proj-%02d", i))
		_ = addTestSession(t, home, workPath, titles[i])
	}

	// Confirm seed.
	before := readSessionsJSON(t, home)
	for _, title := range titles {
		if !strings.Contains(before, title) {
			t.Fatalf("seed missing %q in list:\n%s", title, before)
		}
	}

	// Fire N CLIs in parallel. Each is an independent OS process —
	// independent *sql.DB pools, independent storage rewriters.
	bin := channelsCLIBinary(t)
	var wg sync.WaitGroup
	type result struct {
		title  string
		exit   int
		stdout string
		stderr string
		runErr error
	}
	results := make([]result, N)
	start := make(chan struct{})
	for i, title := range titles {
		wg.Add(1)
		go func(i int, title string) {
			defer wg.Done()
			<-start
			cmd := exec.Command(bin, "rm", title, "--json")
			cmd.Env = cliEnvForIssue961(home)
			var outBuf, errBuf strings.Builder
			cmd.Stdout = &outBuf
			cmd.Stderr = &errBuf
			err := cmd.Run()
			r := result{title: title, stdout: outBuf.String(), stderr: errBuf.String()}
			if exitErr, ok := err.(*exec.ExitError); ok {
				r.exit = exitErr.ExitCode()
			} else if err != nil {
				r.runErr = err
			}
			results[i] = r
		}(i, title)
	}
	close(start)
	wg.Wait()

	// Every rm must have reported success at exit-code level. The pre-#909
	// failure mode is "exit 0 + ✓ Removed + row still in DB" — exit 0 alone
	// is not enough; we re-check the registry below.
	for _, r := range results {
		if r.runErr != nil {
			t.Fatalf("rm %q: run error: %v\nstdout: %s\nstderr: %s", r.title, r.runErr, r.stdout, r.stderr)
		}
		if r.exit != 0 {
			t.Fatalf("rm %q: exit %d\nstdout: %s\nstderr: %s", r.title, r.exit, r.stdout, r.stderr)
		}
	}

	// Independent read: every row must be gone. This is the assertion the
	// pre-#909 path silently violated.
	after := readSessionsJSON(t, home)
	var listed []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(after)), &listed); err != nil {
		// `agent-deck list --json` prints a non-JSON banner when the registry
		// is empty ("No sessions found in profile '<p>'."); treat as zero.
		if !strings.Contains(after, "No sessions found") {
			t.Fatalf("parse list --json: %v\noutput: %s", err, after)
		}
		listed = nil
	}
	if len(listed) != 0 {
		survivors := make([]string, 0, len(listed))
		for _, row := range listed {
			if t, _ := row["title"].(string); t != "" {
				survivors = append(survivors, t)
			}
		}
		t.Fatalf("expected 0 surviving sessions after parallel rm, got %d: %v\nlist:\n%s",
			len(listed), survivors, after)
	}
}

// TestSessionRemove_NoOpExitsNonZero_RegressionFor961 pins the
// not-found contract: `agent-deck session remove <bogus-id>` must NOT
// print success + exit 0 when the row is absent. The original bug
// report explicitly called out that `session remove` "reports success
// but never deletes". Post-#909 the resolver returns NOT_FOUND and the
// CLI exits 2 with a structured error payload.
//
// We assert three things together because they form one contract:
//  1. Non-zero exit (specifically 2 — convention shared with
//     `session stop` for not-found).
//  2. A NOT_FOUND error code in the JSON envelope.
//  3. No "✓ Removed" / success=true leakage on stdout.
func TestSessionRemove_NoOpExitsNonZero_RegressionFor961(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}

	home := t.TempDir()

	// Bogus id — nothing was ever added under this HOME.
	stdout, stderr, code := runAgentDeck(t, home,
		"session", "remove", "does-not-exist-961", "--json",
	)

	if code == 0 {
		t.Fatalf("expected non-zero exit for missing id, got 0\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}
	if code != 2 {
		t.Errorf("expected exit code 2 (NOT_FOUND convention), got %d", code)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil {
		t.Fatalf("parse error envelope: %v\nstdout: %s", err, stdout)
	}
	if success, _ := env["success"].(bool); success {
		t.Errorf("error envelope marked success=true; envelope: %v", env)
	}
	if code, _ := env["code"].(string); code != "NOT_FOUND" {
		t.Errorf("expected code=NOT_FOUND, got %q; envelope: %v", code, env)
	}
	if msg, _ := env["error"].(string); !strings.Contains(strings.ToLower(msg), "not found") {
		t.Errorf("expected error message to mention 'not found', got %q", msg)
	}

	// Also verify the --force variant doesn't bypass the not-found check.
	// Pre-#909 the failure mode the bug report named was "session remove
	// --force ... reports success but does not delete"; that path now
	// must also surface NOT_FOUND on a bogus id.
	stdout2, stderr2, code2 := runAgentDeck(t, home,
		"session", "remove", "does-not-exist-961", "--force", "--json",
	)
	if code2 == 0 {
		t.Fatalf("session remove --force on missing id should be non-zero; got 0\nstdout: %s\nstderr: %s",
			stdout2, stderr2)
	}
}

// cliEnvForIssue961 mirrors runAgentDeck's env scrubbing without
// requiring a *testing.T — needed inside the parallel goroutines above.
// We deliberately re-use the same fixed profile name as runAgentDeck
// ("ch_support_test") so all parallel CLIs hit the same state.db —
// that is the entire point of the race reproducer.
func cliEnvForIssue961(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") ||
			strings.HasPrefix(kv, "AGENTDECK_") ||
			strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"AGENTDECK_PROFILE=ch_support_test",
		"TERM=dumb",
	)
	return env
}
