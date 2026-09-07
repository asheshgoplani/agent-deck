package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Memory-chain bounds. Claude Code caps a single memory file at 4 MB and
// follows @-imports five levels deep; both are reproduced so the reconstruction
// matches what the harness would actually load rather than what the filesystem
// happens to contain.
const (
	// maxMemoryFileBytes is Claude Code's per-file cap.
	maxMemoryFileBytes = 4 << 20
	// maxImportDepth is Claude Code's @-import recursion limit.
	maxImportDepth = 5
	// maxMemoryFiles bounds the chain so a pathological directory tree cannot
	// produce an unbounded report.
	maxMemoryFiles = 256
	// maxRuleFilesPerDir bounds a rules/ directory expansion.
	maxRuleFilesPerDir = 64
)

// disableMemoryEnv is Claude Code's kill switch. When it is set, no memory file
// is loaded at all.
const disableMemoryEnv = "CLAUDE_CODE_DISABLE_CLAUDE_MDS"

// MemoryLayer is a rung of Claude Code's memory hierarchy, in load order.
type MemoryLayer uint8

const (
	// LayerManaged is enterprise-managed policy instructions.
	LayerManaged MemoryLayer = iota
	// LayerUser is the user's own global instructions in the config directory.
	LayerUser
	// LayerAncestor is a directory above the project root.
	LayerAncestor
	// LayerProject is the session's working directory.
	LayerProject
	// LayerAdditional is a directory added to the session beyond the project.
	LayerAdditional
	// LayerAutoMem is the auto-memory file Claude Code maintains per project.
	LayerAutoMem
	// LayerImport is a file pulled in by an @-import from another memory file.
	LayerImport
)

var memoryLayerNames = map[MemoryLayer]string{
	LayerManaged:    "managed policy",
	LayerUser:       "user global",
	LayerAncestor:   "ancestor",
	LayerProject:    "project",
	LayerAdditional: "additional directory",
	LayerAutoMem:    "auto-memory",
	LayerImport:     "@-import",
}

// String returns the display name of the layer.
func (l MemoryLayer) String() string {
	if s, ok := memoryLayerNames[l]; ok {
		return s
	}
	return "project"
}

// Wrapper strings, reproduced exactly as Claude Code injects them. They are
// real bytes in the prompt, so they are counted; getting them wrong would
// under-report every memory file by its own header.
const (
	// memoryHeader precedes the whole chain, once.
	memoryHeader = "Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written."
	// wrapperUserGlobal wraps the user's global instructions.
	wrapperUserGlobal = "Contents of %s (user's private global instructions for all projects):"
	// wrapperProject wraps a project or ancestor instruction file.
	wrapperProject = "Contents of %s (project instructions, checked into the codebase):"
	// wrapperAutoMem wraps the auto-memory file.
	wrapperAutoMem = "Contents of %s (user's auto-memory, persists across conversations):"
)

// wrapperFor returns the injected header line for a file in a layer, and
// whether that exact wrapper has been observed in a real prompt. An unobserved
// wrapper still gets the closest observed form so its bytes are counted, and the
// item says the form is unconfirmed rather than presenting a guess as fact.
func wrapperFor(layer MemoryLayer, path string) (text string, observed bool) {
	switch layer {
	case LayerUser:
		return fmt.Sprintf(wrapperUserGlobal, path), true
	case LayerAutoMem:
		return fmt.Sprintf(wrapperAutoMem, path), true
	case LayerAncestor, LayerProject:
		return fmt.Sprintf(wrapperProject, path), true
	default:
		return fmt.Sprintf(wrapperProject, path), false
	}
}

