package jujutsu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeJJScript stands in for the jj binary. Like real jj it resolves the repo
// from -R, or from the working directory when -R is absent, and prints
// workspace roots with a trailing newline.
const fakeJJScript = `#!/bin/sh
echo "$*" >> "$FAKE_JJ_LOG"
repo=$(pwd -P)
prev=""
name=""
for a in "$@"; do
	[ "$prev" = "-R" ] && repo="$a"
	[ "$prev" = "--name" ] && name="$a"
	prev="$a"
done
if [ "$repo" != "$FAKE_JJ_REPO" ]; then
	echo "Error: There is no jj repo in \"$repo\"" >&2
	exit 1
fi
case "$1" in
root) echo "$FAKE_JJ_REPO" ;;
workspace)
	case "$2" in
	list) printf 'default:aaa\nlive-ws:bbb\ngone-ws:ccc\n' ;;
	root) echo "$FAKE_JJ_WS/$name" ;;
	forget) ;;
	esac ;;
esac
`

// setupFakeJJ puts the fake jj first on PATH and returns the backend, the
// directory holding workspace roots, and the path of the invocation log.
// Workspace live-ws exists on disk; gone-ws does not.
func setupFakeJJ(t *testing.T) (*JJBackend, string, string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "jj"), []byte(fakeJJScript), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ws, "live-ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "jj.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_JJ_REPO", repo)
	t.Setenv("FAKE_JJ_WS", ws)
	t.Setenv("FAKE_JJ_LOG", logPath)
	return &JJBackend{repoDir: repo}, ws, logPath
}

func forgottenWorkspaces(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "workspace forget "); ok {
			names = append(names, strings.Fields(rest)[0])
		}
	}
	return names
}

// TestJujutsuPruneWorktrees_KeepsLiveWorkspace: jj prints the workspace root
// with a trailing newline; an untrimmed path makes the live workspace look
// missing, so prune forgot it.
func TestJujutsuPruneWorktrees_KeepsLiveWorkspace(t *testing.T) {
	b, _, logPath := setupFakeJJ(t)
	t.Chdir(b.repoDir)

	if err := b.PruneWorktrees(); err != nil {
		t.Fatalf("PruneWorktrees: %v", err)
	}
	got := forgottenWorkspaces(t, logPath)
	if len(got) != 1 || got[0] != "gone-ws" {
		t.Fatalf("forgotten workspaces = %q, want only [gone-ws]", got)
	}
}

// TestJujutsuPruneWorktrees_OutsideRepoDir: the workspace root lookup must use
// the backend's repo, not the process working directory.
func TestJujutsuPruneWorktrees_OutsideRepoDir(t *testing.T) {
	b, _, logPath := setupFakeJJ(t)
	t.Chdir(t.TempDir())

	if err := b.PruneWorktrees(); err != nil {
		t.Fatalf("PruneWorktrees: %v", err)
	}
	got := forgottenWorkspaces(t, logPath)
	if len(got) != 1 || got[0] != "gone-ws" {
		t.Fatalf("forgotten workspaces = %q, want only [gone-ws]", got)
	}
}

// TestJujutsuGetWorktreeForBranch_ReturnsPath: a matching workspace must
// report its filesystem path, not "".
func TestJujutsuGetWorktreeForBranch_ReturnsPath(t *testing.T) {
	b, ws, _ := setupFakeJJ(t)
	t.Chdir(t.TempDir())

	got, err := b.GetWorktreeForBranch("live-ws")
	if err != nil {
		t.Fatalf("GetWorktreeForBranch: %v", err)
	}
	if want := filepath.Join(ws, "live-ws"); got != want {
		t.Fatalf("GetWorktreeForBranch(live-ws) = %q, want %q", got, want)
	}
	got, err = b.GetWorktreeForBranch("no-such-ws")
	if err != nil || got != "" {
		t.Fatalf("GetWorktreeForBranch(no-such-ws) = %q, %v; want \"\", nil", got, err)
	}
}
