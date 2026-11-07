package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #3032: `agent-deck session fork` refused a Claude session whose id
// was detected more than five minutes ago ("no resumable session for tool
// claude") although its transcript was on disk. The CLI only refreshes the id
// when it is empty, so the stale ClaudeDetectedAt loaded from state.db decided
// the answer. The same session must fork, and the CanFork value that
// `session show --json` reports as can_fork must agree.
func TestSessionFork_Issue3032StaleDetectedClaudeSessionForks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	claudeDir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	const sid = "3032aaaa-bbbb-4ccc-8ddd-eeeeeeee3032"
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(project)
	if err != nil {
		resolved = project
	}
	transcriptDir := filepath.Join(claudeDir, "projects", session.ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"sessionId":"` + sid + `","type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(transcriptDir, sid+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	source := session.NewInstanceWithGroupAndTool("old-worker", project, "grp", "claude")
	source.ClaudeSessionID = sid
	source.ClaudeDetectedAt = time.Now().Add(-30 * time.Minute) // the issue's trigger

	const profile = "issue3032_fork"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{source}, session.NewGroupTreeWithGroups([]*session.Instance{source}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	// session show --json reports can_fork = inst.CanFork() on the loaded row.
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 || !loaded[0].CanFork() {
		t.Errorf("loaded stale-dated Claude session with a transcript must report can_fork=true (#3032)")
	}

	// A refusal exits through exitCLI; fail the test there instead of
	// letting os.Exit take the whole test binary down.
	oldExit := cliExitHook
	cliExitHook = func(code int) {
		t.Fatalf("session fork exited with code %d: refused a stale-dated Claude session with a transcript (#3032)", code)
	}
	t.Cleanup(func() { cliExitHook = oldExit })

	var forked *session.Instance
	oldHook := sessionForkBeforeStartHook
	sessionForkBeforeStartHook = func(_ *session.Instance, f *session.Instance, _ git.WorktreeStateOptions) { forked = f }
	t.Cleanup(func() { sessionForkBeforeStartHook = oldHook })

	handleSessionFork(profile, []string{"old-worker", "-t", "old-worker-fork"})

	if forked == nil {
		t.Fatal("CLI fork did not reach the pre-start hook")
	}
	if !forked.IsForkAwaitingStart {
		t.Fatal("fork's first start must run the baked fork command")
	}
	for _, want := range []string{"--resume " + sid, "--fork-session"} {
		if !strings.Contains(forked.Command, want) {
			t.Errorf("baked fork command %q lacks %q", forked.Command, want)
		}
	}
}
