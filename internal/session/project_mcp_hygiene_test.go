package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// LOC-1: project MCP config files carry MCP env values (often tokens), so
// agent-deck writes them owner-only and keeps untracked ones out of git.

func hygieneSetup(t *testing.T, env map[string]string) (proj string) {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	proj = filepath.Join(tmp, "proj")
	for _, d := range []string{home, proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"tok": {Command: "echo", Args: []string{"x"}, Env: env},
	}}
	t.Cleanup(resetUserConfigCache(t, cfg))
	return proj
}

func hygieneGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode = %o, want 600", path, got)
	}
}

type projectWriter struct {
	name string
	rel  string
	fn   func(proj string) error
}

var projectWriters = []projectWriter{
	{"claude", ".mcp.json", func(p string) error { return WriteMCPJsonFromConfig(p, []string{"tok"}) }},
	{"cursor", ".cursor/mcp.json", func(p string) error { return WriteCursorProjectMCP(p, []string{"tok"}) }},
	{"opencode", "opencode.json", func(p string) error { return WriteOpenCodeProjectMCP(p, []string{"tok"}) }},
}

func TestProjectMCPConfig_WrittenOwnerOnly(t *testing.T) {
	for _, w := range projectWriters {
		t.Run(w.name, func(t *testing.T) {
			proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
			if err := w.fn(proj); err != nil {
				t.Fatal(err)
			}
			assertOwnerOnly(t, filepath.Join(proj, w.rel))
		})
	}
}

func TestProjectMCPConfig_TightensLooseExistingFile(t *testing.T) {
	for _, w := range projectWriters {
		t.Run(w.name, func(t *testing.T) {
			proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
			if err := w.fn(proj); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(proj, w.rel)
			// An existing setup: same content, but world-readable from an
			// older agent-deck. The rewrite must tighten it even though the
			// bytes are unchanged.
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := w.fn(proj); err != nil {
				t.Fatal(err)
			}
			assertOwnerOnly(t, path)
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatalf("content changed on tighten:\n%s\n---\n%s", before, after)
			}
		})
	}
}

func readExclude(t *testing.T, repo string) string {
	t.Helper()
	p := hygieneGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func countLine(content, line string) int {
	n := 0
	for _, l := range strings.Split(content, "\n") {
		if l == line {
			n++
		}
	}
	return n
}

func TestProjectMCPConfig_UntrackedFileGitExcludedOnce(t *testing.T) {
	for _, w := range projectWriters {
		t.Run(w.name, func(t *testing.T) {
			proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
			hygieneGit(t, proj, "init", "-q")
			for i := 0; i < 2; i++ {
				if err := w.fn(proj); err != nil {
					t.Fatal(err)
				}
			}
			if got := countLine(readExclude(t, proj), "/"+w.rel); got != 1 {
				t.Fatalf("exclude has %d lines for /%s, want 1:\n%s", got, w.rel, readExclude(t, proj))
			}
			if st := hygieneGit(t, proj, "status", "--porcelain", "--untracked-files=all"); countLine(st, "?? "+w.rel) != 0 {
				t.Fatalf("git status still shows the file:\n%s", st)
			}
		})
	}
}

func TestProjectMCPConfig_TrackedFileNotExcluded(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hygieneGit(t, proj, "add", ".mcp.json")
	if err := WriteMCPJsonFromConfig(proj, []string{"tok"}); err != nil {
		t.Fatal(err)
	}
	if ex := readExclude(t, proj); strings.Contains(ex, ".mcp.json") {
		t.Fatalf("tracked file must not be excluded:\n%s", ex)
	}
	if _, err := os.Stat(filepath.Join(proj, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore must not be created/touched: %v", err)
	}
}

func TestProjectMCPConfig_LinkedWorktreeSubdirExcluded(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	main := filepath.Join(proj, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	hygieneGit(t, main, "init", "-q")
	hygieneGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(proj, "wt")
	hygieneGit(t, main, "worktree", "add", "-q", wt)
	if fi, err := os.Lstat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("expected .git file in linked worktree: %v", err)
	}
	sub := filepath.Join(wt, "pkg", "svc")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteMCPJsonFromConfig(sub, []string{"tok"}); err != nil {
		t.Fatal(err)
	}
	if got := countLine(readExclude(t, wt), "/pkg/svc/.mcp.json"); got != 1 {
		t.Fatalf("exclude missing worktree-relative path:\n%s", readExclude(t, wt))
	}
	if st := hygieneGit(t, wt, "status", "--porcelain", "--untracked-files=all"); countLine(st, "?? pkg/svc/.mcp.json") != 0 {
		t.Fatalf("git status still shows the file:\n%s", st)
	}
}

func TestProjectMCPConfig_NotAGitRepoStillWrites(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(proj))
	if err := WriteMCPJsonFromConfig(proj, []string{"tok"}); err != nil {
		t.Fatalf("non-git project must not fail: %v", err)
	}
	assertOwnerOnly(t, filepath.Join(proj, ".mcp.json"))
}

// ${VAR} references are written verbatim, never expanded by agent-deck, so
// the secret stays in the environment and Claude Code expands it at launch.
func TestProjectMCPConfig_EnvVarReferencesWrittenVerbatim(t *testing.T) {
	t.Setenv("FLH_SECRET_TOKEN", "expanded-value-must-not-appear")
	proj := hygieneSetup(t, map[string]string{
		"TOKEN": "${FLH_SECRET_TOKEN}",
		"OTHER": "${FLH_SECRET_TOKEN:-fallback}",
	})
	if err := WriteMCPJsonFromConfig(proj, []string{"tok"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(proj, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "expanded-value-must-not-appear") {
		t.Fatalf("env reference was expanded:\n%s", s)
	}
	for _, want := range []string{`"${FLH_SECRET_TOKEN}"`, `"${FLH_SECRET_TOKEN:-fallback}"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing verbatim %s in:\n%s", want, s)
		}
	}
}

// projectGit only ever runs git in a validated absolute directory inside the
// project root, never one that is relative, option-shaped, outside the root,
// or holds a line break; a missing directory or a file fails at start.
func TestProjectGitDir_Validated(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := validProjectGitDir(dir, dir+"/./"); err != nil || got != filepath.Clean(dir) {
		t.Fatalf("validProjectGitDir(%q) = %q, %v; want %q", dir, got, err, filepath.Clean(dir))
	}
	for _, bad := range []string{"", "relative/dir", "-c", "--upload-pack=touch x", dir + "\nx", dir + "\x00", filepath.Dir(dir), filepath.Join(dir, "..", "x")} {
		if got, err := validProjectGitDir(dir, bad); err == nil {
			t.Errorf("validProjectGitDir(%q) = %q, want an error", bad, got)
		}
		if _, err := projectGit(dir, bad, nil, gitOpWorkTreePrefix); err == nil {
			t.Errorf("projectGit ran git in unvalidated dir %q", bad)
		}
	}
	// Validation is lexical (no stat of the caller-derived path); a missing
	// directory or a regular file still fails when git starts in it.
	for _, bad := range []string{filepath.Join(dir, "missing"), file} {
		if _, err := projectGit(dir, bad, nil, gitOpWorkTreePrefix); err == nil {
			t.Errorf("projectGit succeeded in non-directory %q", bad)
		}
	}
}
