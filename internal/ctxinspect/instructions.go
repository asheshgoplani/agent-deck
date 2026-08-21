package ctxinspect

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InstructionScope says which layer of the hierarchy an instruction file came
// from. Rendering the hierarchy is the point of the memory section: a user who
// cannot see that three ancestors each contribute a file cannot decide which
// one to trim.
type InstructionScope uint8

const (
	// ScopeUser: the user's own configuration directory (global instructions).
	ScopeUser InstructionScope = iota
	// ScopeAncestor: a directory above the project root.
	ScopeAncestor
	// ScopeProject: the session's working directory itself.
	ScopeProject
)

var (
	instructionScopeNames = map[InstructionScope]string{
		ScopeUser:     "user",
		ScopeAncestor: "ancestor",
		ScopeProject:  "project",
	}
	instructionScopeByName = invertEnum(instructionScopeNames)
)

// String returns the wire/display name.
func (s InstructionScope) String() string { return enumName(s, instructionScopeNames, "project") }

// MarshalJSON encodes the value as its wire name.
func (s InstructionScope) MarshalJSON() ([]byte, error) {
	return marshalEnum(s, instructionScopeNames)
}

// UnmarshalJSON decodes the value from its wire name.
func (s *InstructionScope) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, instructionScopeByName, s, "instruction scope")
}

// InstructionSpec describes a harness's instruction-file convention.
type InstructionSpec struct {
	// ProjectNames are the file names looked for in the project and each of its
	// ancestors, in load order.
	ProjectNames []string
	// UserNames are the file names looked for in the harness's user config
	// directory.
	UserNames []string
	// Verified reports whether the convention is documented and confirmed. When
	// false the adapter emits a note saying the discovery is a best guess, so a
	// missing or spurious file is never presented as fact.
	Verified bool
	// Note explains anything unusual about the convention.
	Note string
}

// instructionSpecs holds the per-tool conventions. Only entries confirmed
// against the harness's own documentation or against agent-deck's existing
// per-harness tables are marked Verified; the rest carry the emerging AGENTS.md
// convention and say out loud that it is unconfirmed.
//
// Claude Code deliberately does NOT list AGENTS.md: it does not read that file,
// and showing it would invent context that is not there.
var instructionSpecs = map[string]InstructionSpec{
	"claude": {
		ProjectNames: []string{"CLAUDE.md", "CLAUDE.local.md"},
		UserNames:    []string{"CLAUDE.md"},
		Verified:     true,
		Note:         "Claude Code does not read AGENTS.md, so it is deliberately not listed here.",
	},
	"codex": {
		ProjectNames: []string{"AGENTS.md"},
		UserNames:    []string{"AGENTS.md"},
		Verified:     true,
	},
	"hermes": {
		ProjectNames: []string{"HERMES.md"},
		UserNames:    []string{"HERMES.md"},
		Verified:     true,
	},
	"gemini": {
		ProjectNames: []string{"GEMINI.md"},
		UserNames:    []string{"GEMINI.md"},
		Verified:     true,
	},
	"opencode": {
		ProjectNames: []string{"AGENTS.md"},
		UserNames:    []string{"AGENTS.md"},
		Verified:     true,
	},
	"crush": {
		ProjectNames: []string{"CRUSH.md", "AGENTS.md"},
		Verified:     false,
		Note:         "CRUSH.md is the documented name; the AGENTS.md fallback is unconfirmed.",
	},
	"cursor": {
		ProjectNames: []string{"AGENTS.md", ".cursorrules"},
		Verified:     false,
		Note:         "cursor-agent's instruction-file precedence is not documented; both names are checked and neither is confirmed to load.",
	},
	"copilot": {
		ProjectNames: []string{filepath.Join(".github", "copilot-instructions.md"), "AGENTS.md"},
		Verified:     false,
		Note:         "the .github path is the documented Copilot convention; the AGENTS.md fallback is unconfirmed.",
	},
	"aider": {
		ProjectNames: []string{"CONVENTIONS.md"},
		Verified:     false,
		Note:         "aider loads CONVENTIONS.md only when it is passed as a read-only file; presence on disk does not prove it is loaded.",
	},
}

// defaultInstructionSpec is used for tools with no entry, including "shell" and
// unrecognised custom tools.
var defaultInstructionSpec = InstructionSpec{
	ProjectNames: []string{"AGENTS.md"},
	Verified:     false,
	Note:         "no instruction-file convention is known for this tool; AGENTS.md is checked as the emerging cross-tool default and its presence does not prove the agent reads it.",
}

// InstructionSpecFor returns the instruction-file convention for a tool.
// Compatibility is consulted first so a custom [tools.*] entry wrapping claude
// or codex inherits that harness's convention instead of falling through to the
// unconfirmed default.
func InstructionSpecFor(tool string, host Host) InstructionSpec {
	if host == nil {
		host = NoHost()
	}
	if spec, ok := instructionSpecs[tool]; ok {
		return spec
	}
	switch {
	case host.IsClaudeCompatible(tool):
		return instructionSpecs["claude"]
	case host.IsCodexCompatible(tool):
		return instructionSpecs["codex"]
	}
	return defaultInstructionSpec
}

