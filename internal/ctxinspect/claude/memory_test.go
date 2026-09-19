package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file and its parent directories.
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// chainPaths returns the discovered paths in load order.
func chainPaths(c *MemoryChain) []string {
	out := make([]string, 0, len(c.Files))
	for _, f := range c.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestDiscoverMemoryLoadOrder(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "work", "repo")

	userMD := writeFile(t, filepath.Join(config, "CLAUDE.md"), "user global")
	userRule := writeFile(t, filepath.Join(config, "rules", "a.md"), "rule a")
	ancestorMD := writeFile(t, filepath.Join(root, "work", "CLAUDE.md"), "ancestor")
	projectMD := writeFile(t, filepath.Join(project, "CLAUDE.md"), "project")
	projectDot := writeFile(t, filepath.Join(project, ".claude", "CLAUDE.md"), "project dot")
	projectLocal := writeFile(t, filepath.Join(project, "CLAUDE.local.md"), "local")
	// AGENTS.md must never appear: Claude Code does not read it, and listing it
	// would invent context that is not in the window.
	writeFile(t, filepath.Join(project, "AGENTS.md"), "agents")

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}

	want := []string{userMD, userRule, ancestorMD, projectMD, projectDot, projectLocal}
	got := chainPaths(chain)
	if len(got) != len(want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain[%d] = %s, want %s (full chain %v)", i, got[i], want[i], got)
		}
	}
	for _, f := range chain.Files {
		if strings.HasSuffix(f.Path, "AGENTS.md") {
			t.Fatalf("AGENTS.md must not appear in a Claude Code chain: %s", f.Path)
		}
	}
	if chain.Files[0].Layer != LayerUser {
		t.Errorf("first layer = %s, want %s", chain.Files[0].Layer, LayerUser)
	}
	if chain.Files[len(chain.Files)-1].Layer != LayerProject {
		t.Errorf("last layer = %s, want %s", chain.Files[len(chain.Files)-1].Layer, LayerProject)
	}
	for i, f := range chain.Files {
		if f.Order != i {
			t.Errorf("file %s has Order %d, want %d", f.Path, f.Order, i)
		}
	}
}

func TestDiscoverMemoryWrapperStrings(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(config, "CLAUDE.md"), "u")
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "p")
	// The auto-memory file is filed under the slug of the project root, which
	// here is the project itself: a temp dir is in no git repository. The
	// transcript is filed under the same slug in this case but is not what
	// locates the index.
	transcript := writeFile(t, filepath.Join(config, "projects", claudeProjectSlug(project), "abc.jsonl"), "{}")
	writeFile(t, filepath.Join(config, "projects", claudeProjectSlug(project), "memory", "MEMORY.md"), "m")

	chain, err := DiscoverMemory(MemoryOptions{
		ConfigDir: config, ProjectPath: project, TranscriptPath: transcript, Env: emptyEnv,
	})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}

	wrappers := map[MemoryLayer]string{}
	for _, f := range chain.Files {
		wrappers[f.Layer] = f.Wrapper
		if !f.WrapperObserved {
			t.Errorf("layer %s reports an unobserved wrapper; all three layers here have been seen in real prompts", f.Layer)
		}
	}
	// These are the exact strings Claude Code injects. Getting them wrong
	// under-reports every file by its own header.
	if got, want := wrappers[LayerUser], "(user's private global instructions for all projects):"; !strings.HasSuffix(got, want) {
		t.Errorf("user wrapper = %q, want it to end with %q", got, want)
	}
	if got, want := wrappers[LayerProject], "(project instructions, checked into the codebase):"; !strings.HasSuffix(got, want) {
		t.Errorf("project wrapper = %q, want it to end with %q", got, want)
	}
	if got, want := wrappers[LayerAutoMem], "(user's auto-memory, persists across conversations):"; !strings.HasSuffix(got, want) {
		t.Errorf("auto-memory wrapper = %q, want it to end with %q", got, want)
	}
}

func TestDiscoverMemoryKillSwitch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "should not load")

	chain, err := DiscoverMemory(MemoryOptions{
		ProjectPath: root,
		Env:         envMap(map[string]string{disableMemoryEnv: "1"}),
	})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	if !chain.Disabled {
		t.Fatal("want the chain reported as disabled")
	}
	if len(chain.Files) != 0 {
		t.Fatalf("files = %v, want none when the kill switch is set", chainPaths(chain))
	}
}

