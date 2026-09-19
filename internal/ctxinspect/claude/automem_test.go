package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// gitRepo makes dir look like the root of a git repository to the resolver: a
// .git directory is all the ancestor walk checks for.
func gitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("creating %s/.git: %v", dir, err)
	}
	return dir
}

// TestDiscoverMemoryAutoMemoryUsesRepoRootSlugInNonDefaultConfigDir is the
// regression test for the defect a live session exposed on 2026-07-30.
//
// The shape is the live one exactly, because both halves of the defect were
// needed to produce it:
//
//   - the session's cwd is a *subdirectory* of a git repository, and Claude Code
//     files auto-memory under the slug of the repository root, so a cwd-derived
//     slug points at the wrong directory; and
//   - the session's config dir is a per-session scratch home, not ~/.claude, so
//     a path built from anything but the resolved config dir points at the wrong
//     tree as well.
//
// On the live session the two wrongs landed on a 130-byte leftover stub, priced
// at ~123 tokens, and the row's lever told the user to edit it — while the index
// the session loads was 17,506 bytes.
func TestDiscoverMemoryAutoMemoryUsesRepoRootSlugInNonDefaultConfigDir(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "worker-scratch", "session-1")
	profile := filepath.Join(root, "profile-home")
	repo := gitRepo(t, filepath.Join(root, "repo"))
	cwd := filepath.Join(repo, "conductor", "agent-deck")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("creating %s: %v", cwd, err)
	}

	// The index the session loads: repo-root slug, scratch config dir.
	loaded := writeFile(t,
		filepath.Join(scratch, "projects", claudeProjectSlug(repo), "memory", "MEMORY.md"),
		strings.Repeat("real index\n", 200))
	// The stub an older Claude Code left under the cwd slug, in both trees.
	stubScratch := writeFile(t,
		filepath.Join(scratch, "projects", claudeProjectSlug(cwd), "memory", "MEMORY.md"), "stub\n")
	stubProfile := writeFile(t,
		filepath.Join(profile, "projects", claudeProjectSlug(cwd), "memory", "MEMORY.md"), "stub\n")
	// The transcript is filed under the *cwd* slug — and, as on the live
	// session, it was found in the profile home rather than the scratch one.
	transcript := writeFile(t,
		filepath.Join(profile, "projects", claudeProjectSlug(cwd), "uuid.jsonl"), "{}\n")

	chain, err := DiscoverMemory(MemoryOptions{
		ConfigDir:      scratch,
		ProjectPath:    cwd,
		TranscriptPath: transcript,
		Env:            emptyEnv,
	})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}

	if got := chain.AutoMem.Path; got != loaded {
		t.Fatalf("auto-memory path = %q, want %q", got, loaded)
	}
	if !chain.AutoMem.Confident {
		t.Error("want the resolution reported as confident: it follows the rule and the file is there")
	}
	if chain.AutoMem.Basis != autoMemFromGitRoot {
		t.Errorf("basis = %d, want the git-root basis", chain.AutoMem.Basis)
	}
	if chain.AutoMem.ProjectRoot != repo {
		t.Errorf("project root = %q, want the repository root %q", chain.AutoMem.ProjectRoot, repo)
	}
	if chain.ConfigDir != scratch {
		t.Errorf("chain config dir = %q, want the session's scratch home %q", chain.ConfigDir, scratch)
	}

	var autoMem *MemoryFile
	for i := range chain.Files {
		if chain.Files[i].Layer == LayerAutoMem {
			if autoMem != nil {
				t.Fatalf("two auto-memory files in the chain: %s and %s", autoMem.Path, chain.Files[i].Path)
			}
			autoMem = &chain.Files[i]
		}
	}
	if autoMem == nil {
		t.Fatalf("no auto-memory file in the chain: %v", chainPaths(chain))
	}
	if autoMem.Path != loaded {
		t.Errorf("chain auto-memory = %q, want %q", autoMem.Path, loaded)
	}
	if autoMem.Size < 1000 {
		t.Errorf("auto-memory size = %d, want the real index's bytes, not a stub's", autoMem.Size)
	}
	for _, stub := range []string{stubScratch, stubProfile} {
		for _, f := range chain.Files {
			if f.Path == stub {
				t.Errorf("%s is a leftover stub and must not be in the chain", stub)
			}
		}
	}
	// The stub under the same config dir is a candidate slug, so it is reported
	// as superseded: a user who finds it must be told it is not read. The one in
	// the other config dir is not this session's tree at all and is not listed.
	if got := chain.AutoMem.Superseded; len(got) != 1 || got[0] != stubScratch {
		t.Errorf("superseded = %v, want exactly [%s]", got, stubScratch)
	}
}

