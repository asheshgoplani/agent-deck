// session_remove_kill_test.go — regression tests for issue #59 (v1.7.68).
//
// Context: on 2026-04-22 a claude process (PID 321456,
// AGENTDECK_INSTANCE_ID `bcb1d1cc-1776748185`) was found running for
// 33+ hours with no corresponding agent-deck session record. The
// `session remove` code path in v1.7.61 only called `inst.Kill()`
// when the caller also passed `--prune-worktree`. `remove --force`
// alone deleted the registry row but left the tmux scope (and any
// SIGHUP-immune claude process inside it) alive forever.
//
// These tests drive the built binary against a real tmux server on a
// per-test socket (tests/eval/harness) and assert what the user would
// see afterwards: whether the tmux session and its pane process still
// exist, whether the registry row is gone, and whether a git worktree
// directory survived.
//
//  1. `session remove --force` kills the live tmux scope, not only the row.
//  2. `session remove --all-errored` kills each errored session it removes.
//  3. `session cleanup --yes` keeps worktree directories unless
//     --prune-worktree is passed.
//  4. `session cleanup` re-probes liveness after the [y/N] prompt, so a
//     session revived while the prompt is open is neither killed nor
//     removed.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// removeSandbox is a harness sandbox whose tmux calls all land on the
// sandbox's own socket.
func removeSandbox(t *testing.T) *harness.Sandbox {
	t.Helper()
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	sb := harness.NewSandbox(t)
	sb.InstallTmuxShim(t)
	return sb
}

// runRemoveCLI runs the binary in the sandbox with a 60 s bound.
func runRemoveCLI(t *testing.T, sb *harness.Sandbox, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.BinPath, args...)
	cmd.Env = sb.Env()
	cmd.Dir = sb.Home
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("agent-deck %v did not finish in 60s:\n%s", args, out)
	}
	return string(out), err
}

