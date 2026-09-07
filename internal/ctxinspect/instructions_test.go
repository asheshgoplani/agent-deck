package ctxinspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestInstructionSpecForKnownTools(t *testing.T) {
	tests := []struct {
		tool      string
		wantFirst string
		verified  bool
	}{
		{"claude", "CLAUDE.md", true},
		{"codex", "AGENTS.md", true},
		{"hermes", "HERMES.md", true},
		{"gemini", "GEMINI.md", true},
		{"cursor", "AGENTS.md", false},
		{"aider", "CONVENTIONS.md", false},
		{"shell", "AGENTS.md", false},
		{"some-unknown-tool", "AGENTS.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			spec := InstructionSpecFor(tt.tool, NoHost())
			if len(spec.ProjectNames) == 0 || spec.ProjectNames[0] != tt.wantFirst {
				t.Fatalf("first project name = %v, want %q", spec.ProjectNames, tt.wantFirst)
			}
			if spec.Verified != tt.verified {
				t.Fatalf("verified = %v, want %v", spec.Verified, tt.verified)
			}
			if !spec.Verified && spec.Note == "" {
				t.Fatal("an unverified convention must carry a note saying so")
			}
		})
	}
}

func TestClaudeSpecDoesNotClaimAgentsMd(t *testing.T) {
	// Claude Code does not read AGENTS.md. Listing it would invent context
	// that is not there, which is the exact failure this feature prevents.
	spec := InstructionSpecFor("claude", NoHost())
	for _, name := range append(append([]string{}, spec.ProjectNames...), spec.UserNames...) {
		if name == "AGENTS.md" {
			t.Fatal("AGENTS.md must not appear in the Claude Code instruction chain")
		}
	}
}

func TestInstructionSpecFollowsHostCompatibility(t *testing.T) {
	host := &StaticHost{ClaudeTools: []string{"claude-yolo"}, CodexTools: []string{"codex-wrap"}}
	if got := InstructionSpecFor("claude-yolo", host); got.ProjectNames[0] != "CLAUDE.md" {
		t.Fatalf("a custom tool wrapping claude must inherit CLAUDE.md, got %v", got.ProjectNames)
	}
	if got := InstructionSpecFor("codex-wrap", host); got.ProjectNames[0] != "AGENTS.md" {
		t.Fatalf("a custom tool wrapping codex must inherit AGENTS.md, got %v", got.ProjectNames)
	}
	if got := InstructionSpecFor("claude-yolo", NoHost()); got.Verified {
		t.Fatal("with no host wired the tool is unrecognised and must fall back to the unverified default")
	}
}

func TestDiscoverInstructionFilesWalksTheHierarchyInLoadOrder(t *testing.T) {
	root := t.TempDir()
	userDir := filepath.Join(root, "userconfig")
	project := filepath.Join(root, "work", "repo", "sub")

	writeFile(t, filepath.Join(userDir, "CLAUDE.md"), "global rules")
	writeFile(t, filepath.Join(root, "work", "CLAUDE.md"), "org rules")
	writeFile(t, filepath.Join(root, "work", "repo", "CLAUDE.md"), "repo rules")
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "subdir rules")

	files, err := DiscoverInstructionFiles(InstructionWalkOptions{
		Spec:        instructionSpecs["claude"],
		ProjectPath: project,
		UserDirs:    []string{userDir},
		ReadContent: true,
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("found %d files, want 4: %+v", len(files), files)
	}

	wantOrder := []string{"global rules", "org rules", "repo rules", "subdir rules"}
	for i, want := range wantOrder {
		if files[i].Content != want {
			t.Fatalf("position %d holds %q, want %q: the walk must run from the furthest ancestor down to the working directory", i, files[i].Content, want)
		}
	}
	if files[0].Scope != ScopeUser {
		t.Fatalf("first scope = %s, want user", files[0].Scope)
	}
	if files[3].Scope != ScopeProject {
		t.Fatalf("last scope = %s, want project", files[3].Scope)
	}
	if files[1].Scope != ScopeAncestor {
		t.Fatalf("middle scope = %s, want ancestor", files[1].Scope)
	}
	if files[1].Depth >= files[2].Depth {
		t.Fatal("depth must increase as the walk approaches the project directory")
	}
}

