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
	"ls-files": true, "--sparse": true, "--error-unmatch": true, "--": true, ".mcp.json": true, "mcp.json": true, "opencode.json": true,
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
if [ -n "$FAKE_GIT_NOT_IGNORED" ]; then
  case "$*" in *check-ignore*) exit 1 ;; esac
fi
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
	assertLiteralArgv(t, calls(), "-x", "upload-pack", "pwned")
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
	assertLiteralArgv(t, calls(), "-y")
	if ex := readExclude(t, repo); strings.Contains(ex, "-y") {
		t.Fatalf("tracked file must not be excluded:\n%s", ex)
	}
}

// A folder holding a newline, or a file name agent-deck never writes, is
// skipped before any git call: never in argv, never an exclude pattern.
func TestProjectMCPGitExclude_NewlineOrUnknownNameNeverInArgv(t *testing.T) {
	root := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, root, "init", "-q")
	calls, reset := installFakeGit(t, "")
	nl := filepath.Join(root, "a\nb")
	if err := os.MkdirAll(nl, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(nl, ".mcp.json"), filepath.Join(root, "a\n*.json"), filepath.Join(root, "--output=x")} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := readExclude(t, root)
		reset()
		excludeProjectMCPFromGit(root, f)
		if c := calls(); len(c) != 0 {
			t.Fatalf("git ran for %q: %q", f, c)
		}
		if after := readExclude(t, root); after != before {
			t.Fatalf("exclude changed for %q:\n%s", f, after)
		}
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

// The tracked check reads only the index: in a partial clone whose promisor
// remote would run the repo's own core.sshCommand on a lazy fetch, a tracked
// entry whose blob is missing is recognised without git ever reaching the
// object store. The argv is the literal ls-files form; cat-file never runs.
func TestProjectMCPGitExclude_TrackedCheckNeverLazyFetches(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	marker := filepath.Join(t.TempDir(), "PWNED")
	hygieneGit(t, proj, "config", "core.repositoryformatversion", "1")
	hygieneGit(t, proj, "config", "extensions.partialClone", "origin")
	hygieneGit(t, proj, "remote", "add", "origin", "ssh://example.invalid/repo.git")
	hygieneGit(t, proj, "config", "remote.origin.promisor", "true")
	hygieneGit(t, proj, "config", "core.sshCommand", "touch '"+marker+"'; false")
	hygieneGit(t, proj, "update-index", "--add", "--info-only", "--cacheinfo", "100644,1234567890123456789012345678901234567890,.mcp.json")
	mcpFile := filepath.Join(proj, ".mcp.json")
	if err := os.WriteFile(mcpFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, reset := installFakeGit(t, "")
	reset()
	excludeProjectMCPFromGit(proj, mcpFile)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a lazy fetch ran the repo's core.sshCommand: %v", err)
	}
	got := calls()
	assertLiteralArgv(t, got)
	sawLsFiles := false
	for _, argv := range got {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "cat-file") {
			t.Fatalf("cat-file invoked: %q", argv)
		}
		if strings.HasSuffix(joined, "ls-files --sparse --error-unmatch -- .mcp.json") {
			sawLsFiles = true
		}
	}
	if !sawLsFiles {
		t.Fatalf("tracked check is not the literal ls-files form: %q", got)
	}
	if ex := readExclude(t, proj); strings.Contains(ex, ".mcp.json") {
		t.Fatalf("tracked file must not be excluded:\n%s", ex)
	}
}

// Any index entry counts as tracked: intent-to-add and conflicted entries
// are left alone, for each project MCP file name agent-deck writes.
func TestProjectMCPGitExclude_IntentToAddAndConflictCountAsTracked(t *testing.T) {
	for _, rel := range []string{".mcp.json", ".cursor/mcp.json", "opencode.json"} {
		for _, mode := range []string{"intent-to-add", "conflict"} {
			t.Run(rel+"/"+mode, func(t *testing.T) {
				proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
				hygieneGit(t, proj, "init", "-q")
				mcpFile := filepath.Join(proj, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(mcpFile), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mcpFile, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "intent-to-add" {
					hygieneGit(t, proj, "add", "-N", "--", rel)
				} else {
					blob := hygieneGit(t, proj, "hash-object", "-w", "--", rel)
					info := ""
					for _, stage := range []string{"1", "2", "3"} {
						info += "100644 " + blob + " " + stage + "\t" + rel + "\n"
					}
					cmd := exec.Command("git", "-C", proj, "update-index", "--index-info")
					cmd.Stdin = strings.NewReader(info)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("update-index: %v\n%s", err, out)
					}
					if st := hygieneGit(t, proj, "ls-files", "--stage", "--", rel); strings.Count(st, "\n") != 2 {
						t.Fatalf("expected three conflict stages:\n%s", st)
					}
				}
				excludeProjectMCPFromGit(filepath.Dir(mcpFile), mcpFile)
				if ex := readExclude(t, proj); strings.Contains(ex, rel) {
					t.Fatalf("%s entry must not be excluded:\n%s", mode, ex)
				}
			})
		}
	}
}