func TestDiscoverMemoryKillSwitchFalsyValues(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "loads")
	for _, v := range []string{"", "0", "false", "no", "off"} {
		chain, err := DiscoverMemory(MemoryOptions{
			ProjectPath: root,
			Env:         envMap(map[string]string{disableMemoryEnv: v}),
		})
		if err != nil {
			t.Fatalf("DiscoverMemory(%q): %v", v, err)
		}
		if chain.Disabled {
			t.Errorf("%s=%q must not disable the chain", disableMemoryEnv, v)
		}
	}
}

func TestDiscoverMemoryHonoursClaudeMdExcludes(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(config, "settings.json"), `{"claudeMdExcludes":["CLAUDE.local.md"]}`)
	kept := writeFile(t, filepath.Join(project, "CLAUDE.md"), "kept")
	excluded := writeFile(t, filepath.Join(project, "CLAUDE.local.md"), "excluded")

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	if got := chainPaths(chain); len(got) != 1 || got[0] != kept {
		t.Fatalf("chain = %v, want only %s", got, kept)
	}
	if len(chain.Excluded) != 1 || chain.Excluded[0].Path != excluded {
		t.Fatalf("excluded = %+v, want %s", chain.Excluded, excluded)
	}
	if chain.Excluded[0].Pattern != "CLAUDE.local.md" {
		t.Errorf("Pattern = %q, want the matching pattern recorded", chain.Excluded[0].Pattern)
	}
	if chain.Excluded[0].Size == 0 {
		t.Error("want the excluded file's size recorded, so the report can say what the exclusion saves")
	}
}

func TestDiscoverMemoryImports(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "repo")
	// A three-level import chain plus a cycle back to the root, and a reference
	// that looks like an import but points at nothing.
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "root\n@./one.md\n@./missing.md\nsee @anthropic-ai/sdk for details")
	writeFile(t, filepath.Join(project, "one.md"), "one\n@./two.md")
	writeFile(t, filepath.Join(project, "two.md"), "two\n@./CLAUDE.md")

	chain, err := DiscoverMemory(MemoryOptions{ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}

	got := chainPaths(chain)
	if len(got) != 3 {
		t.Fatalf("chain = %v, want exactly the root plus two real imports", got)
	}
	byPath := map[string]MemoryFile{}
	for _, f := range chain.Files {
		byPath[filepath.Base(f.Path)] = f
	}
	if d := byPath["one.md"].ImportDepth; d != 1 {
		t.Errorf("one.md ImportDepth = %d, want 1", d)
	}
	if d := byPath["two.md"].ImportDepth; d != 2 {
		t.Errorf("two.md ImportDepth = %d, want 2", d)
	}
	if byPath["one.md"].ImportedBy == "" {
		t.Error("want the importing file recorded on an imported one")
	}
	// A nonexistent target and an npm scope must not become imports: requiring
	// the file to exist is what keeps the reconstruction from inventing context.
	for _, f := range chain.Files {
		if strings.Contains(f.Path, "missing") || strings.Contains(f.Path, "anthropic-ai") {
			t.Errorf("%s must not be treated as an import", f.Path)
		}
	}
}

