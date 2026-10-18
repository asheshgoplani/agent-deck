package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #2683: Claude Code's agent view moves a backgrounded conversation to a
// new session id and ends the old transcript with a continued-in record. A
// restart must resume the successor, not reopen the stalled pre-handoff copy.

func setupContinuedInProject(t *testing.T) (projectPath, projectsDir string) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	projectPath = filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	projectsDir = filepath.Join(tmpHome, ".claude", "projects", ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return projectPath, projectsDir
}

func writeContinuedInTranscript(t *testing.T, dir, id, continuedIn string) {
	t.Helper()
	body := `{"type":"user","sessionId":"` + id + `","message":{"role":"user","content":"hi"}}` + "\n"
	if continuedIn != "" {
		body += `{"type":"continued-in","continuedInSessionId":"` + continuedIn + `"}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildClaudeResumeCommand_FollowsContinuedInChain(t *testing.T) {
	projectPath, dir := setupContinuedInProject(t)
	const (
		oldID = "11111111-1111-4111-8111-111111111111"
		midID = "22222222-2222-4222-8222-222222222222"
		newID = "33333333-3333-4333-8333-333333333333"
	)
	writeContinuedInTranscript(t, dir, oldID, midID)
	writeContinuedInTranscript(t, dir, midID, newID)
	writeContinuedInTranscript(t, dir, newID, "")

	inst := NewInstance("handoff", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = oldID

	cmd := inst.buildClaudeResumeCommand()
	if !strings.Contains(cmd, "--resume "+newID) {
		t.Fatalf("restart must resume the newest continued-in successor %s, got %q", newID, cmd)
	}
	if inst.ClaudeSessionID != newID {
		t.Fatalf("binding must move to the successor, got %q", inst.ClaudeSessionID)
	}
}

func TestBuildClaudeResumeCommand_ContinuedInSuccessorMustBeInSameProject(t *testing.T) {
	projectPath, dir := setupContinuedInProject(t)
	const (
		oldID     = "44444444-4444-4444-8444-444444444444"
		foreignID = "55555555-5555-4555-8555-555555555555"
	)
	writeContinuedInTranscript(t, dir, oldID, foreignID)
	other := filepath.Join(filepath.Dir(dir), "-elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	writeContinuedInTranscript(t, other, foreignID, "")

	inst := NewInstance("handoff-foreign", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = oldID

	cmd := inst.buildClaudeResumeCommand()
	if !strings.Contains(cmd, "--resume "+oldID) || inst.ClaudeSessionID != oldID {
		t.Fatalf("a successor outside the project dir must not be followed, got %q (id %q)", cmd, inst.ClaudeSessionID)
	}
}

func TestBuildClaudeResumeCommand_ContinuedInCycleIsBounded(t *testing.T) {
	projectPath, dir := setupContinuedInProject(t)
	const (
		aID = "66666666-6666-4666-8666-666666666666"
		bID = "77777777-7777-4777-8777-777777777777"
	)
	writeContinuedInTranscript(t, dir, aID, bID)
	writeContinuedInTranscript(t, dir, bID, aID)

	inst := NewInstance("handoff-cycle", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = aID

	cmd := inst.buildClaudeResumeCommand()
	if !strings.Contains(cmd, "--resume ") {
		t.Fatalf("a cyclic chain must still resume something, got %q", cmd)
	}
}

// Codex P1 on #2683: the continued-in rebind happens inside
// buildClaudeResumeCommand, and the existing-pane Restart path (RespawnPane)
// never re-runs the start-time SetEnvironment or a Save. The rebind must
// therefore be written to SQLite (a fresh load must see the successor) and to
// the pane's CLAUDE_SESSION_ID (so RefreshLiveSessionIDs cannot read the old
// id back and revert the binding).
func TestFollowClaudeContinuedIn_PersistsAndPublishesSuccessor(t *testing.T) {
	skipIfNoTmuxBinary(t)
	projectPath, dir := setupContinuedInProject(t)
	db := withTempGlobalStateDB(t)
	const (
		oldID = "88888888-8888-4888-8888-888888888888"
		newID = "99999999-9999-4999-8999-999999999999"
	)
	writeContinuedInTranscript(t, dir, oldID, newID)
	writeContinuedInTranscript(t, dir, newID, "")

	inst := NewInstanceWithTool("handoff-persist", projectPath, "claude")
	inst.ClaudeSessionID = oldID
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: inst.ID, Title: inst.Title, ProjectPath: inst.ProjectPath, GroupPath: inst.GroupPath,
		Tool: "claude", Status: "idle", CreatedAt: time.Now(),
		ToolData: json.RawMessage(`{"claude_session_id":"` + oldID + `"}`),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	name := tmux.SessionPrefix + "continued-in_" + fmt.Sprintf("%d", time.Now().UnixNano())
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "sh", "-c", "sleep 3600").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	inst.SetTmuxSessionForTest(&tmux.Session{Name: name})
	if err := inst.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", oldID); err != nil {
		t.Fatalf("seed pane env: %v", err)
	}

	cmd := inst.buildClaudeResumeCommand()
	if !strings.Contains(cmd, "--resume "+newID) {
		t.Fatalf("restart must resume the successor %s, got %q", newID, cmd)
	}
	if got := readClaudeSessionIDFromDB(t, db, inst.ID); got != newID {
		t.Fatalf("stored claude_session_id = %q, want %q: the continued-in rebind never reached SQLite", got, newID)
	}
	if got, err := inst.tmuxSession.GetEnvironment("CLAUDE_SESSION_ID"); err != nil || got != newID {
		t.Fatalf("pane CLAUDE_SESSION_ID = %q (err %v), want %q: a respawn would inherit the stalled id", got, err, newID)
	}
}

// Review follow-up on #2683: after a continued-in rebind, a hook sample still
// carrying the pre-handoff id (the status file is only refreshed when the
// next hook fires) must not revert ClaudeSessionID via the hook fast path.
func TestApplyHookSessionID_IgnoresSupersededContinuedInID(t *testing.T) {
	projectPath, dir := setupContinuedInProject(t)
	const (
		oldID   = "66666666-6666-4666-8666-666666666666"
		newID   = "77777777-7777-4777-8777-777777777777"
		otherID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	)
	writeContinuedInTranscript(t, dir, oldID, newID)
	writeContinuedInTranscript(t, dir, newID, "")

	inst := NewInstance("handoff-stale-hook", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = oldID
	_ = inst.buildClaudeResumeCommand()
	if inst.ClaudeSessionID != newID {
		t.Fatalf("setup: continued-in must rebind to %s, got %q", newID, inst.ClaudeSessionID)
	}

	inst.hookSessionID = oldID
	inst.applyHookSessionIDLocked()
	if inst.ClaudeSessionID != newID {
		t.Fatalf("a stale pre-handoff hook id reverted ClaudeSessionID to %q, want %q", inst.ClaudeSessionID, newID)
	}

	// A genuinely new id from a live hook is still adopted.
	inst.hookSessionID = otherID
	inst.applyHookSessionIDLocked()
	if inst.ClaudeSessionID != otherID {
		t.Fatalf("a fresh hook id must still rebind, got %q", inst.ClaudeSessionID)
	}
}

// Review follow-up on #2683: a tmux server that already carries
// CLAUDE_JOB_DIR in its global environment hands it to every pane. The spawn
// command itself must unset it for every hook-emitting tool, or the hook
// handler drops every hook from that pane.
func TestBuildEnvSourceCommand_UnsetsClaudeJobDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	for _, tool := range []string{"claude", "codex"} {
		inst := &Instance{ID: "jobdir-" + tool, Title: "jobdir", Tool: tool, ProjectPath: t.TempDir()}
		if got := inst.buildEnvSourceCommand(); !strings.Contains(got, "unset CLAUDE_JOB_DIR") {
			t.Errorf("%s spawn env %q must unset CLAUDE_JOB_DIR", tool, got)
		}
	}
	plain := &Instance{ID: "jobdir-shell", Title: "jobdir", Tool: "shell", ProjectPath: t.TempDir()}
	if got := plain.buildEnvSourceCommand(); strings.Contains(got, "CLAUDE_JOB_DIR") {
		t.Errorf("a plain shell pane must be left alone, got %q", got)
	}
}

// CodeQL go/path-injection on #2683: ids used as transcript file names must be
// bare UUIDs.
func TestValidClaudeSessionIDForPath_RejectsTraversal(t *testing.T) {
	for _, id := range []string{
		"",
		"../11111111-1111-4111-8111-111111111111",
		"/tmp/11111111-1111-4111-8111-111111111111",
		"11111111-1111-4111-8111-111111111111/../x",
		`..\11111111-1111-4111-8111-111111111111`,
		"..",
	} {
		if validClaudeSessionIDForPath(id) {
			t.Errorf("id %q must be refused", id)
		}
	}
	if !validClaudeSessionIDForPath("11111111-1111-4111-8111-111111111111") {
		t.Error("a bare UUID must be accepted")
	}
}

// A continued-in chain is only walked inside the Claude projects directory:
// a project directory symlinked to somewhere outside it is not read.
func TestFollowClaudeContinuedIn_RefusesSymlinkedProjectDirOutsideRoot(t *testing.T) {
	projectPath, dir := setupContinuedInProject(t)
	const (
		oldID = "12121212-1212-4121-8121-121212121212"
		newID = "34343434-3434-4343-8343-343434343434"
	)
	outside := t.TempDir()
	writeContinuedInTranscript(t, outside, oldID, newID)
	writeContinuedInTranscript(t, outside, newID, "")
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}

	inst := NewInstance("handoff-symlink-dir", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = oldID
	inst.followClaudeContinuedIn()
	if inst.ClaudeSessionID != oldID {
		t.Fatalf("a project dir linking outside the projects root must not be followed, got %q", inst.ClaudeSessionID)
	}
}

// A successor transcript that is a symlink out of the project directory is
// refused too.
func TestFollowClaudeContinuedIn_RefusesSymlinkedSuccessorOutsideRoot(t *testing.T) {
	projectPath, dir := setupContinuedInProject(t)
	const (
		oldID = "56565656-5656-4565-8565-565656565656"
		newID = "78787878-7878-4787-8787-787878787878"
	)
	outside := t.TempDir()
	writeContinuedInTranscript(t, dir, oldID, newID)
	writeContinuedInTranscript(t, outside, newID, "")
	if err := os.Symlink(filepath.Join(outside, newID+".jsonl"), filepath.Join(dir, newID+".jsonl")); err != nil {
		t.Fatal(err)
	}

	inst := NewInstance("handoff-symlink-file", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = oldID
	inst.followClaudeContinuedIn()
	if inst.ClaudeSessionID != oldID {
		t.Fatalf("a successor linking outside the projects root must not be followed, got %q", inst.ClaudeSessionID)
	}
}

// followClaudeContinuedIn runs from buildClaudeResumeCommand without i.mu
// while UpdateStatus reads claudeSupersededIDs and ClaudeSessionID under it.
// Run with -race: the rebind must take the lock.
func TestFollowClaudeContinuedIn_ConcurrentWithStatusUpdate(t *testing.T) {
	projectPath, dir := setupContinuedInProject(t)
	const (
		oldID = "9a9a9a9a-9a9a-49a9-89a9-9a9a9a9a9a9a"
		newID = "b0b0b0b0-b0b0-4b0b-8b0b-b0b0b0b0b0b0"
	)
	writeContinuedInTranscript(t, dir, oldID, newID)
	writeContinuedInTranscript(t, dir, newID, "")

	for n := 0; n < 20; n++ {
		inst := NewInstance("handoff-race", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = oldID
		inst.hookSessionID = oldID
		done := make(chan struct{})
		go func() {
			defer close(done)
			for k := 0; k < 20; k++ {
				_ = inst.UpdateStatus()
				inst.mu.Lock()
				inst.applyHookSessionIDLocked()
				_ = inst.claudeIDSuperseded(oldID)
				inst.mu.Unlock()
			}
		}()
		inst.followClaudeContinuedIn()
		<-done
		inst.mu.RLock()
		got := inst.ClaudeSessionID
		inst.mu.RUnlock()
		if got != newID {
			t.Fatalf("iteration %d: ClaudeSessionID = %q, want %q", n, got, newID)
		}
	}
}
