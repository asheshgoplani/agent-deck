package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// On OpenCode 2.x, session.fork keeps the child in the parent's directory.
// A fork into a worktree must move the child there before the TUI resumes it,
// otherwise only the shell cwd changes and the service session still belongs
// to the parent's tree.
func TestOpenCodeForkV2_WorktreeForkMovesChildToWorkDir(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	argv := fakeOpenCodeForkService(t, "ses_child_456", 0)

	parent := NewInstanceWithTool("oc", t.TempDir(), "opencode")
	parent.OpenCodeSessionID = "ses_parent_123"
	parent.OpenCodeDetectedAt = time.Now()
	workDir := t.TempDir() + "/wt with space"
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	forked, cmd, err := parent.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(
		"oc fork", "", &OpenCodeOptions{Agent: "build"}, workDir, parent.ProjectPath, "feature")
	if err != nil {
		t.Fatalf("CreateForkedOpenCodeInstanceWithOptionsAndWorkDir: %v", err)
	}
	if want := "cd " + shellescape.Quote(workDir) + " && " + GetToolCommand("opencode") + " -s ses_child_456"; cmd != want {
		t.Fatalf("fork command = %q, want %q", cmd, want)
	}
	if forked.OpenCodeSessionID != "ses_child_456" || forked.WorktreePath != workDir {
		t.Fatalf("forked binding = %q worktree = %q", forked.OpenCodeSessionID, forked.WorktreePath)
	}

	gotArgv, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("read argv marker: %v", err)
	}
	moveBody, _ := json.Marshal(map[string]string{"directory": workDir})
	want := "api\nsession.fork\n--param\nsessionID=ses_parent_123\n-d\n{}\n" +
		"api\nsession.move\n--param\nsessionID=ses_child_456\n-d\n" + string(moveBody) + "\n" +
		"api\nsession.switchAgent\n--param\nsessionID=ses_child_456\n-d\n" + `{"agent":"build"}` + "\n"
	if string(gotArgv) != want {
		t.Fatalf("opencode argv =\n%s\nwant\n%s", gotArgv, want)
	}
}

// A child that cannot be moved onto the worktree is removed, like a child
// whose overrides fail, so no stray fork stays in the parent's directory.
func TestOpenCodeForkV2_WorktreeMoveFailureRemovesChild(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	argv := fakeOpenCodeService(t, "ses_child_456", 0, 1)

	parent := NewInstanceWithTool("oc", t.TempDir(), "opencode")
	parent.OpenCodeSessionID = "ses_parent_123"
	parent.OpenCodeDetectedAt = time.Now()

	forked, _, err := parent.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(
		"oc fork", "", &OpenCodeOptions{}, t.TempDir(), parent.ProjectPath, "feature")
	if err == nil || forked != nil {
		t.Fatalf("expected an error and no instance when session.move fails; got forked=%t err=%v", forked != nil, err)
	}
	if !strings.Contains(err.Error(), "move") {
		t.Fatalf("error should name the move: %v", err)
	}
	gotArgv, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("read argv marker: %v", err)
	}
	if !strings.Contains(string(gotArgv), "api\nsession.move\n") {
		t.Fatalf("worktree fork never attempted session.move; opencode argv =\n%s", gotArgv)
	}
	if want := "api\nsession.remove\n--param\nsessionID=ses_child_456\n"; !strings.HasSuffix(string(gotArgv), want) {
		t.Fatalf("a child that could not be moved must be removed; opencode argv =\n%s", gotArgv)
	}
}