// MemoryFile is one file in the reconstructed chain.
type MemoryFile struct {
	// Path is absolute. It is the path the discovery walk reached first, in
	// load order, which is the one Claude Code would name.
	Path string
	// ResolvedPath is Path with every symlink resolved. It is the identity a
	// file is deduplicated on: two chain positions that reach one inode are one
	// file with one cost, not two. Empty when resolution failed, in which case
	// Path itself was used as the identity.
	ResolvedPath string
	// Aliases are the other discovered paths that resolve to this same file.
	// They are reported, not counted: a reader who has one of the other paths
	// in mind needs to see that it is here, and needs to see that it was not
	// charged for twice.
	Aliases []string
	// Layer is the rung of the hierarchy it came from.
	Layer MemoryLayer
	// Order is the position in load order across the whole chain.
	Order int
	// Wrapper is the header line Claude Code injects above the content.
	Wrapper string
	// WrapperObserved reports whether that exact wrapper has been seen in a
	// real prompt for this layer.
	WrapperObserved bool
	// Content is the file text, bounded by [maxMemoryFileBytes].
	Content string
	// Chars is the character count of Content.
	Chars int
	// Size is the file size on disk.
	Size int64
	// Truncated reports that Content stops at the harness's per-file cap.
	Truncated bool
	// ReadErr is why a discovered file could not be read. Such a file still
	// appears in the report, priced from its size: dropping it would leave a
	// hole in the inventory the panel exists to provide.
	ReadErr string
	// ImportDepth is 0 for a directly discovered file and counts up through
	// @-imports.
	ImportDepth int
	// ImportedBy is the path of the file whose @-import pulled this one in.
	ImportedBy string
}

// MemoryChain is the reconstructed hierarchy.
type MemoryChain struct {
	// Files are in load order.
	Files []MemoryFile
	// Disabled reports that the kill switch suppressed the whole chain.
	Disabled bool
	// Excluded lists files that matched an exclude pattern, with the pattern.
	Excluded []ExcludedMemoryFile
	// Excludes are the patterns that were applied.
	Excludes []string
	// AdditionalDirs are the extra directories that were searched.
	AdditionalDirs []string
	// SettingsRead lists the settings files whose keys were honoured.
	SettingsRead []string
	// Warnings records discovery problems worth surfacing.
	Warnings []string
	// ConfigDir is the one Claude config directory every layer above was read
	// from. It is reported so a caller can say which directory the figures
	// describe, and so a mismatch with what the user expected is visible rather
	// than silently changing which files were priced.
	ConfigDir string
	// AutoMem is where the per-project auto-memory index was resolved to, and
	// how sure that is. It is populated even when the file does not exist, so
	// the "nothing found" case can still name where the search looked.
	AutoMem AutoMemory
}

// ExcludedMemoryFile is a file the exclude patterns kept out of the chain.
type ExcludedMemoryFile struct {
	// Path is absolute.
	Path string
	// Pattern is the claudeMdExcludes entry that matched.
	Pattern string
	// Size is the file size on disk, so the report can say what the exclusion
	// is saving rather than only that it happened.
	Size int64
}

// MemoryOptions configures [DiscoverMemory].
type MemoryOptions struct {
	// ConfigDir is the Claude config directory resolved for this specific
	// session. agent-deck's per-session scratch homes make the process-wide
	// default frequently wrong, so it must be passed in.
	ConfigDir string
	// ProjectPath is the session's working directory.
	ProjectPath string
	// TranscriptPath, when known, is a fallback source for ConfigDir only: a
	// transcript at <configDir>/projects/<slug>/<id>.jsonl names the directory
	// it lives under.
	//
	// It deliberately does not locate the auto-memory file. A transcript is
	// filed under the slug of the session's *working directory* while
	// auto-memory is filed under the slug of the session's *git repository
	// root*, so treating the transcript's own directory as the auto-memory
	// directory pointed a live session's row at a 130-byte stub instead of the
	// 17,506-byte index it loads. It is also resolved against whichever config
	// dir found it first, which is not necessarily the session's.
	TranscriptPath string
	// Env resolves environment variables. Nil uses [os.Getenv].
	Env func(string) string
	// Stat and ReadFile are injected so the walk is testable without a real
	// home directory. Nil uses the os package.
	Stat func(string) (os.FileInfo, error)
	// Resolve turns a discovered path into the identity it is deduplicated on.
	// Nil uses [filepath.EvalSymlinks]; a test injects it to exercise the
	// failure and cycle paths without building symlinks on disk.
	Resolve func(string) (string, error)
}