// TestResolveAutoMemoryFallsBackToExistingIndexAndSaysSo covers the honest
// unknown. When no index exists where the rule puts it, an index that does exist
// under a candidate slug is a better answer than silence — but it is a guess,
// and the resolution must not report it as settled.
func TestResolveAutoMemoryFallsBackToExistingIndexAndSaysSo(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	repo := gitRepo(t, filepath.Join(root, "repo"))
	cwd := filepath.Join(repo, "sub", "dir")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("creating %s: %v", cwd, err)
	}
	// Only the cwd-scoped index exists, which is where an older Claude Code
	// filed it.
	legacy := writeFile(t, filepath.Join(config, "projects", claudeProjectSlug(cwd), "memory", "MEMORY.md"), "legacy index\n")

	auto := resolveAutoMemory(config, cwd, mergedSettings{}, emptyEnv, os.Stat)
	if auto.Path != legacy {
		t.Fatalf("path = %q, want the only index that exists, %q", auto.Path, legacy)
	}
	if auto.Confident {
		t.Error("want Confident false: the rule's location is empty, so this is inferred")
	}
	if auto.Basis != autoMemFromExistingIndex {
		t.Errorf("basis = %d, want the existing-index basis", auto.Basis)
	}
	if want := filepath.Join(config, "projects", claudeProjectSlug(repo), "memory", "MEMORY.md"); !strings.Contains(auto.Reason, want) {
		t.Errorf("reason %q does not name the location the rule predicts (%s)", auto.Reason, want)
	}
	if len(auto.Candidates) == 0 || auto.Candidates[0] != filepath.Join(config, "projects", claudeProjectSlug(repo), "memory", "MEMORY.md") {
		t.Errorf("candidates = %v, want the rule's own answer first", auto.Candidates)
	}
}

// TestResolveAutoMemoryEmptyIndexIsNotEvidence keeps a zero-byte file from
// winning the fallback ladder: an empty index proves nothing about which file
// the session loads.
func TestResolveAutoMemoryEmptyIndexIsNotEvidence(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	repo := gitRepo(t, filepath.Join(root, "repo"))
	cwd := filepath.Join(repo, "sub")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("creating %s: %v", cwd, err)
	}
	writeFile(t, filepath.Join(config, "projects", claudeProjectSlug(cwd), "memory", "MEMORY.md"), "")

	auto := resolveAutoMemory(config, cwd, mergedSettings{}, emptyEnv, os.Stat)
	if auto.Basis != autoMemAbsent {
		t.Fatalf("basis = %d, want absent when the only index on disk is empty", auto.Basis)
	}
	if want := filepath.Join(config, "projects", claudeProjectSlug(repo), "memory", "MEMORY.md"); auto.Path != want {
		t.Errorf("path = %q, want the rule's own location %q so the note can name it", auto.Path, want)
	}
}

// TestResolveAutoMemoryWithoutRepositoryUsesWorkingDir pins the other half of
// the rule: outside a repository the working directory's own slug is correct.
func TestResolveAutoMemoryWithoutRepositoryUsesWorkingDir(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "plain", "dir")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("creating %s: %v", project, err)
	}
	want := writeFile(t, filepath.Join(config, "projects", claudeProjectSlug(project), "memory", "MEMORY.md"), "index\n")

	auto := resolveAutoMemory(config, project, mergedSettings{}, emptyEnv, os.Stat)
	if auto.Path != want {
		t.Fatalf("path = %q, want %q", auto.Path, want)
	}
	if auto.Basis != autoMemFromWorkingDir {
		t.Errorf("basis = %d, want the working-directory basis", auto.Basis)
	}
	if !auto.Confident {
		t.Error("want Confident true: the rule's location holds the index")
	}
}