func TestDiscoverMemoryImportsStopAtDepthLimit(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "repo")
	// A chain longer than Claude Code follows. Files past the limit exist but
	// are not in the window, so reporting them would overstate the context.
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "root\n@./l1.md")
	for i := 1; i <= maxImportDepth+2; i++ {
		next := ""
		if i <= maxImportDepth+1 {
			next = "\n@./l" + string(rune('0'+i+1)) + ".md"
		}
		writeFile(t, filepath.Join(project, "l"+string(rune('0'+i))+".md"), "level"+next)
	}

	chain, err := DiscoverMemory(MemoryOptions{ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	for _, f := range chain.Files {
		if f.ImportDepth > maxImportDepth {
			t.Fatalf("%s has import depth %d, above the harness's limit of %d", f.Path, f.ImportDepth, maxImportDepth)
		}
	}
}

func TestImportRefsSkipsFencedCode(t *testing.T) {
	content := "before @./real.md\n```\n@./inside-a-fence.md\n```\nafter"
	refs := importRefs(content)
	for _, r := range refs {
		if strings.Contains(r, "inside-a-fence") {
			t.Fatalf("refs = %v, must not include a reference inside a code fence", refs)
		}
	}
	found := false
	for _, r := range refs {
		if r == "./real.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("refs = %v, want ./real.md", refs)
	}
}

func TestDiscoverMemoryUnreadableFileStaysInTheChain(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny reads")
	}
	root := t.TempDir()
	path := writeFile(t, filepath.Join(root, "CLAUDE.md"), "secret")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	chain, err := DiscoverMemory(MemoryOptions{ProjectPath: root, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	if len(chain.Files) != 1 {
		t.Fatalf("chain = %v, want the unreadable file retained: dropping it would leave a hole in the inventory", chainPaths(chain))
	}
	if chain.Files[0].ReadErr == "" {
		t.Error("want the read failure recorded on the file")
	}
	if chain.Files[0].Size == 0 {
		t.Error("want the size retained so the file can still be priced")
	}
}

func TestDiscoverMemoryAdditionalDirectories(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")
	extra := filepath.Join(root, "extra")
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "p")
	extraMD := writeFile(t, filepath.Join(extra, "CLAUDE.md"), "e")
	writeFile(t, filepath.Join(config, "settings.json"), `{"additionalDirectories":["`+extra+`"]}`)

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	found := false
	for _, f := range chain.Files {
		if f.Path == extraMD {
			found = true
			if f.Layer != LayerAdditional {
				t.Errorf("layer = %s, want %s", f.Layer, LayerAdditional)
			}
		}
	}
	if !found {
		t.Fatalf("chain = %v, want the additional directory's file", chainPaths(chain))
	}
}

func TestDiscoverMemoryUnparsableSettingsWarns(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFile(t, filepath.Join(config, "settings.json"), "{not json")

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	if len(chain.Warnings) == 0 {
		t.Fatal("want a warning: silently ignoring a broken settings file would apply no excludes without saying so")
	}
}

func TestReadBoundedFileCap(t *testing.T) {
	path := writeFile(t, filepath.Join(t.TempDir(), "big.md"), strings.Repeat("a", 100))

	content, truncated, err := readBoundedFile(path, 10)
	if err != nil {
		t.Fatalf("readBoundedFile: %v", err)
	}
	if len(content) != 10 || !truncated {
		t.Fatalf("content=%d truncated=%v, want 10 bytes and truncated", len(content), truncated)
	}

	// Exactly at the cap must not be reported as truncated.
	content, truncated, err = readBoundedFile(path, 100)
	if err != nil {
		t.Fatalf("readBoundedFile: %v", err)
	}
	if len(content) != 100 || truncated {
		t.Fatalf("content=%d truncated=%v, want the whole file and not truncated", len(content), truncated)
	}
}

func TestMatchExclude(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		patterns []string
		want     string
	}{
		{"base name", "/a/b/CLAUDE.local.md", []string{"CLAUDE.local.md"}, "CLAUDE.local.md"},
		{"glob on base", "/a/b/CLAUDE.local.md", []string{"*.local.md"}, "*.local.md"},
		{"directory prefix", "/opt/vendor/CLAUDE.md", []string{"/opt/*"}, "/opt/*"},
		{"no match", "/a/b/CLAUDE.md", []string{"*.local.md"}, ""},
		{"blank pattern ignored", "/a/b/CLAUDE.md", []string{"  "}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := matchExclude(tc.path, tc.patterns)
			if (tc.want == "") == ok {
				t.Fatalf("matchExclude ok = %v, want %v", ok, tc.want != "")
			}
			if got != tc.want {
				t.Fatalf("pattern = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeProjectSlug(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"separators and dots", "/Users/ashesh/.agent-deck", "-Users-ashesh--agent-deck"},
		{"version dot", "/home/user/foo.v1", "-home-user-foo-v1"},
		{"trailing separator is cleaned by the caller", "/a/b", "-a-b"},
		// Everything outside [A-Za-z0-9] becomes a dash, not just separators and
		// dots. Underscores and spaces are the common cases the old encoding got
		// wrong, and getting them wrong points the row at a directory that does
		// not exist.
		{"underscore", "/a/my_project", "-a-my-project"},
		{"space", "/a/my project", "-a-my-project"},
		{"non-ascii", "/a/café", "-a-caf-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeProjectSlug(tc.in); got != tc.want {
				t.Errorf("claudeProjectSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestClaudeProjectSlugTruncatesWithHash pins the long-path form. Claude Code
// caps the slug at 200 characters and appends a dash plus a base-36 hash of the
// whole path; without that, every deeply nested project's index is looked for in
// a directory that does not exist.
func TestClaudeProjectSlugTruncatesWithHash(t *testing.T) {
	long := "/" + strings.Repeat("abcdefghij", 30) // 301 characters
	got := claudeProjectSlug(long)
	if len(got) <= claudeSlugMaxUnits {
		t.Fatalf("slug length = %d, want more than the %d-character cap plus a hash", len(got), claudeSlugMaxUnits)
	}
	// The dash that introduces the hash sits exactly at the cap, so the slug is
	// the first 200 characters, a dash, and the hash.
	if idx := strings.LastIndex(got, "-"); idx != claudeSlugMaxUnits {
		t.Fatalf("hash separator at %d, want it at the %d-character cap (slug %q)", idx, claudeSlugMaxUnits, got)
	}
	if hash := got[claudeSlugMaxUnits+1:]; hash == "" {
		t.Fatal("want a non-empty base-36 hash after the cap")
	}
	if short := claudeProjectSlug("/a/b"); strings.Contains(short, "--") {
		t.Fatalf("a short path must not be hashed: %q", short)
	}
}

// TestClaudeSlugHash pins the hash itself against values computed by the
// harness's own expression, h = h*31 + unit over UTF-16 code units in int32,
// then absolute. A drift here silently relocates every long project's index.
func TestClaudeSlugHash(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"a", 97},
		{"ab", 3105},   // 97*31 + 98
		{"abc", 96354}, // 3105*31 + 99
	}
	for _, tc := range tests {
		if got := claudeSlugHash(tc.in); got != tc.want {
			t.Errorf("claudeSlugHash(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestResolveConfigDir pins the one resolution every memory layer shares.
//
// The caller's ConfigDir wins because agent-deck resolves it per session; a
// transcript path is only a fallback, and it is read for the directory it lives
// under rather than trusted to locate anything else.
func TestResolveConfigDir(t *testing.T) {
	if got, want := resolveConfigDir(MemoryOptions{ConfigDir: "/scratch/home"}), filepath.Clean("/scratch/home"); got != want {
		t.Errorf("resolveConfigDir with an explicit dir = %q, want %q", got, want)
	}
	// A transcript under a *different* config dir must not override the resolved
	// one: that mismatch is precisely how the auto-memory row came to be built
	// from ~/.claude while every other row used the session's scratch home.
	got := resolveConfigDir(MemoryOptions{
		ConfigDir:      "/scratch/home",
		TranscriptPath: "/Users/x/.claude/projects/-a-b/uuid.jsonl",
	})
	if want := filepath.Clean("/scratch/home"); got != want {
		t.Errorf("resolveConfigDir = %q, want the explicit %q to win over the transcript's dir", got, want)
	}
	if got, want := resolveConfigDir(MemoryOptions{TranscriptPath: "/cfg/projects/-a-b/uuid.jsonl"}), filepath.Clean("/cfg"); got != want {
		t.Errorf("resolveConfigDir from a transcript = %q, want %q", got, want)
	}
	if got := resolveConfigDir(MemoryOptions{TranscriptPath: "/cfg/not-projects/uuid.jsonl"}); got != "" {
		t.Errorf("resolveConfigDir = %q, want empty when the transcript names no projects/ dir", got)
	}
	if got := resolveConfigDir(MemoryOptions{}); got != "" {
		t.Errorf("resolveConfigDir = %q, want empty when nothing locates it", got)
	}
}

// TestDiscoverMemoryCollapsesSymlinkAliases is the regression test for the
// defect a live /context capture exposed on 2026-07-29.
//
// A session's config dir was a per-session worker-scratch home whose CLAUDE.md
// was a symlink into the account profile, whose own CLAUDE.md was a symlink
// into ~/.claude. The walk reached that single file twice — once as the user
// global, once as an ancestor's .claude/CLAUDE.md — and charged for it twice,
// because it deduplicated on filepath.Clean and never resolved the link. The
// harness loads those bytes once.
func TestDiscoverMemoryCollapsesSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(home, "scratch")
	project := filepath.Join(home, "work", "repo")

	// The real file, and a two-hop symlink chain reaching it.
	real := writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "the only copy of these bytes")
	profile := filepath.Join(home, ".claude-profile", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(real, profile); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	scratch := filepath.Join(config, "CLAUDE.md")
	if err := os.Symlink(profile, scratch); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}

	// The walk reaches the file as <config>/CLAUDE.md (user global) and again
	// as <home>/.claude/CLAUDE.md (an ancestor's dot-claude). One row.
	if len(chain.Files) != 1 {
		t.Fatalf("chain = %v, want exactly one row: three paths, one inode, one cost", chainPaths(chain))
	}
	f := chain.Files[0]
	if f.Path != scratch {
		t.Fatalf("row path = %s, want the first path in load order (%s)", f.Path, scratch)
	}
	if len(f.Aliases) == 0 {
		t.Fatal("the collapsed alias must be reported: a reader looking for the other path has to be told it is the same file, not left to assume it was missed")
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if f.ResolvedPath != resolvedReal {
		t.Fatalf("resolved path = %s, want %s", f.ResolvedPath, resolvedReal)
	}
	for _, alias := range f.Aliases {
		if alias == f.Path {
			t.Fatalf("the row's own path %s must not also be listed as an alias of itself", alias)
		}
	}
}

// TestDiscoverMemoryChargesAnAliasedFileOnce states the arithmetic consequence
// directly: the chain's total character count is the file's, not a multiple of
// it. This is the assertion that would have caught the 8% overstatement.
func TestDiscoverMemoryChargesAnAliasedFileOnce(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(home, "scratch")
	project := filepath.Join(home, "work", "repo")

	body := strings.Repeat("x", 4096)
	real := writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), body)
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(real, filepath.Join(config, "CLAUDE.md")); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	total := 0
	for _, f := range chain.Files {
		total += f.Chars
	}
	if total != len(body) {
		t.Fatalf("chain charges %d characters for a %d-character file reached twice", total, len(body))
	}
}

// TestDiscoverMemorySurvivesUnresolvablePaths pins the failure behaviour. A
// symlink cycle, a permission-denied component or a race with a deletion all
// make resolution fail; none of them may drop a real file or crash the panel.
// Dedupe degrades to the literal path for that entry and the walk continues.
func TestDiscoverMemorySurvivesUnresolvablePaths(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "work", "repo")
	userMD := writeFile(t, filepath.Join(config, "CLAUDE.md"), "user global")
	projectMD := writeFile(t, filepath.Join(project, "CLAUDE.md"), "project")

	chain, err := DiscoverMemory(MemoryOptions{
		ConfigDir:   config,
		ProjectPath: project,
		Env:         emptyEnv,
		Resolve: func(string) (string, error) {
			return "", os.ErrPermission
		},
	})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	got := chainPaths(chain)
	want := []string{userMD, projectMD}
	if len(got) != len(want) {
		t.Fatalf("chain = %v, want %v: a resolution failure must degrade dedupe, not drop files", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	for _, f := range chain.Files {
		if f.ResolvedPath != "" {
			t.Fatalf("%s claims a resolved path %q although resolution failed", f.Path, f.ResolvedPath)
		}
	}
}

// TestDiscoverMemoryDedupesTheDirectAddPath guards the entry point that has no
// import provenance. Both add sites must share one seen-check; a second entry
// point with its own copy of the logic is exactly how a file comes to be
// counted twice.
func TestDiscoverMemoryDedupesTheDirectAddPath(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "work", "repo")
	writeFile(t, filepath.Join(config, "CLAUDE.md"), "user global")
	extra := writeFile(t, filepath.Join(root, "extra", "CLAUDE.md"), "additional directory")
	writeFile(t, filepath.Join(project, ".claude", "settings.json"),
		`{"additionalDirectories":["`+filepath.Join(root, "extra")+`","`+filepath.Join(root, "extra")+`"]}`)
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	seen := 0
	for _, f := range chain.Files {
		if f.Path == extra {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("%s appears %d times; one additional directory listed twice is still one file", extra, seen)
	}
}