// DiscoverMemory reconstructs the CLAUDE.md chain Claude Code would load, in
// load order: managed policy, the user's global file and rules, every ancestor
// of the project from the filesystem root down to the working directory,
// additional directories, and finally the auto-memory file.
//
// AGENTS.md is deliberately absent. Claude Code does not read it, and listing
// it would invent context that is not in the window.
//
// Every candidate is identified by its path with symlinks resolved, so a file
// the walk reaches by two routes produces one row carrying both names. This is
// not hypothetical: a per-session config dir whose CLAUDE.md is a symlink into
// the profile, whose own CLAUDE.md is a symlink into ~/.claude, is reached
// three times on a single walk, and pricing it once per name overstated a real
// session's memory total by 8%.
//
// The result is [ctxinspect.TextReconstructed], never captured: verified across
// the eighty most recent transcripts on a working machine, the injected memory
// text appears in none of them, because it goes into the system prompt. The
// caller upgrades individual files to captured where a nested_memory record
// proves the exact bytes.
func DiscoverMemory(opts MemoryOptions) (*MemoryChain, error) {
	env := opts.Env
	if env == nil {
		env = os.Getenv
	}
	statFn := opts.Stat
	if statFn == nil {
		statFn = os.Stat
	}

	chain := &MemoryChain{}
	if isTruthy(env(disableMemoryEnv)) {
		chain.Disabled = true
		return chain, nil
	}

	// One resolution, used by every layer below. The auto-memory layer used to
	// derive its own directory from the transcript path, which is how it ended
	// up reading a different config dir from the one the rest of the chain was
	// read from; there is deliberately no second source here to drift from.
	configDir := resolveConfigDir(opts)
	chain.ConfigDir = configDir

	settings := readSettings(configDir, opts.ProjectPath)
	chain.Excludes = settings.Excludes
	chain.AdditionalDirs = settings.AdditionalDirs
	chain.SettingsRead = settings.Sources
	chain.Warnings = append(chain.Warnings, settings.Warnings...)

	w := &memoryWalk{
		chain:    chain,
		stat:     statFn,
		resolve:  opts.Resolve,
		excludes: settings.Excludes,
		seen:     make(map[string]int),
		excluded: make(map[string]bool),
	}
	if w.resolve == nil {
		w.resolve = filepath.EvalSymlinks
	}

	// 1. Managed policy instructions.
	for _, p := range managedPolicyPaths() {
		w.add(p, LayerManaged)
	}

	// 2. The user's own global instructions and rule files.
	if configDir != "" {
		w.add(filepath.Join(configDir, "CLAUDE.md"), LayerUser)
		w.addRules(filepath.Join(configDir, "rules"), LayerUser)
	}

	// 3. Every ancestor from the filesystem root down to the working directory.
	if project := strings.TrimSpace(opts.ProjectPath); project != "" {
		abs, err := filepath.Abs(project)
		if err != nil {
			return nil, fmt.Errorf("resolving the project path %q: %w", project, err)
		}
		for _, dir := range ancestorChain(abs) {
			layer := LayerAncestor
			if dir == abs {
				layer = LayerProject
			}
			w.add(filepath.Join(dir, "CLAUDE.md"), layer)
			w.add(filepath.Join(dir, ".claude", "CLAUDE.md"), layer)
			w.addRules(filepath.Join(dir, ".claude", "rules"), layer)
			w.add(filepath.Join(dir, "CLAUDE.local.md"), layer)
		}
	}

	// 4. Directories added to the session beyond the project.
	for _, dir := range settings.AdditionalDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		abs := expandHome(dir, env)
		w.add(filepath.Join(abs, "CLAUDE.md"), LayerAdditional)
		w.add(filepath.Join(abs, ".claude", "CLAUDE.md"), LayerAdditional)
	}

	// 5. The auto-memory file Claude Code maintains for this project, located by
	// the harness's own rule: the slug of the session's canonical git root under
	// the config dir resolved above, never the transcript's directory.
	chain.AutoMem = resolveAutoMemory(configDir, opts.ProjectPath, settings, env, statFn)
	if p := chain.AutoMem.Path; p != "" && !chain.AutoMem.Disabled {
		w.add(p, LayerAutoMem)
	}

	// 6. @-imports, breadth-first, to the harness's depth limit.
	w.expandImports(env)

	for i := range chain.Files {
		chain.Files[i].Order = i
	}
	return chain, nil
}