// InstructionFile is one discovered instruction file.
type InstructionFile struct {
	// Path is absolute.
	Path string
	// Name is the convention name that matched.
	Name string
	// Scope says which layer it came from.
	Scope InstructionScope
	// Depth is 0 for the user scope and, within the project chain, counts down
	// from the filesystem root so that a smaller number means "further from the
	// project" — the order the harness concatenates them in.
	Depth int
	// Size is the file size in bytes.
	Size int64
	// Content is the file's text, empty when reading was not requested.
	Content string
	// Chars is the character count of Content.
	Chars int
	// Truncated reports that Content holds only the first MaxBytes of the file.
	Truncated bool
	// ReadErr, when non-empty, is why the file was found but could not be read.
	// The file still appears in the report; a silently dropped file would be a
	// gap in the very inventory the panel exists to provide.
	ReadErr string
}

// defaultInstructionMaxBytes bounds a single instruction file read. Instruction
// files are prose; anything past a megabyte is pathological, and the panel runs
// on a render path that must not be able to exhaust memory.
const defaultInstructionMaxBytes = 1 << 20

// defaultInstructionMaxFiles bounds the number of files a single walk returns,
// so a deeply nested project on a pathological filesystem cannot produce an
// unbounded report.
const defaultInstructionMaxFiles = 256

// InstructionWalkOptions configures [DiscoverInstructionFiles].
type InstructionWalkOptions struct {
	// Spec is the convention to apply.
	Spec InstructionSpec
	// ProjectPath is the session's working directory. Ancestors are walked from
	// the filesystem root down to it.
	ProjectPath string
	// UserDirs are the harness config directories searched for UserNames, in
	// order.
	UserDirs []string
	// ReadContent requests file contents. When false only sizes are recorded,
	// which is enough for a size-ranked breakdown and costs one stat per file.
	ReadContent bool
	// MaxBytes bounds a single file read. Zero means
	// [defaultInstructionMaxBytes].
	MaxBytes int64
	// MaxFiles bounds the result length. Zero means
	// [defaultInstructionMaxFiles].
	MaxFiles int
}

// DiscoverInstructionFiles walks a harness's instruction-file hierarchy and
// returns what exists, in load order: user scope first, then each ancestor from
// the filesystem root down to the project directory.
//
// It never follows an ancestor chain outside the filesystem, performs at most
// one stat per candidate, and bounds every read. A file that exists but cannot
// be read is returned with [InstructionFile.ReadErr] set rather than dropped:
// omitting it would be a hole in the inventory, which is the one thing this
// package must not have.
//
// An error is returned only when the request itself is unusable — for example a
// project path that cannot be made absolute.
func DiscoverInstructionFiles(opts InstructionWalkOptions) ([]InstructionFile, error) {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultInstructionMaxBytes
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultInstructionMaxFiles
	}

	var out []InstructionFile
	seen := make(map[string]bool)

	add := func(path, name string, scope InstructionScope, depth int) bool {
		if len(out) >= maxFiles {
			return false
		}
		if seen[path] {
			return true
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return true
		}
		seen[path] = true
		f := InstructionFile{Path: path, Name: name, Scope: scope, Depth: depth, Size: info.Size()}
		if opts.ReadContent {
			content, truncated, rerr := readBounded(path, maxBytes)
			if rerr != nil {
				f.ReadErr = rerr.Error()
			} else {
				f.Content = content
				f.Chars = len([]rune(content))
				f.Truncated = truncated
			}
		}
		out = append(out, f)
		return true
	}

	for _, dir := range opts.UserDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, name := range opts.Spec.UserNames {
			if !add(filepath.Join(dir, name), name, ScopeUser, 0) {
				return out, nil
			}
		}
	}

	if strings.TrimSpace(opts.ProjectPath) != "" {
		project, err := filepath.Abs(opts.ProjectPath)
		if err != nil {
			return nil, fmt.Errorf("ctxinspect: resolving project path %q: %w", opts.ProjectPath, err)
		}
		chain := ancestorChain(project)
		for depth, dir := range chain {
			scope := ScopeAncestor
			if dir == project {
				scope = ScopeProject
			}
			for _, name := range opts.Spec.ProjectNames {
				if !add(filepath.Join(dir, name), name, scope, depth+1) {
					return out, nil
				}
			}
		}
	}

	return out, nil
}

// ancestorChain returns every directory from the filesystem root down to and
// including dir. The order matches the order harnesses concatenate instruction
// files in: the furthest ancestor first, the working directory last.
func ancestorChain(dir string) []string {
	var reversed []string
	for {
		reversed = append(reversed, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	chain := make([]string, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		chain = append(chain, reversed[i])
	}
	return chain
}

// readBounded reads at most maxBytes from path and reports whether the file was
// longer. It reads one byte past the limit to distinguish "exactly at the
// limit" from "truncated", so a report never claims a complete file it only
// partly holds.
func readBounded(path string, maxBytes int64) (content string, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()

	buf, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", false, err
	}
	if int64(len(buf)) > maxBytes {
		return string(buf[:maxBytes]), true, nil
	}
	return string(buf), false, nil
}
