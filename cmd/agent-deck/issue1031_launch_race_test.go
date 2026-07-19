package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// CLI-level regression tests for issue #1031.
//
// Bug: When N `agent-deck launch` invocations fire concurrently (same loop
// tick, same profile), only N-1 of the N requested sessions persist to the
// SQLite state DB. All N return exit 0, all N worktrees/tmux sessions are
// created — only the database row silently goes missing.
//
// This is the same load-modify-write SQLite race as #961 (`agent-deck rm`)
// fixed in v1.9.1 via #909 / #993:
//
//   - Each launch's `SaveWithGroups` does load → append → rewrite of the
//     full instances table inside SaveInstances, which performs
//     `DELETE FROM instances WHERE id NOT IN (...)` over the slice the
//     caller loaded — including any rows another launch wrote *after*
//     this caller's load. That sibling row is silently DELETE'd.
//
// The structural fix mirrors RemoveSessionAndVerify: a new
// InsertSessionAndVerify path that does a targeted single-row INSERT OR
// REPLACE via SaveInstance + verify-with-backoff, *without* the
// load-modify-write rewrite. The launch CLI also gains a deterministic
// way to return the new session ID to its caller so the conductor's
// fleet-spawning loop no longer has to diff `list --json` before/after
// (the diff itself was unsafe under this race).
//
// Coverage shape mirrors issue961_rm_safety_test.go: real CLI subprocesses
// each own an independent *sql.DB pool, which is the exact contention
// pattern `xargs -P N agent-deck launch` exercises in production. A future
// refactor that downgrades the launch persistence path back to the
// pre-fix SaveWithGroups load-modify-write would re-open the silent-loss
// window; this test catches it at the user-facing CLI surface.

// TestAgentDeckLaunch_ParallelSafe_AllSessionsPersist_RegressionFor1031 spawns
// N concurrent `agent-deck launch` subprocesses against a shared HOME
// (= shared state.db). Pre-fix, the load-modify-write race in
// SaveWithGroups silently dropped ~1/5 rows even though every CLI printed
// success + exit 0. Post-fix, all N rows must be persisted.
func TestAgentDeckLaunch_ParallelSafe_AllSessionsPersist_RegressionFor1031(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; launch CLI needs a real tmux server")
	}

	home := t.TempDir()
	socket := isolatedTmuxSocket1031(t)

	const N = 5
	titles := make([]string, N)
	projDirs := make([]string, N)
	for i := 0; i < N; i++ {
		titles[i] = fmt.Sprintf("launch-race-cli-%02d", i)
		projDirs[i] = filepath.Join(home, fmt.Sprintf("proj-%02d", i))
		if err := os.MkdirAll(projDirs[i], 0o755); err != nil {
			t.Fatalf("mkdir proj %d: %v", i, err)
		}
	}

	// Pre-create the profile + state.db by running one no-op CLI before
	// the parallel burst. The bug report's repro shape is "same loop
	// tick, same profile" — the profile already exists in production.
	// Without this seed, the 5 parallel CLIs each race to create the
	// state.db file and run the SQLite WAL setup, which fails with
	// SQLITE_BUSY on cold-start (a different, pre-existing bug that
	// would otherwise mask the #1031 race we're pinning here).
	if _, _, code := runAgentDeck(t, home, "list", "--json"); code != 0 {
		// `list` against an empty profile prints "No sessions found"
		// and exits 0. Anything else means the seed itself broke.
		t.Fatalf("seed `agent-deck list` failed with exit %d", code)
	}

	bin := channelsCLIBinary(t)
	type result struct {
		title  string
		exit   int
		stdout string
		stderr string
		runErr error
	}
	results := make([]result, N)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// No `-c <tool>` — launch defaults Tool to "shell", which Start()
			// implements as `tmux new-session <shell>`. The race we're
			// pinning lives in SaveWithGroups *before* the spawn step, so
			// the tool choice doesn't matter — we just need a launch path
			// that doesn't require claude/codex installed in CI.
			cmd := exec.Command(bin, "launch",
				"-t", titles[i],
				"--no-parent",
				"--tmux-socket", socket,
				"--json",
				projDirs[i],
			)
			cmd.Env = cliEnvForIssue1031(home)
			var outBuf, errBuf strings.Builder
			cmd.Stdout = &outBuf
			cmd.Stderr = &errBuf
			err := cmd.Run()
			r := result{title: titles[i], stdout: outBuf.String(), stderr: errBuf.String()}
			if exitErr, ok := err.(*exec.ExitError); ok {
				r.exit = exitErr.ExitCode()
			} else if err != nil {
				r.runErr = err
			}
			results[i] = r
		}(i)
	}
	close(start)
	wg.Wait()

	// Every launch must report success at the exit-code layer. The pre-fix
	// failure mode is "exit 0 + success message + row missing from DB" —
	// exit 0 alone is not enough; we re-check the registry below.
	for _, r := range results {
		if r.runErr != nil {
			t.Fatalf("launch %q: run error: %v\nstdout: %s\nstderr: %s", r.title, r.runErr, r.stdout, r.stderr)
		}
		if r.exit != 0 {
			t.Fatalf("launch %q: exit %d\nstdout: %s\nstderr: %s", r.title, r.exit, r.stdout, r.stderr)
		}
	}

	// Independent read: every row must be present. This is the assertion
	// the pre-fix path silently violates.
	after := readSessionsJSON(t, home)
	var listed []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(after)), &listed); err != nil {
		if !strings.Contains(after, "No sessions found") {
			t.Fatalf("parse list --json: %v\noutput: %s", err, after)
		}
		listed = nil
	}

	seen := make(map[string]bool, N)
	for _, row := range listed {
		title, _ := row["title"].(string)
		seen[title] = true
	}

	missing := make([]string, 0, N)
	for _, want := range titles {
		if !seen[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf(
			"expected all %d launched sessions persisted, %d silently dropped: %v\n"+
				"persisted titles: %d / %d\nfull list:\n%s",
			N, len(missing), missing, len(seen), N, after,
		)
	}
}