// memoryWalk carries the state of one discovery pass.
type memoryWalk struct {
	chain    *MemoryChain
	stat     func(string) (os.FileInfo, error)
	resolve  func(string) (string, error)
	excludes []string
	// seen maps a file's resolved identity to its index in chain.Files, so a
	// second path reaching the same file can be recorded as an alias of the row
	// that already exists instead of adding a second one.
	seen map[string]int
	// excluded holds the identities already reported as excluded. Without it an
	// excluded file reached by two routes is listed as two exclusions, which
	// double-counts the saving the exclusion is credited with.
	excluded map[string]bool
}

// identity returns the key a discovered path is deduplicated on: the path with
// every symlink resolved.
//
// Resolution can fail for reasons that are not the caller's fault — a symlink
// cycle (ELOOP), a permission-denied component, a race with a deletion. In
// every such case the cleaned literal path is used instead. That degrades
// dedupe to the old string comparison for that one path rather than dropping
// the file or crashing the panel, and the second return says which happened so
// a caller can tell an alias-collapse from a fallback.
func (w *memoryWalk) identity(path string) (key string, resolved string) {
	clean := filepath.Clean(path)
	if w.resolve == nil {
		return clean, ""
	}
	real, err := w.resolve(clean)
	if err != nil || strings.TrimSpace(real) == "" {
		return clean, ""
	}
	return filepath.Clean(real), filepath.Clean(real)
}

// add records one candidate path if it exists, is not already in the chain, and
// is not excluded.
//
// It goes through addImported rather than duplicating the checks: a second
// entry point with its own copy of the seen-check is exactly how one file comes
// to be counted twice.
func (w *memoryWalk) add(path string, layer MemoryLayer) {
	w.addImported(path, layer, 0, "")
}

// addImported records a candidate, tracking @-import provenance.
func (w *memoryWalk) addImported(path string, layer MemoryLayer, depth int, importedBy string) bool {
	if len(w.chain.Files) >= maxMemoryFiles {
		return false
	}
	path = filepath.Clean(path)
	key, resolved := w.identity(path)
	if idx, ok := w.seen[key]; ok {
		// The same file, reached by a second route. Claude Code loads its bytes
		// once, so the report charges for it once and says where else it lives.
		// Counting it again is the difference between a 26.8k memory total and
		// a 28.9k one, and only one of those is true.
		w.noteAlias(idx, path)
		return false
	}
	info, err := w.stat(path)
	if err != nil || info.IsDir() {
		return false
	}

	if pattern, ok := matchExclude(path, w.excludes); ok {
		// Keyed on the resolved identity, so the same file arriving by an alias
		// later is not listed as a second exclusion.
		if !w.excluded[key] {
			w.excluded[key] = true
			w.chain.Excluded = append(w.chain.Excluded, ExcludedMemoryFile{Path: path, Pattern: pattern, Size: info.Size()})
		}
		return false
	}

	wrapper, observed := wrapperFor(layer, path)
	f := MemoryFile{
		Path:            path,
		ResolvedPath:    resolved,
		Layer:           layer,
		Wrapper:         wrapper,
		WrapperObserved: observed,
		Size:            info.Size(),
		ImportDepth:     depth,
		ImportedBy:      importedBy,
	}
	content, truncated, rerr := readBoundedFile(path, maxMemoryFileBytes)
	if rerr != nil {
		f.ReadErr = rerr.Error()
	} else {
		f.Content = content
		f.Chars = len([]rune(content))
		f.Truncated = truncated
	}
	w.seen[key] = len(w.chain.Files)
	w.chain.Files = append(w.chain.Files, f)
	return true
}

// noteAlias records that path is another name for the file already at idx.
// Duplicate and self-referential aliases are dropped so a chain that reaches
// one file by three routes lists two extra names, not three.
func (w *memoryWalk) noteAlias(idx int, path string) {
	if idx < 0 || idx >= len(w.chain.Files) {
		return
	}
	f := &w.chain.Files[idx]
	if path == f.Path {
		return
	}
	for _, existing := range f.Aliases {
		if existing == path {
			return
		}
	}
	f.Aliases = append(f.Aliases, path)
}

// addRules expands a rules directory. Only .md files count, the listing is
// sorted so the report is stable, and the expansion is bounded.
func (w *memoryWalk) addRules(dir string, layer MemoryLayer) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) > maxRuleFilesPerDir {
		w.chain.Warnings = append(w.chain.Warnings,
			fmt.Sprintf("%s holds %d rule files; only the first %d are reported", dir, len(names), maxRuleFilesPerDir))
		names = names[:maxRuleFilesPerDir]
	}
	for _, name := range names {
		w.add(filepath.Join(dir, name), layer)
	}
}

