package session

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Project git hygiene: caller-derived paths never reach git's argv (they go
// on stdin or as the validated working directory), git only runs inside the
// session's project root, and info/exclude is only written inside the git dir.

// projectGitLiteralArgs is every argv element projectGit may hand to git.
var projectGitLiteralArgs = map[string]bool{
	"-c": true, "core.fsmonitor=false": true, "core.hooksPath=/dev/null": true,
	"rev-parse": true, "--is-inside-work-tree": true, "--show-prefix": true,
	"cat-file": true, "--batch-check": true,
	"check-ignore": true, "-q": true, "--no-index": true, "--stdin": true, "-z": true,
	"--path-format=absolute": true, "--git-common-dir": true, "--git-path": true, "info/exclude": true,
}

// installFakeGit puts a git wrapper first on PATH that records each
// invocation's argv (NUL-separated, one invocation per line) and then runs the
// real git. When fakeExclude is set, an info/exclude lookup answers with that
// path instead. It returns a reader for the recorded invocations and a reset.
func installFakeGit(t *testing.T, fakeExclude string) (calls func() [][]string, reset func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "argv.log")
	script := `#!/bin/sh
for a in "$@"; do printf '%s\0' "$a" >> "$FAKE_GIT_LOG"; done
printf '\n' >> "$FAKE_GIT_LOG"
if [ -n "$FAKE_GIT_EXCLUDE" ]; then
  case "$*" in *--git-path*)
    case "$*" in *--git-common-dir*) "$FAKE_GIT_REAL" rev-parse --path-format=absolute --git-common-dir ;; esac
    printf '%s\n' "$FAKE_GIT_EXCLUDE"
    exit 0 ;;
  esac
fi
exec "$FAKE_GIT_REAL" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GIT_REAL", real)
	t.Setenv("FAKE_GIT_LOG", log)
	t.Setenv("FAKE_GIT_EXCLUDE", fakeExclude)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls = func() [][]string {
		data, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var out [][]string
		for _, line := range bytes.Split(data, []byte("\x00\n")) {
			if len(line) == 0 {
				continue
			}
			out = append(out, strings.Split(string(line), "\x00"))
		}
		return out
	}
	reset = func() { _ = os.Remove(log) }
	return calls, reset
}

func assertLiteralArgv(t *testing.T, calls [][]string, forbidden ...string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no git call recorded")
	}
	for _, argv := range calls {
		for _, a := range argv {
			for _, f := range forbidden {
				if strings.Contains(a, f) {
					t.Fatalf("git argv carries caller-derived %q: %q", f, argv)
				}
			}
			if !projectGitLiteralArgs[a] {
				t.Fatalf("git argv element %q is not a projectGit literal: %q", a, argv)
			}
		}
	}
}

// A project whose path starts with '-' is excluded (and a tracked one left
// alone) while git's argv never carries the path.
func TestProjectMCPGitExclude_DashDirPathNeverInArgv(t *testing.T) {
	repo := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, repo, "init", "-q")
	calls, reset := installFakeGit(t, "")

	proj := filepath.Join(repo, "-x", "--upload-pack=touch pwned")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	reset()
	if err := WriteMCPJsonFromConfig(proj, []string{"tok"}); err != nil {
		t.Fatal(err)
	}
	assertLiteralArgv(t, calls(), "-x", "upload-pack", ".mcp.json")
	want := "/-x/--upload-pack=touch pwned/.mcp.json"
	if got := countLine(readExclude(t, repo), want); got != 1 {
		t.Fatalf("exclude has %d lines for %s, want 1:\n%s", got, want, readExclude(t, repo))
	}

	tracked := filepath.Join(repo, "-y")
	if err := os.MkdirAll(tracked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tracked, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hygieneGit(t, repo, "add", "--", "-y/.mcp.json")
	reset()
	if err := WriteMCPJsonFromConfig(tracked, []string{"tok"}); err != nil {
		t.Fatal(err)
	}
	assertLiteralArgv(t, calls(), "-y", ".mcp.json")
	if ex := readExclude(t, repo); strings.Contains(ex, "-y") {
		t.Fatalf("tracked file must not be excluded:\n%s", ex)
	}
}

// A file name holding a newline is skipped safely: never in argv, never split
// into exclude patterns.
func TestProjectMCPGitExclude_NewlineNameNeverInArgv(t *testing.T) {
	root := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, root, "init", "-q")
	calls, reset := installFakeGit(t, "")
	mcpFile := filepath.Join(root, "a\n*.json")
	if err := os.WriteFile(mcpFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readExclude(t, root)
	reset()
	excludeProjectMCPFromGit(root, mcpFile)
	assertLiteralArgv(t, calls(), "a\n", "*.json")
	if after := readExclude(t, root); after != before {
		t.Fatalf("exclude changed:\n%s", after)
	}
}

// A project MCP file outside the given project root is refused before any
// git call or write, lexically or through a symlink.
func TestProjectMCPGitExclude_FileOutsideRootRefused(t *testing.T) {
	repo := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, repo, "init", "-q")
	root := filepath.Join(repo, "a")
	other := filepath.Join(repo, "b")
	for _, d := range []string{root, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, ".mcp.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	calls, reset := installFakeGit(t, "")
	before := readExclude(t, repo)
	for _, f := range []string{
		filepath.Join(other, ".mcp.json"),
		root + string(filepath.Separator) + ".." + string(filepath.Separator) + "b" + string(filepath.Separator) + ".mcp.json",
		filepath.Join(root, "link", ".mcp.json"),
	} {
		reset()
		if err := addProjectMCPGitExclude(root, f); err == nil {
			t.Errorf("file %q outside root %q was not refused", f, root)
		}
		if c := calls(); len(c) != 0 {
			t.Errorf("git ran for file %q outside root: %q", f, c)
		}
		if after := readExclude(t, repo); after != before {
			t.Fatalf("exclude changed for %q:\n%s", f, after)
		}
	}
}

// A session whose project path is a symlink still gets its file excluded.
func TestProjectMCPGitExclude_SymlinkedProjectDir(t *testing.T) {
	repo := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, repo, "init", "-q")
	sub := filepath.Join(repo, "svc")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(repo), "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteMCPJsonFromConfig(link, []string{"tok"}); err != nil {
		t.Fatal(err)
	}
	if got := countLine(readExclude(t, repo), "/svc/.mcp.json"); got != 1 {
		t.Fatalf("exclude missing symlinked project file:\n%s", readExclude(t, repo))
	}
}

// An info/exclude path outside the repository's git dir is never written.
func TestProjectMCPGitExclude_ExcludeOutsideGitDirRefused(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	outside := filepath.Join(t.TempDir(), "victim")
	installFakeGit(t, outside)
	if err := WriteMCPJsonFromConfig(proj, []string{"tok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("wrote info/exclude outside the git dir: %v", err)
	}
	if ex := readExclude(t, proj); strings.Contains(ex, ".mcp.json") {
		t.Fatalf("unexpected exclude entry:\n%s", ex)
	}
	if _, _, err := checkExcludeWithinGitDir(filepath.Join(proj, ".git"), filepath.Join(proj, ".git", "..", "x")); err == nil {
		t.Fatal("exclude path escaping the git dir via .. accepted")
	}
}
