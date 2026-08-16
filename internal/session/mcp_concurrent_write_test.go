package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent read-modify-write on the shared Claude config.
//
// The web MCP pane and the TUI MCP dialog both write .claude.json. Every writer
// here is read-modify-write: load the whole document, replace one section, save
// it all back. Two of those overlapping means the second reader loaded a
// document that predates the first writer, so saving it erases the first
// writer's section — silently, with both calls returning success.
//
// These tests drive the exact pairing a user can produce with both surfaces
// open: one writer owning the root mcpServers map, another owning
// projects[path].mcpServers, in the same file at the same time. Both sections
// must survive, and unrelated top-level keys must never be touched.

func concurrentWriteEnv(t *testing.T) (home, project string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-cfg"))
	if err := os.MkdirAll(filepath.Join(home, ".claude-cfg"), 0o755); err != nil {
		t.Fatalf("mkdir claude config dir: %v", err)
	}

	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	const catalog = `
[mcps.alpha]
command = "alpha-server"

[mcps.beta]
command = "beta-server"

[mcps.gamma]
command = "gamma-server"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(catalog), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if len(GetAvailableMCPs()) == 0 {
		t.Skip("catalog did not load from the temp config")
	}
	return home, t.TempDir()
}

func readClaudeDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("config is not valid JSON after concurrent writes — a write was torn or interleaved:\n%s\nerror: %v", data, err)
	}
	return doc
}

func rootMCPNames(doc map[string]any) []string {
	servers, _ := doc["mcpServers"].(map[string]any)
	out := make([]string, 0, len(servers))
	for k := range servers {
		out = append(out, k)
	}
	return out
}

func projectMCPNames(doc map[string]any, projectPath string) []string {
	projects, _ := doc["projects"].(map[string]any)
	proj, _ := projects[projectPath].(map[string]any)
	servers, _ := proj["mcpServers"].(map[string]any)
	out := make([]string, 0, len(servers))
	for k := range servers {
		out = append(out, k)
	}
	return out
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestConcurrentGlobalAndProjectWritesDoNotLoseEitherSection is the web+TUI
// case. Without the whole read-modify-write under one lock, one of the two
// sections vanishes.
func TestConcurrentGlobalAndProjectWritesDoNotLoseEitherSection(t *testing.T) {
	home, project := concurrentWriteEnv(t)
	configFile := filepath.Join(home, ".claude-cfg", ".claude.json")

	// Seed with an unrelated top-level key that neither writer owns; it must
	// survive every round untouched.
	seed := map[string]any{"numStartups": float64(7), "theme": "dark"}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Enough rounds that an unserialized interleaving is overwhelmingly likely.
	const rounds = 120
	for i := 0; i < rounds; i++ {
		// Reset both sections so each round is a clean race.
		if err := WriteGlobalMCP(nil); err != nil {
			t.Fatalf("round %d reset global: %v", i, err)
		}
		if err := WriteProjectMCP(project, nil); err != nil {
			t.Fatalf("round %d reset project: %v", i, err)
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = WriteGlobalMCP([]string{"alpha"}) }()
		go func() { defer wg.Done(); errs[1] = WriteProjectMCP(project, []string{"beta"}) }()
		wg.Wait()

		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d: concurrent write failed: %v", i, err)
			}
		}

		doc := readClaudeDoc(t, configFile)
		root := rootMCPNames(doc)
		proj := projectMCPNames(doc, project)
		if !hasName(root, "alpha") {
			t.Fatalf("round %d: the project write erased the global section (root=%v project=%v). "+
				"The whole read-modify-write must hold the config lock, not just the save.", i, root, proj)
		}
		if !hasName(proj, "beta") {
			t.Fatalf("round %d: the global write erased the project section (root=%v project=%v). "+
				"The whole read-modify-write must hold the config lock, not just the save.", i, root, proj)
		}
		if doc["numStartups"] == nil || doc["theme"] == nil {
			t.Fatalf("round %d: concurrent writes dropped unrelated top-level keys: %v", i, doc)
		}
	}
}

// TestConcurrentWritersToTheSameSectionKeepTheFileValid covers many writers on
// one section. Last-writer-wins on the managed set is the API's contract; a
// torn or unparseable file never is.
func TestConcurrentWritersToTheSameSectionKeepTheFileValid(t *testing.T) {
	home, _ := concurrentWriteEnv(t)
	configFile := filepath.Join(home, ".claude-cfg", ".claude.json")
	if err := os.WriteFile(configFile, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wg sync.WaitGroup
	const writers = 16
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			names := []string{"alpha", "beta", "gamma"}[:1+i%3]
			errs[i] = WriteGlobalMCP(names)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	doc := readClaudeDoc(t, configFile)
	if doc["theme"] != "dark" {
		t.Errorf("concurrent writers dropped an unrelated key: %v", doc)
	}
	if len(rootMCPNames(doc)) == 0 {
		t.Errorf("every concurrent writer's section was lost: %v", doc)
	}
}

// TestWriteGlobalMCPAndClearProjectMCPsHoldsOneLock is the atomicity assertion.
//
// The end state alone cannot tell the two implementations apart: taking the
// lock once across both writes and taking it twice finish identically, which is
// why the outcome-only version of this test passed even with the pair split
// back into two separate locked calls. Counting acquisitions is the property
// that actually differs.
func TestWriteGlobalMCPAndClearProjectMCPsHoldsOneLock(t *testing.T) {
	_, project := concurrentWriteEnv(t)

	before := ConfigLockAcquisitionsForTest()
	if err := WriteGlobalMCPAndClearProjectMCPs(project, []string{"alpha"}); err != nil {
		t.Fatalf("combined write: %v", err)
	}
	combined := ConfigLockAcquisitionsForTest() - before

	// The equivalent done as two public calls, for comparison.
	before = ConfigLockAcquisitionsForTest()
	if err := WriteGlobalMCP([]string{"alpha"}); err != nil {
		t.Fatalf("global: %v", err)
	}
	if err := ClearProjectMCPs(project); err != nil {
		t.Fatalf("clear: %v", err)
	}
	separate := ConfigLockAcquisitionsForTest() - before

	if combined != 1 {
		t.Errorf("the combined write took the config lock %d times, want exactly 1: the global "+
			"write and the project clear must span ONE lock, or another writer can observe the "+
			"new global set with the stale project set still merged in", combined)
	}
	if separate <= combined {
		t.Errorf("sanity check failed: two separate calls took %d locks, expected more than the "+
			"combined form's %d", separate, combined)
	}
}

// TestWriteGlobalMCPAndClearProjectMCPsAppliesBothHalves covers the outcome.
func TestWriteGlobalMCPAndClearProjectMCPsAppliesBothHalves(t *testing.T) {
	home, project := concurrentWriteEnv(t)
	configFile := filepath.Join(home, ".claude-cfg", ".claude.json")

	if err := WriteProjectMCP(project, []string{"beta"}); err != nil {
		t.Fatalf("seed project scope: %v", err)
	}
	if names := projectMCPNames(readClaudeDoc(t, configFile), project); !hasName(names, "beta") {
		t.Fatalf("precondition: project scope should hold beta, got %v", names)
	}

	if err := WriteGlobalMCPAndClearProjectMCPs(project, []string{"alpha"}); err != nil {
		t.Fatalf("combined write: %v", err)
	}

	doc := readClaudeDoc(t, configFile)
	if !hasName(rootMCPNames(doc), "alpha") {
		t.Errorf("global half did not apply: %v", rootMCPNames(doc))
	}
	if names := projectMCPNames(doc, project); hasName(names, "beta") {
		t.Errorf("project half was not cleared, so a removed MCP still reads as attached: %v", names)
	}
}

// TestClearProjectMCPsOnAbsentConfigIsANoOp pins the inverted-bug fix. The
// hand-rolled read this replaced errored when the config did not exist yet;
// nothing to clear is success.
func TestClearProjectMCPsOnAbsentConfigIsANoOp(t *testing.T) {
	home, project := concurrentWriteEnv(t)
	configFile := filepath.Join(home, ".claude-cfg", ".claude.json")
	if err := os.Remove(configFile); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove config: %v", err)
	}

	if err := ClearProjectMCPs(project); err != nil {
		t.Errorf("clearing an absent config should be a no-op, got: %v", err)
	}
	if _, err := os.Stat(configFile); err == nil {
		t.Error("clearing an absent config created one; it should not have written anything")
	}
}

// TestResolveConfigLockPathRejectsEscapes pins the lock-path derivation. The
// lock must always land beside the config it guards, whatever spelling the
// caller used, and equivalent spellings must resolve to the same mutex key so
// two callers cannot each get "their own" lock on one file.
func TestResolveConfigLockPathRejectsEscapes(t *testing.T) {
	dir := t.TempDir()

	resolved, lockPath, err := resolveConfigLockPath(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("plain path: %v", err)
	}
	if want := filepath.Join(dir, ".claude.json.lock"); lockPath != want {
		t.Errorf("lockPath = %q, want %q", lockPath, want)
	}
	if filepath.Dir(lockPath) != filepath.Dir(resolved) {
		t.Errorf("lock %q is not beside its config %q", lockPath, resolved)
	}

	// A path with traversal segments must normalize to the same place, not
	// escape it, and must share the resolved key.
	messy := filepath.Join(dir, "sub", "..", ".claude.json")
	resolved2, lockPath2, err := resolveConfigLockPath(messy)
	if err != nil {
		t.Fatalf("traversal path: %v", err)
	}
	if resolved2 != resolved || lockPath2 != lockPath {
		t.Errorf("equivalent spellings resolved differently: %q/%q vs %q/%q",
			resolved2, lockPath2, resolved, lockPath)
	}

	for _, bad := range []string{"", "   "} {
		if _, _, err := resolveConfigLockPath(bad); err == nil {
			t.Errorf("resolveConfigLockPath(%q) should be refused", bad)
		}
	}
	if _, _, err := resolveConfigLockPath("/"); err == nil {
		t.Error("a path with no file name component should be refused")
	}
}