// expandImports follows @-imports breadth-first to [maxImportDepth].
//
// A candidate is accepted only when the resolved path exists on disk. Claude
// Code's import syntax overlaps with ordinary prose — an npm scope such as
// @anthropic-ai/sdk is indistinguishable by pattern alone — so requiring the
// file to exist is what keeps the reconstruction from inventing imports.
func (w *memoryWalk) expandImports(env func(string) string) {
	for depth := 0; depth < maxImportDepth; depth++ {
		var frontier []MemoryFile
		for _, f := range w.chain.Files {
			if f.ImportDepth == depth && f.Content != "" {
				frontier = append(frontier, f)
			}
		}
		if len(frontier) == 0 {
			return
		}
		for _, f := range frontier {
			base := filepath.Dir(f.Path)
			for _, ref := range importRefs(f.Content) {
				target := expandHome(ref, env)
				if !filepath.IsAbs(target) {
					target = filepath.Join(base, target)
				}
				w.addImported(target, LayerImport, depth+1, f.Path)
			}
		}
	}
}

// importPattern matches an @-import reference: an @ at a line start or after
// whitespace, followed by a path-like token.
var importPattern = regexp.MustCompile(`(?m)(?:^|[\s(])@([~./][^\s)\]"']*|[A-Za-z0-9_.-]+/[^\s)\]"']*)`)

// importRefs extracts @-import candidates from memory content, skipping fenced
// code blocks so an example in documentation is not read as an instruction.
func importRefs(content string) []string {
	var out []string
	seen := make(map[string]bool)
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range importPattern.FindAllStringSubmatch(line, -1) {
			ref := strings.TrimRight(m[1], ".,;:")
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// claudeSettings is the subset of Claude Code settings the memory walk honours.
type claudeSettings struct {
	Excludes       []string `json:"claudeMdExcludes"`
	AdditionalDirs []string `json:"additionalDirectories"`
	// AutoMemEnabled is a pointer so an absent key is distinguishable from an
	// explicit false: the default is on, and only an explicit false suppresses
	// the layer.
	AutoMemEnabled *bool  `json:"autoMemoryEnabled"`
	AutoMemDir     string `json:"autoMemoryDirectory"`
}

// mergedSettings is the union of every settings file that was readable.
type mergedSettings struct {
	Excludes       []string
	AdditionalDirs []string
	Sources        []string
	Warnings       []string
	// AutoMemEnabled and AutoMemDir are single-valued, so unlike the list keys
	// they are overwritten in precedence order rather than appended.
	AutoMemEnabled *bool
	AutoMemDir     string
	// AutoMemDirSource names the settings file that set AutoMemDir, so a lever
	// can say where to change it.
	AutoMemDirSource string
}

// readSettings merges the settings files that can influence the chain, in
// Claude Code's precedence order (user, then project, then project-local).
//
// The single-valued auto-memory keys are overwritten as the walk goes, so the
// last file to set one wins — which is the order the harness resolves them in.
// Two sources it consults are invisible from here: enterprise-managed policy
// settings and flags passed on the session's own command line. An override set
// only in one of those is not reflected, and the auto-memory row says its
// location came from the rule rather than claiming to have seen every override.
func readSettings(configDir, projectPath string) mergedSettings {
	var out mergedSettings
	candidates := []string{}
	if d := strings.TrimSpace(configDir); d != "" {
		candidates = append(candidates, filepath.Join(d, "settings.json"))
	}
	if p := strings.TrimSpace(projectPath); p != "" {
		candidates = append(candidates,
			filepath.Join(p, ".claude", "settings.json"),
			filepath.Join(p, ".claude", "settings.local.json"),
		)
	}
	for _, path := range candidates {
		raw, err := readBoundedRaw(path, 1<<20)
		if err != nil {
			continue
		}
		var s claudeSettings
		if err := json.Unmarshal(raw, &s); err != nil {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("%s could not be parsed (%v), so any claudeMdExcludes or additionalDirectories it sets were not applied", path, err))
			continue
		}
		out.Sources = append(out.Sources, path)
		out.Excludes = append(out.Excludes, s.Excludes...)
		out.AdditionalDirs = append(out.AdditionalDirs, s.AdditionalDirs...)
		if s.AutoMemEnabled != nil {
			out.AutoMemEnabled = s.AutoMemEnabled
		}
		if v := strings.TrimSpace(s.AutoMemDir); v != "" {
			out.AutoMemDir = v
			out.AutoMemDirSource = path
		}
	}
	return out
}

// matchExclude reports whether a path matches any claudeMdExcludes pattern.
// Both the full path and the base name are tested, which is how a pattern like
// "CLAUDE.local.md" and one like "/opt/**" both do what a user expects.
func matchExclude(path string, patterns []string) (string, bool) {
	base := filepath.Base(path)
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ok, err := filepath.Match(p, path); err == nil && ok {
			return p, true
		}
		if ok, err := filepath.Match(p, base); err == nil && ok {
			return p, true
		}
		// A trailing-glob directory pattern is the common way to exclude a
		// whole tree; filepath.Match does not cross separators, so prefix-match
		// it explicitly.
		if trimmed := strings.TrimSuffix(strings.TrimSuffix(p, "*"), "/"); trimmed != "" && trimmed != p {
			if strings.HasPrefix(path, trimmed) {
				return p, true
			}
		}
	}
	return "", false
}