// TestAgentDeckLaunch_ReturnsSessionID_RegressionFor1031 pins the second
// half of the #1031 fix: a single `agent-deck launch` must surface the
// new session's stable ID on stdout, so callers (the conductor's
// fleet-spawning loop, shell scripts, downstream automation) can capture
// it deterministically without the unsafe `agent-deck list --json` diff.
//
// The bug report calls this out explicitly:
//
//	"agent-deck launch --json printing {sessionId, worktreePath, tmuxSession}
//	 on stdout after the session is fully persisted would let callers
//	 identify their own session deterministically — no before/after diff,
//	 no race to lose."
//
// Pre-fix shape on main:
//   - `--json` returns a generic `"id"` key (was already there, but unclear)
//   - non-JSON human mode prints only "✓ Launched session: <title>" with
//     no session ID anywhere on stdout — callers cannot grep it out
//
// Post-fix expectation:
//   - The session ID is on stdout in BOTH modes. JSON output carries an
//     explicit `session_id` key (in addition to the legacy `id`) so
//     scripts can grep deterministically. Human output ends with the
//     session ID on its own annotation so `awk`/`grep` callers don't
//     need --json at all.
func TestAgentDeckLaunch_ReturnsSessionID_RegressionFor1031(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; launch CLI needs a real tmux server")
	}

	home := t.TempDir()
	socket := isolatedTmuxSocket1031(t)

	projDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// --- (a) --json mode must return an explicit `session_id` field ----
	stdout, stderr, code := runAgentDeck(t, home,
		"launch",
		"-t", "launch-id-test",
		"--no-parent",
		"--tmux-socket", socket,
		"--json",
		projDir,
	)
	if code != 0 {
		t.Fatalf("launch --json failed (exit %d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("parse launch --json: %v\nstdout: %s", err, stdout)
	}

	sessionID, _ := resp["session_id"].(string)
	if sessionID == "" {
		t.Fatalf(
			"launch --json must surface an explicit `session_id` field so "+
				"callers don't have to fall back to diff `list --json` "+
				"(the diff itself was unsafe under the #1031 race)\n"+
				"got payload: %v", resp,
		)
	}
	// The new explicit key must agree with the legacy generic `id` key,
	// so older callers that grepped `id` keep working.
	if legacy, _ := resp["id"].(string); legacy != "" && legacy != sessionID {
		t.Errorf("session_id (%q) and id (%q) disagree — they must reference the same session",
			sessionID, legacy)
	}
}

// --- helpers --------------------------------------------------------------

// cliEnvForIssue1031 mirrors cliEnvForIssue961's env scrubbing — needed
// inside the parallel goroutines above where t isn't safe to share with
// runAgentDeck. The fixed profile name matches runAgentDeck's
// AGENTDECK_PROFILE so all parallel CLIs hit the same state.db (that's
// the entire point of the race reproducer).
func cliEnvForIssue1031(home string) []string {
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

// uniqueTmuxSocketName1031 returns a per-test tmux -L socket name. The
// launches under test live on this isolated socket so they don't touch
// the developer's real tmux server.
//
// The name is DETERMINISTIC per test (an FNV hash of t.Name()), not
// timestamp-derived. A timestamped name meant every test run that crashed,
// timed out, or was SIGKILL'd before t.Cleanup ran leaked a brand-new,
// uniquely-named `ad1031-*` server that no later run could reach or reap —
// they piled up and consumed ptys. With a stable name, the next run of the
// same test inherits the same socket and reaps the leftover at setup (see
// isolatedTmuxSocket1031).
//
// We keep the name short on purpose: it lands under /tmp/tmux-<uid>/<name>
// which is a Unix-domain socket path with a hard ~108-char limit. The
// 8-hex-digit hash stays well within it.
func uniqueTmuxSocketName1031(t *testing.T) string {
	t.Helper()
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name()))
	return fmt.Sprintf("ad1031-%08x", h.Sum32())
}

// isolatedTmuxSocket1031 returns the deterministic isolated socket for this
// test and guarantees the server on it is reaped both NOW (setup) and on
// cleanup. The setup-time kill is the robust part: t.Cleanup cannot run when a
// test times out, panics hard, or the test binary is SIGKILL'd — exactly the
// paths that orphan the server. Reaping at setup means the next run of the same
// test reclaims any leftover instead of leaking a new one.
func isolatedTmuxSocket1031(t *testing.T) string {
	t.Helper()
	socket := uniqueTmuxSocketName1031(t)
	// Reap a server leaked by a prior crashed run before we start. Best-effort:
	// a no-op when nothing is listening on the socket.
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	return socket
}