// info/ symlinked out of the git dir: the realInfo re-check refuses it even
// though the path git reported is lexically inside.
func TestProjectMCPGitExclude_SymlinkedInfoDirRefusedByRealInfoCheck(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	info := filepath.Join(proj, ".git", "info")
	outside := t.TempDir()
	if err := os.Rename(info, info+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, info); err != nil {
		t.Fatal(err)
	}
	// git canonicalises --git-path through the symlink (refused by the
	// git-dir containment check); answer with the lexical path so the realInfo
	// re-check is what refuses it.
	installFakeGit(t, filepath.Join(info, "exclude"))
	if err := addProjectMCPGitExclude(proj, writeProjectMCPFile(t, proj)); err == nil || !strings.Contains(err.Error(), "outside the git directory") {
		t.Fatalf("symlinked info/ not refused by the realInfo check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "exclude")); !os.IsNotExist(err) {
		t.Fatalf("wrote exclude through the info/ symlink: %v", err)
	}
}

// A symlinked info/exclude is never followed (O_NOFOLLOW), even when the path
// reported for it is lexically inside the git dir: its target is neither read
// nor appended to.
func TestProjectMCPGitExclude_SymlinkedExcludeFileNotFollowed(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	exclude := filepath.Join(proj, ".git", "info", "exclude")
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exclude, exclude+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, exclude); err != nil {
		t.Fatal(err)
	}
	// git itself canonicalises --git-path through the symlink (which the
	// git-dir containment check already refuses); answer with the lexical
	// path instead so only O_NOFOLLOW stands between the append and the target.
	installFakeGit(t, exclude)
	if err := addProjectMCPGitExclude(proj, writeProjectMCPFile(t, proj)); err == nil || !strings.Contains(err.Error(), "open info/exclude") {
		t.Fatalf("symlinked info/exclude was not refused at open: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep\n" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func writeProjectMCPFile(t *testing.T, proj string) string {
	t.Helper()
	f := filepath.Join(proj, ".mcp.json")
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// With a sparse index, expanding it reads tree objects; in a crafted partial
// clone a missing tree would lazily fetch and run the repo's own
// core.sshCommand. --sparse avoids the expansion and GIT_NO_LAZY_FETCH=1
// refuses the fetch, so the marker command never runs.
func TestProjectMCPGitExclude_SparseIndexMissingTreeNeverLazyFetches(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	for _, f := range []string{"a/x", "b/y"} {
		p := filepath.Join(proj, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hygieneGit(t, proj, "add", "a", "b")
	hygieneGit(t, proj, "commit", "-q", "-m", "init")
	hygieneGit(t, proj, "sparse-checkout", "set", "--cone", "--sparse-index", "a")
	if st := hygieneGit(t, proj, "ls-files", "--sparse"); countLine(st, "b/") != 1 {
		t.Fatalf("expected a sparse directory entry for b/:\n%s", st)
	}
	tree := hygieneGit(t, proj, "rev-parse", "HEAD:b")
	if err := os.Remove(filepath.Join(proj, ".git", "objects", tree[:2], tree[2:])); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "PWNED")
	hygieneGit(t, proj, "config", "core.repositoryformatversion", "1")
	hygieneGit(t, proj, "config", "extensions.partialClone", "origin")
	hygieneGit(t, proj, "remote", "add", "origin", "ssh://example.invalid/repo.git")
	hygieneGit(t, proj, "config", "remote.origin.promisor", "true")
	hygieneGit(t, proj, "config", "core.sshCommand", "touch '"+marker+"'; false")

	excludeProjectMCPFromGit(proj, writeProjectMCPFile(t, proj))
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a lazy fetch ran the repo's core.sshCommand: %v", err)
	}
	if got := countLine(readExclude(t, proj), "/.mcp.json"); got != 1 {
		t.Fatalf("untracked file not excluded in a sparse-index repo:\n%s", readExclude(t, proj))
	}
}

// Inherited repo-local git variables (GIT_DIR, GIT_INDEX_FILE, ...) never
// redirect the calls or the info/exclude write to another repository.
func TestProjectMCPGitExclude_InheritedGitDirIgnored(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	hygieneGit(t, other, "init", "-q")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.excludesFile")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(other, "ignore-all"))
	if err := os.WriteFile(filepath.Join(other, "ignore-all"), []byte("*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readFileString(t, filepath.Join(other, ".git", "info", "exclude"))
	excludeProjectMCPFromGit(proj, writeProjectMCPFile(t, proj))
	if after := readFileString(t, filepath.Join(other, ".git", "info", "exclude")); after != before {
		t.Fatalf("exclude write redirected by inherited GIT_DIR:\n%s", after)
	}
	if got := countLine(readFileString(t, filepath.Join(proj, ".git", "info", "exclude")), "/.mcp.json"); got != 1 {
		t.Fatalf("project exclude missing entry:\n%s", readFileString(t, filepath.Join(proj, ".git", "info", "exclude")))
	}
	env := projectGitEnv([]string{"GIT_DIR=x", "GIT_CONFIG_KEY_3=k", "GIT_CONFIG_VALUE_3=v", "GIT_NO_LAZY_FETCH=0", "PATH=/bin", "GIT_AUTHOR_NAME=a"})
	want := []string{"PATH=/bin", "GIT_AUTHOR_NAME=a", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0"}
	if strings.Join(env, "|") != strings.Join(want, "|") {
		t.Fatalf("projectGitEnv = %q, want %q", env, want)
	}
}

func readFileString(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}