// managedPolicyPaths returns the platform locations of enterprise-managed
// instructions. Both are checked on every platform: the cost is one stat each,
// and a wrong platform guess would silently drop a real contributor.
func managedPolicyPaths() []string {
	return []string{
		filepath.Join("/Library", "Application Support", "ClaudeCode", "CLAUDE.md"),
		filepath.Join("/etc", "claude-code", "CLAUDE.md"),
	}
}

// resolveConfigDir returns the single Claude config directory every layer of the
// chain is read from.
//
// The caller's ConfigDir is authoritative: agent-deck resolves it per session,
// and a per-session worker-scratch home is a different directory from the
// profile that seeded it. When it is absent, a known transcript path names the
// directory it lives under — <configDir>/projects/<slug>/<id>.jsonl — which is
// better than falling back to a process-wide default that belongs to whatever
// shell agent-deck itself was started from.
//
// Every layer calls this. That is the point: the auto-memory row was wrong for
// exactly as long as one layer resolved its directory some other way.
func resolveConfigDir(opts MemoryOptions) string {
	if dir := strings.TrimSpace(opts.ConfigDir); dir != "" {
		return filepath.Clean(dir)
	}
	return configDirFromTranscript(opts.TranscriptPath)
}

// configDirFromTranscript walks a transcript path back to the config directory
// that contains it, by finding the projects/ component it sits under. It returns
// empty when the path has no such component, rather than guessing.
func configDirFromTranscript(transcript string) string {
	t := strings.TrimSpace(transcript)
	if t == "" {
		return ""
	}
	dir := filepath.Dir(filepath.Clean(t))
	for dir != filepath.Dir(dir) {
		if filepath.Base(dir) == "projects" {
			return filepath.Dir(dir)
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

// ancestorChain returns every directory from the filesystem root down to and
// including dir — the order Claude Code concatenates instruction files in.
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

// expandHome resolves a leading ~ against the environment's HOME.
func expandHome(path string, env func(string) string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home := env("HOME"); home != "" {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
		}
	}
	return path
}

// isTruthy interprets an environment flag the way shell tooling does.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// readBoundedFile reads at most maxBytes of a file and reports whether it was
// longer, reading one byte past the cap so "exactly at the cap" is not
// mistaken for "truncated".
func readBoundedFile(path string, maxBytes int64) (content string, truncated bool, err error) {
	raw, truncated, err := readBoundedBytes(path, maxBytes)
	return string(raw), truncated, err
}

// readBoundedRaw reads at most maxBytes of a file, discarding the truncation
// flag; it is used for settings files, where a truncated JSON document simply
// fails to parse and is reported as such.
func readBoundedRaw(path string, maxBytes int64) ([]byte, error) {
	raw, _, err := readBoundedBytes(path, maxBytes)
	return raw, err
}

// readBoundedBytes is the single bounded read used by this package.
func readBoundedBytes(path string, maxBytes int64) (raw []byte, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()

	buf, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > maxBytes {
		return buf[:maxBytes], true, nil
	}
	return buf, false, nil
}