func TestDiscoverInstructionFilesRecordsSizeWithoutReading(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "AGENTS.md"), strings.Repeat("x", 1234))

	files, err := DiscoverInstructionFiles(InstructionWalkOptions{
		Spec:        instructionSpecs["codex"],
		ProjectPath: project,
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("found %d files, want 1", len(files))
	}
	if files[0].Size != 1234 {
		t.Fatalf("size = %d, want 1234", files[0].Size)
	}
	if files[0].Content != "" {
		t.Fatal("contents must not be read unless requested")
	}
}

func TestDiscoverInstructionFilesBoundsEveryRead(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "AGENTS.md"), strings.Repeat("y", 5000))

	files, err := DiscoverInstructionFiles(InstructionWalkOptions{
		Spec:        instructionSpecs["codex"],
		ProjectPath: project,
		ReadContent: true,
		MaxBytes:    100,
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got := len(files[0].Content); got != 100 {
		t.Fatalf("read %d bytes, want the 100-byte cap honoured: an unbounded read on a render path can exhaust memory", got)
	}
	if !files[0].Truncated {
		t.Fatal("a partly-held file must say so; claiming a complete file we only partly hold is the worst outcome here")
	}
	if files[0].Size != 5000 {
		t.Fatalf("size = %d, want the full 5000 so the user sees what was trimmed", files[0].Size)
	}
}

func TestDiscoverInstructionFilesCapsTheResultLength(t *testing.T) {
	root := t.TempDir()
	dir := root
	for i := 0; i < 6; i++ {
		dir = filepath.Join(dir, "d")
		writeFile(t, filepath.Join(dir, "AGENTS.md"), "x")
	}
	files, err := DiscoverInstructionFiles(InstructionWalkOptions{
		Spec:        instructionSpecs["codex"],
		ProjectPath: dir,
		MaxFiles:    3,
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("returned %d files, want the 3-file cap honoured", len(files))
	}
}

func TestDiscoverInstructionFilesKeepsUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000 file")
	}
	project := t.TempDir()
	path := filepath.Join(project, "AGENTS.md")
	writeFile(t, path, "secret")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	files, err := DiscoverInstructionFiles(InstructionWalkOptions{
		Spec:        instructionSpecs["codex"],
		ProjectPath: project,
		ReadContent: true,
	})
	if err != nil {
		t.Fatalf("one unreadable file must not fail the whole walk: %v", err)
	}
	if len(files) != 1 {
		t.Fatal("an unreadable file must still appear: dropping it would leave a hole in the inventory")
	}
	if files[0].ReadErr == "" {
		t.Fatal("the read failure must be recorded so the UI can show it")
	}
}

func TestDiscoverInstructionFilesDeduplicates(t *testing.T) {
	// A user dir that is also an ancestor of the project must not yield the
	// same file twice.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "shared")
	project := filepath.Join(root, "child")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	spec := InstructionSpec{ProjectNames: []string{"AGENTS.md"}, UserNames: []string{"AGENTS.md"}, Verified: true}
	files, err := DiscoverInstructionFiles(InstructionWalkOptions{
		Spec:        spec,
		ProjectPath: project,
		UserDirs:    []string{root},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("found %d entries for one file, want 1", len(files))
	}
}

func TestAncestorChainRunsRootToLeaf(t *testing.T) {
	chain := ancestorChain(filepath.Join(string(filepath.Separator), "a", "b", "c"))
	if len(chain) < 4 {
		t.Fatalf("chain = %v, want root plus three levels", chain)
	}
	if chain[len(chain)-1] != filepath.Join(string(filepath.Separator), "a", "b", "c") {
		t.Fatalf("last element = %q, want the leaf", chain[len(chain)-1])
	}
	if chain[0] != string(filepath.Separator) {
		t.Fatalf("first element = %q, want the filesystem root", chain[0])
	}
}