// TestCanonicalGitRootHopsFromLinkedWorktreeToMainRepo pins the worktree hop.
// Every worktree of a repository shares the repository's one auto-memory index,
// so a worktree session must resolve to the main working tree's slug.
func TestCanonicalGitRootHopsFromLinkedWorktreeToMainRepo(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "repo")
	commonGit := filepath.Join(main, ".git")
	wt := filepath.Join(root, "wt", "feature")
	wtGitDir := filepath.Join(commonGit, "worktrees", "feature")

	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", wtGitDir, err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("creating %s: %v", wt, err)
	}
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+wtGitDir+"\n")
	writeFile(t, filepath.Join(wtGitDir, "commondir"), "../..\n")

	got, inRepo := canonicalGitRoot(filepath.Join(wt, "nested"), os.Stat)
	if !inRepo {
		t.Fatal("want a repository found from inside a linked worktree")
	}
	if got != main {
		t.Fatalf("canonical root = %q, want the main working tree %q", got, main)
	}

	// A gitdir: pointer whose commondir does not describe the worktrees layout
	// is not trusted: a stray file must not relocate a session's whole index.
	bogus := filepath.Join(root, "bogus")
	if err := os.MkdirAll(bogus, 0o755); err != nil {
		t.Fatalf("creating %s: %v", bogus, err)
	}
	writeFile(t, filepath.Join(bogus, ".git"), "gitdir: "+filepath.Join(root, "elsewhere")+"\n")
	writeFile(t, filepath.Join(root, "elsewhere", "commondir"), "../other\n")
	if got, _ := canonicalGitRoot(bogus, os.Stat); got != bogus {
		t.Fatalf("canonical root = %q, want the directory itself %q when the pointer chain is inconsistent", got, bogus)
	}
}

// TestResolveAutoMemoryHonoursOverrides covers the two ways a session can be
// told where its auto-memory lives, and the one way it can be switched off. A
// row must never price a file the harness is not reading.
func TestResolveAutoMemoryHonoursOverrides(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("creating %s: %v", project, err)
	}

	t.Run("env override", func(t *testing.T) {
		dir := filepath.Join(root, "env-mem")
		want := writeFile(t, filepath.Join(dir, "MEMORY.md"), "env index\n")
		auto := resolveAutoMemory(config, project, mergedSettings{},
			envMap(map[string]string{autoMemOverrideEnv: dir}), os.Stat)
		if auto.Path != want {
			t.Fatalf("path = %q, want %q", auto.Path, want)
		}
		if auto.Basis != autoMemFromOverride {
			t.Errorf("basis = %d, want the override basis", auto.Basis)
		}
	})

	t.Run("settings override", func(t *testing.T) {
		dir := filepath.Join(root, "settings-mem")
		want := writeFile(t, filepath.Join(dir, "MEMORY.md"), "settings index\n")
		settings := mergedSettings{AutoMemDir: dir, AutoMemDirSource: "/x/settings.json"}
		auto := resolveAutoMemory(config, project, settings, emptyEnv, os.Stat)
		if auto.Path != want {
			t.Fatalf("path = %q, want %q", auto.Path, want)
		}
		if !strings.Contains(auto.Reason, "/x/settings.json") {
			t.Errorf("reason %q does not name the settings file that set it", auto.Reason)
		}
	})

	t.Run("relative override is ignored", func(t *testing.T) {
		settings := mergedSettings{AutoMemDir: "relative/mem"}
		auto := resolveAutoMemory(config, project, settings, emptyEnv, os.Stat)
		if auto.Basis == autoMemFromOverride {
			t.Fatalf("path = %q, want a relative override ignored the way the harness ignores it", auto.Path)
		}
	})

	t.Run("remote memory dir replaces the config dir", func(t *testing.T) {
		remote := filepath.Join(root, "remote-home")
		want := writeFile(t, filepath.Join(remote, "projects", claudeProjectSlug(project), "memory", "MEMORY.md"), "remote index\n")
		auto := resolveAutoMemory(config, project, mergedSettings{},
			envMap(map[string]string{autoMemRemoteDirEnv: remote}), os.Stat)
		if auto.Path != want {
			t.Fatalf("path = %q, want %q", auto.Path, want)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		off := false
		auto := resolveAutoMemory(config, project, mergedSettings{AutoMemEnabled: &off}, emptyEnv, os.Stat)
		if !auto.Disabled {
			t.Fatal("want the layer reported as disabled")
		}
		if auto.Path != "" {
			t.Errorf("path = %q, want none when auto-memory is off", auto.Path)
		}
	})
}

