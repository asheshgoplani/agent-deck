package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

func TestBuildBashExportPrefixIncludesRepositorySessionTemp(t *testing.T) {
	project := t.TempDir()
	inst := NewInstanceWithTool("repo-temp", project, "claude")
	inst.ID = "session-temp-123"

	projectRoot, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(projectRoot, ".agent-deck", "tmp", inst.ID)
	prefix := inst.buildBashExportPrefix(false)
	for _, name := range []string{"TMPDIR", "TMP", "TEMP", "AGENT_DECK_SESSION_TMPDIR"} {
		want := "export " + name + "=" + shellescape.Quote(wantPath) + "; "
		if !strings.Contains(prefix, want) {
			t.Errorf("buildBashExportPrefix() missing %q in %q", want, prefix)
		}
	}
}

func TestPrepareCommandUsesGitWorktreeRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	project := filepath.Join(repo, "nested", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("repo-temp", project, "shell")
	inst.ID = "session-worktree-root"

	if _, _, err := inst.prepareCommand("true"); err != nil {
		t.Fatalf("prepareCommand() error: %v", err)
	}
	want := filepath.Join(repo, ".agent-deck", "tmp", inst.ID)
	if _, err := os.Stat(filepath.Join(want, repositorySessionTempMarker)); err != nil {
		t.Fatalf("marker was not created at Git worktree root %s: %v", want, err)
	}
	wrong := filepath.Join(project, ".agent-deck", "tmp", inst.ID)
	if _, err := os.Stat(wrong); !os.IsNotExist(err) {
		t.Fatalf("nested project path unexpectedly owns session temp root %s", wrong)
	}
}

func TestPrepareCommandRejectsSymlinkedAgentDeckDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	project := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, ".agent-deck")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	inst := NewInstanceWithTool("repo-temp", project, "shell")
	inst.ID = "session-symlink-refused"

	if _, _, err := inst.prepareCommand("true"); err == nil {
		t.Fatal("prepareCommand() accepted a symlinked .agent-deck directory")
	}
	escaped := filepath.Join(outside, "tmp", inst.ID)
	if _, err := os.Lstat(escaped); !os.IsNotExist(err) {
		t.Fatalf("prepareCommand() wrote through symlink outside project: %s", escaped)
	}
}

func TestPrepareCommandRejectsForeignSessionTempMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	project := t.TempDir()
	inst := NewInstanceWithTool("repo-temp", project, "shell")
	inst.ID = "session-marker-refused"
	root := filepath.Join(project, ".agent-deck", "tmp", inst.ID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, repositorySessionTempMarker)
	foreign := "schema=1\nsession_id=another-session\n"
	if err := os.WriteFile(markerPath, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := inst.prepareCommand("true"); err == nil {
		t.Fatal("prepareCommand() accepted a foreign session temp marker")
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(marker); got != foreign {
		t.Fatalf("foreign marker was overwritten: got %q, want %q", got, foreign)
	}
}

func TestPrepareCommandCreatesMarkedRepositorySessionTempAndGlobalExclude(t *testing.T) {
	home := t.TempDir()
	xdgConfig := filepath.Join(home, "xdg-config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	project := t.TempDir()
	inst := NewInstanceWithTool("repo-temp", project, "shell")
	inst.ID = "session-temp-prepare"

	if _, _, err := inst.prepareCommand("true"); err != nil {
		t.Fatalf("prepareCommand() error: %v", err)
	}

	root := filepath.Join(project, ".agent-deck", "tmp", inst.ID)
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("session temp root was not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("session temp root mode = %04o, want 0700", got)
	}
	marker, err := os.ReadFile(filepath.Join(root, ".agent-deck-session-temp"))
	if err != nil {
		t.Fatalf("read ownership marker: %v", err)
	}
	wantMarker := "schema=1\nsession_id=" + inst.ID + "\n"
	if got := string(marker); got != wantMarker {
		t.Errorf("ownership marker = %q, want %q", got, wantMarker)
	}

	ignorePath := filepath.Join(xdgConfig, "git", "ignore")
	ignore, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatalf("read global Git excludes: %v", err)
	}
	if got := string(ignore); got != ".agent-deck/\n" {
		t.Errorf("global Git excludes = %q, want %q", got, ".agent-deck/\n")
	}
}