// startLiveShellSession registers a bash session, starts it, and returns
// its id, its tmux session name and the pid of the process in its pane.
func startLiveShellSession(t *testing.T, sb *harness.Sandbox, title string) (id, tmuxName string, panePID int) {
	t.Helper()
	work := filepath.Join(sb.Home, "work-"+title)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runRemoveCLI(t, sb, "add", "-c", "bash", "-t", title, "--no-parent", "--json", work)
	if err != nil {
		t.Fatalf("add %s: %v\n%s", title, err, out)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		t.Fatalf("add %s: no id in %q (%v)", title, out, err)
	}
	if out, err := runRemoveCLI(t, sb, "session", "start", title); err != nil {
		t.Fatalf("session start %s: %v\n%s", title, err, out)
	}
	row := loadSandboxRow(t, sb, added.ID)
	if row == nil || row.TmuxSession == "" {
		t.Fatalf("session %s has no tmux session after start: %+v", title, row)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		pid, err := sb.TmuxTry("display-message", "-p", "-t", row.TmuxSession+":", "#{pane_pid}")
		if n, convErr := strconv.Atoi(strings.TrimSpace(pid)); err == nil && convErr == nil && n > 0 {
			return added.ID, row.TmuxSession, n
		}
		if time.Now().After(deadline) {
			sessions, _ := sb.TmuxTry("list-sessions")
			t.Fatalf("tmux session %s for %s never came up (last: %q %v)\nlist-sessions: %s", row.TmuxSession, title, pid, err, sessions)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func sandboxStateDB(t *testing.T, sb *harness.Sandbox) *statedb.StateDB {
	t.Helper()
	// The XDG data dir is where a fresh home keeps its profiles.
	dir := filepath.Join(sb.Home, ".local", "share", "agent-deck", "profiles", "default")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

// loadSandboxRow reads one session row straight from the sandbox's
// state.db; nil means the row is gone.
func loadSandboxRow(t *testing.T, sb *harness.Sandbox, id string) *statedb.InstanceRow {
	t.Helper()
	db := sandboxStateDB(t, sb)
	defer db.Close()
	row, err := db.LoadInstanceByID(id)
	if err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	return row
}

func setSandboxRowStatus(t *testing.T, sb *harness.Sandbox, id, status string) {
	t.Helper()
	db := sandboxStateDB(t, sb)
	defer db.Close()
	row, err := db.LoadInstanceByID(id)
	if err != nil || row == nil {
		t.Fatalf("load %s: %+v %v", id, row, err)
	}
	row.Status = status
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
}

// startTmuxServer starts an unrelated session so the sandbox tmux server is
// up: cleanup treats a socket it cannot list as "maybe alive" and never
// purges from it, so a dead session is only judged dead against a running
// server, as on a real deck.
func startTmuxServer(sb *harness.Sandbox) {
	sb.Tmux("new-session", "-d", "-s", "keepalive", "sleep 600")
}

// seedDeadSession writes a session row whose tmux session does not exist
// and whose last activity is 90 days old: a `session cleanup` candidate.
func seedDeadSession(t *testing.T, sb *harness.Sandbox, id, worktree, repo string) {
	t.Helper()
	db := sandboxStateDB(t, sb)
	defer db.Close()
	old := time.Now().Add(-90 * 24 * time.Hour)
	row := &statedb.InstanceRow{
		ID: id, Title: id, ProjectPath: sb.Home, GroupPath: "cleanup", Tool: "shell", Command: "bash",
		Status: "error", TmuxSession: "agentdeck_" + strings.ReplaceAll(id, "-", "_"),
		CreatedAt: old, LastAccessed: old,
		WorktreePath: worktree, WorktreeRepo: repo, WorktreeBranch: id,
	}
	if worktree == "" {
		row.WorktreeBranch = ""
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
}

func tmuxSessionAlive(sb *harness.Sandbox, name string) bool {
	_, err := sb.TmuxTry("has-session", "-t", name)
	return err == nil
}

// processGone reports whether pid no longer exists (or is a zombie the
// tmux server has not reaped yet).
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return os.IsNotExist(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}

// waitKilled waits up to 10 s for the tmux session and its pane process to
// disappear.
func waitKilled(t *testing.T, sb *harness.Sandbox, name string, pid int, why string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for tmuxSessionAlive(sb, name) || !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: tmux session %s (alive=%v) or pane pid %d (gone=%v) survived — "+
				"the registry row is gone but the process keeps running (issue #59)",
				why, name, tmuxSessionAlive(sb, name), pid, processGone(pid))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// `session remove --force` on a running session must kill its tmux scope
// and the process in it, not only delete the row. Before issue #59 the
// kill only ran with --prune-worktree.
func TestSessionRemove_ForceKillsLiveTmuxSession(t *testing.T) {
	sb := removeSandbox(t)
	id, name, pid := startLiveShellSession(t, sb, "live")
	if out, err := runRemoveCLI(t, sb, "session", "remove", "--force", "live"); err != nil {
		t.Fatalf("session remove --force: %v\n%s", err, out)
	}
	if row := loadSandboxRow(t, sb, id); row != nil {
		t.Fatalf("row still present after remove --force: %+v", row)
	}
	waitKilled(t, sb, name, pid, "session remove --force")
}

// The --all-errored bulk path must kill every session it removes and leave
// the others (here: a running one) alone.
func TestSessionRemove_AllErroredKillsEachRemovedSession(t *testing.T) {
	sb := removeSandbox(t)
	idA, nameA, pidA := startLiveShellSession(t, sb, "err-a")
	idB, nameB, pidB := startLiveShellSession(t, sb, "err-b")
	idKeep, nameKeep, pidKeep := startLiveShellSession(t, sb, "keep")
	setSandboxRowStatus(t, sb, idA, "error")
	setSandboxRowStatus(t, sb, idB, "error")

	if out, err := runRemoveCLI(t, sb, "session", "remove", "--all-errored", "--json"); err != nil {
		t.Fatalf("session remove --all-errored: %v\n%s", err, out)
	}
	for _, id := range []string{idA, idB} {
		if row := loadSandboxRow(t, sb, id); row != nil {
			t.Fatalf("errored row %s still present: %+v", id, row)
		}
	}
	waitKilled(t, sb, nameA, pidA, "session remove --all-errored (err-a)")
	waitKilled(t, sb, nameB, pidB, "session remove --all-errored (err-b)")

	if loadSandboxRow(t, sb, idKeep) == nil {
		t.Fatal("--all-errored removed a session that was not in error state")
	}
	if !tmuxSessionAlive(sb, nameKeep) || processGone(pidKeep) {
		t.Fatal("--all-errored killed a session that was not in error state")
	}
}

// newSandboxWorktree makes a git repo with one commit under the sandbox
// home and a linked worktree of it; it returns (repo, worktree).
func newSandboxWorktree(t *testing.T, sb *harness.Sandbox, name string) (string, string) {
	t.Helper()
	repo := filepath.Join(sb.Home, "repo-"+name)
	wt := filepath.Join(sb.Home, "wt-"+name)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(sb.Env(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "init")
	git(repo, "worktree", "add", "-q", "-b", name, wt)
	return repo, wt
}

// `session cleanup` is registry-only: --yes alone must remove the dead row
// but keep its git worktree directory (uncommitted work lives there). Only
// an explicit --prune-worktree deletes it.
func TestSessionCleanup_KeepsWorktreeUnlessPruneRequested(t *testing.T) {
	sb := removeSandbox(t)
	startTmuxServer(sb)
	repoKeep, wtKeep := newSandboxWorktree(t, sb, "cleanup-keep")
	repoPrune, wtPrune := newSandboxWorktree(t, sb, "cleanup-prune")
	if err := os.WriteFile(filepath.Join(wtKeep, "uncommitted.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	seedDeadSession(t, sb, "cleanup-keep", wtKeep, repoKeep)
	if out, err := runRemoveCLI(t, sb, "session", "cleanup", "--yes"); err != nil {
		t.Fatalf("session cleanup --yes: %v\n%s", err, out)
	}
	if row := loadSandboxRow(t, sb, "cleanup-keep"); row != nil {
		t.Fatalf("dead row still present after cleanup --yes: %+v", row)
	}
	if _, err := os.Stat(filepath.Join(wtKeep, "uncommitted.txt")); err != nil {
		t.Fatalf("cleanup --yes without --prune-worktree deleted the worktree: %v", err)
	}

	seedDeadSession(t, sb, "cleanup-prune", wtPrune, repoPrune)
	if out, err := runRemoveCLI(t, sb, "session", "cleanup", "--yes", "--prune-worktree"); err != nil {
		t.Fatalf("session cleanup --yes --prune-worktree: %v\n%s", err, out)
	}
	if _, err := os.Stat(wtPrune); !os.IsNotExist(err) {
		t.Fatalf("cleanup --prune-worktree kept the worktree (stat err %v)", err)
	}
}

// The [y/N] prompt can stay open for minutes. A candidate that comes back to
// life meanwhile must survive: cleanup re-probes liveness after the answer
// and before any kill or delete.
func TestSessionCleanup_SkipsSessionRevivedDuringPrompt(t *testing.T) {
	sb := removeSandbox(t)
	startTmuxServer(sb)
	seedDeadSession(t, sb, "revived", "", "")
	name := "agentdeck_revived"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.BinPath, "session", "cleanup")
	cmd.Env = sb.Env()
	cmd.Dir = sb.Home
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	prompt := regexp.MustCompile(`Delete 1 session\(s\)\? \[y/N\]`)
	var seen strings.Builder
	r := bufio.NewReader(stdout)
	buf := make([]byte, 256)
	for !prompt.MatchString(seen.String()) {
		n, rerr := r.Read(buf)
		seen.Write(buf[:n])
		if rerr != nil {
			_ = cmd.Wait()
			t.Fatalf("cleanup never asked for confirmation (%v):\n%s\nstderr: %s", rerr, seen.String(), stderr.String())
		}
	}

	// Revive the candidate while the prompt is open, then answer yes.
	sb.Tmux("new-session", "-d", "-s", name, "sleep 600")
	pid, err := strconv.Atoi(strings.TrimSpace(sb.Tmux("display-message", "-p", "-t", name+":", "#{pane_pid}")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, "y\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	rest, _ := io.ReadAll(r)
	seen.Write(rest)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cleanup: %v\nstdout: %s\nstderr: %s", err, seen.String(), stderr.String())
	}

	if row := loadSandboxRow(t, sb, "revived"); row == nil {
		t.Fatalf("a session revived during the prompt was removed\nstdout: %s\nstderr: %s", seen.String(), stderr.String())
	}
	if !tmuxSessionAlive(sb, name) || processGone(pid) {
		t.Fatalf("a session revived during the prompt was killed\nstdout: %s\nstderr: %s", seen.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "came back to life") {
		t.Errorf("cleanup did not report the revived session on stderr: %q", stderr.String())
	}
}

// extractFuncBody returns the body of a named Go function from source.
// Finds `func <name>` then walks to the next top-level `{` and does
// brace-counting to the matching `}`. Handles multi-line signatures.
// Still used by other structural tests in this package.
func extractFuncBody(src, fnName string) string {
	re := regexp.MustCompile(`(?m)^func[^\n]*\b` + regexp.QuoteMeta(fnName) + `\s*\(`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	i := loc[1]
	for i < len(src) && src[i] != '{' {
		i++
	}
	if i == len(src) {
		return ""
	}
	start := i + 1
	depth := 1
	for j := start; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start:j]
			}
		}
	}
	return ""
}