// TestDiscoverMemorySkipsAutoMemoryWhenDisabled keeps a switched-off index out of
// the chain even though the file is right there on disk.
func TestDiscoverMemorySkipsAutoMemoryWhenDisabled(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("creating %s: %v", project, err)
	}
	writeFile(t, filepath.Join(config, "settings.json"), `{"autoMemoryEnabled": false}`)
	writeFile(t, filepath.Join(config, "projects", claudeProjectSlug(project), "memory", "MEMORY.md"), "index\n")

	chain, err := DiscoverMemory(MemoryOptions{ConfigDir: config, ProjectPath: project, Env: emptyEnv})
	if err != nil {
		t.Fatalf("DiscoverMemory: %v", err)
	}
	if !chain.AutoMem.Disabled {
		t.Error("want the auto-memory resolution reported as disabled")
	}
	for _, f := range chain.Files {
		if f.Layer == LayerAutoMem {
			t.Fatalf("%s must not be priced when autoMemoryEnabled is false", f.Path)
		}
	}
}

// TestInspectAutoMemoryRowNamesTheFileTheUserMustEdit is the end-to-end half of
// the 2026-07-30 regression, at the level the user sees: the row's label, its
// lever and its figure must all describe the index the session loads, and the
// stale index left under the working directory's slug must be called out rather
// than silently offered as the thing to edit.
func TestInspectAutoMemoryRowNamesTheFileTheUserMustEdit(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "worker-scratch", "session-1")
	repo := gitRepo(t, filepath.Join(root, "repo"))
	cwd := filepath.Join(repo, "conductor", "agent-deck")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("creating %s: %v", cwd, err)
	}
	writeFile(t, filepath.Join(scratch, "CLAUDE.md"), "global instructions\n")
	loaded := writeFile(t,
		filepath.Join(scratch, "projects", claudeProjectSlug(repo), "memory", "MEMORY.md"),
		strings.Repeat("- [a memory](a.md) — one line of index\n", 300))
	stale := writeFile(t,
		filepath.Join(scratch, "projects", claudeProjectSlug(cwd), "memory", "MEMORY.md"),
		"- [stale](s.md) — left by an older version\n")

	rep := inspect(t, ctxinspect.Request{
		Tool: "claude", ProjectPath: cwd, ConfigDir: scratch,
	})
	mem, ok := rep.Category(CategoryMemory)
	if !ok {
		t.Fatal("want a memory category")
	}

	var row *ctxinspect.Item
	for i := range mem.Items {
		if strings.HasSuffix(mem.Items[i].Label, filepath.Join("memory", "MEMORY.md")) {
			if row != nil {
				t.Fatalf("two auto-memory rows: %s and %s", row.Label, mem.Items[i].Label)
			}
			row = &mem.Items[i]
		}
	}
	if row == nil {
		t.Fatal("want an auto-memory row")
	}
	if row.Label != loaded {
		t.Errorf("row label = %q, want the index the session loads, %q", row.Label, loaded)
	}
	if row.Lever.Path != loaded {
		t.Errorf("lever points at %q, want the file the user actually has to edit, %q", row.Lever.Path, loaded)
	}
	if row.Lever.Path == stale {
		t.Error("the lever points at the stale index an older Claude Code left behind")
	}
	if !strings.Contains(row.Lever.Why, repo) {
		t.Errorf("lever reason %q does not say the location comes from the repository root %s", row.Lever.Why, repo)
	}
	tokens, known := row.Load.Actual.Value()
	if !known {
		t.Fatal("want a known figure for a file that was read")
	}
	// The real index is ~11 KB here; the stub it used to be confused with is one
	// line. Any figure in the low hundreds means the wrong file was priced.
	if tokens < 1000 {
		t.Errorf("row priced at %d tokens, want the real index's cost — a figure this small means the stale one-line index was priced", tokens)
	}
	for _, c := range row.Caveats {
		if c.Code == "memory-automem-location-inferred" {
			t.Errorf("row carries an inferred-location caveat, but the rule's own location holds the index: %s", c.Message)
		}
	}
	var superseded bool
	for _, c := range rep.Caveats {
		if c.Code == "memory-automem-superseded" && strings.Contains(c.Message, stale) {
			superseded = true
		}
	}
	if !superseded {
		t.Errorf("want a caveat naming %s as an index on disk that this session does not load", stale)
	}
}
