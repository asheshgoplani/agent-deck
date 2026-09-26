package session

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/git"
)

// Directory-local configuration overrides (#2093).
//
// A `.agent-deck/config.toml` found in the target directory or one of its
// ancestors can override a small, explicitly allowlisted set of [worktree]
// settings — default_location, path_template, sparse_checkout — for sessions
// created within that directory tree. This supports a "workspace parent"
// layout where sibling git checkouts share one directory-local config file
// that is not itself inside any of the checkouts' git roots.
//
// Safety: dir-local config comes from a checkout that may not be trusted the
// way ~/.agent-deck/config.toml is, so it is deliberately restricted to these
// three declarative keys. Any other key or section is refused with an error
// naming the file and the offending key(s) (fail closed) rather than
// silently ignored.
//
// default_location and path_template get a second layer of scrutiny (P1
// review follow-up, #2093, round 3): unlike sparse_checkout, both are used to
// build a filesystem path that GenerateWorktreePath/resolveTemplate will
// write a worktree to, with no containment check of their own (see
// internal/git/git.go and internal/git/template.go). So a dir-local value for
// either key is bounded to the directory containing the file it was actually
// READ FROM — a dir-local file may only target its own subtree. A
// workspace-parent file (one that sits ABOVE the repos it configures, with no
// dir-local file of its own above it) is, by construction, its own boundary
// and so still gets the wider "workspace" bound for values it contributes
// itself (e.g. path_template = "{repo-root}/../wt-{branch}" set in that
// file) — but an INNER file nested below it (e.g. inside one of the sibling
// repos) is bounded to its own directory tree only, and cannot use a
// path_template/default_location to point outside it, even into a sibling
// repo the outer file's boundary would otherwise permit. Global config and
// explicit CLI flags are unaffected and remain fully trusted, as before. See
// validateDirLocalWorktreeValue.

// dirLocalWorktreeConfig is the allowlisted [worktree] surface for dir-local
// config files. Pointer fields distinguish "not set" from "explicitly
// cleared" (e.g. path_template = "" clears an inherited template from a
// further-out directory).
type dirLocalWorktreeConfig struct {
	DefaultLocation *string `toml:"default_location"`
	PathTemplate    *string `toml:"path_template"`
	SparseCheckout  *string `toml:"sparse_checkout"`
}

// dirLocalConfig is the entire allowlisted schema for a dir-local
// .agent-deck/config.toml. Only the [worktree] section (and only the three
// fields above) is supported; any other top-level section or unknown key
// under [worktree] is rejected. The struct itself is the allowlist: the
// BurntSushi toml decoder's Undecoded() metadata reports any TOML key that
// has no matching struct field, so no separate key-matching switch is
// needed.
type dirLocalConfig struct {
	Worktree dirLocalWorktreeConfig `toml:"worktree"`
}

// dirLocalConfigRelPath is the dir-local config file's path relative to a
// candidate directory.
const dirLocalConfigRelPath = ".agent-deck/config.toml"

// loadDirLocalConfig strictly decodes a single dir-local config file,
// rejecting any key or section not in the allowlist above.
func loadDirLocalConfig(path string) (*dirLocalConfig, error) {
	var cfg dirLocalConfig
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		slices.Sort(keys)
		return nil, fmt.Errorf(
			"%s: unknown key(s) not allowed in directory-local config: %s",
			path, strings.Join(keys, ", "))
	}

	return &cfg, nil
}

// DiscoverDirLocalConfigPaths walks from targetDir upward through every
// ancestor directory looking for a regular file at "<dir>/.agent-deck/config.toml".
//
// The walk is inclusive of the user's home directory but goes no further: for
// a targetDir under $HOME, discovery stops at $HOME. For a targetDir outside
// $HOME, discovery stops at the filesystem root instead.
//
// The literal legacy global config path ($HOME/.agent-deck/config.toml) is
// excluded from the result — it is already applied as "global" config, and
// must never be double-counted as dir-local too. Any other file found during
// the walk, including a distinct file located AT $HOME under XDG mode, counts
// as dir-local.
//
// Results are ordered outermost-first (furthest ancestor first, targetDir's
// own file last); this ordering is also precedence order low-to-high.
func DiscoverDirLocalConfigPaths(targetDir string) ([]string, error) {
	abs, err := filepath.Abs(targetDir)
	if err != nil {
		return nil, fmt.Errorf("resolve target directory %q: %w", targetDir, err)
	}
	// Every path compared below is symlink-resolved the same way, so a
	// symlinked $HOME (e.g. macOS's /tmp -> /private/tmp) cannot defeat the
	// home boundary or the global-config exclusion.
	resolved := resolveSymlinks(abs)

	// The global config is already applied as "global" config and must never be
	// double-counted as dir-local. Exclude the legacy $HOME/.agent-deck/config.toml
	// and — defensively, in case XDG-first resolution ever lands exactly on a
	// candidate — whatever GetUserConfigPath() resolves to today.
	//
	// Compared by file identity (os.SameFile), not by path string: a global
	// config that is itself a symlink, or sits in a symlinked ~/.agent-deck
	// (a dotfiles checkout, #2367), has a different resolved path than the
	// candidate built from the resolved directory walk below.
	var globalFiles []os.FileInfo
	addGlobal := func(path string) {
		if info, err := os.Stat(path); err == nil {
			globalFiles = append(globalFiles, info)
		}
	}
	if path, err := GetUserConfigPath(); err == nil {
		addGlobal(path)
	}
	if legacyDir, err := agentpaths.LegacyDir(); err == nil {
		addGlobal(filepath.Join(legacyDir, UserConfigFileName))
	}
	isGlobal := func(info os.FileInfo) bool {
		return slices.ContainsFunc(globalFiles, func(g os.FileInfo) bool { return os.SameFile(g, info) })
	}

	var home string
	if h, err := os.UserHomeDir(); err == nil {
		home = resolveSymlinks(h)
	}
	stopAtHome := home != "" && isWithinDir(home, resolved)

	var found []string
	for dir := resolved; ; {
		candidate := filepath.Join(dir, filepath.FromSlash(dirLocalConfigRelPath))
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && !isGlobal(info) {
			found = append(found, candidate)
		}

		if stopAtHome && dir == home {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // filesystem root
		}
		dir = parent
	}

	// found was collected innermost-first; reverse to outermost-first.
	slices.Reverse(found)
	return found, nil
}

// resolveSymlinks returns path with symlinks resolved, or path unchanged when
// it cannot be resolved (e.g. it does not exist yet).
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// isWithinDir reports whether path is base itself or a descendant of it. Both
// are expected to be absolute and resolved by resolveSymlinks.
func isWithinDir(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// The three dir-local-eligible [worktree] keys. They are also the keys of the
// "sources" map returned by ResolveWorktreeSettingsForDir, so callers
// reporting on those settings share one spelling of each name.
const (
	WorktreeKeyDefaultLocation = "default_location"
	WorktreeKeyPathTemplate    = "path_template"
	WorktreeKeySparseCheckout  = "sparse_checkout"
)

const (
	sourceDefault = "default"
	sourceGlobal  = "global"
)

// ResolveWorktreeSettingsForDir merges built-in defaults, the global user
// config, and any directory-local .agent-deck/config.toml files discovered
// by walking up from targetDir (outermost to innermost; see
// DiscoverDirLocalConfigPaths), and reports which file supplied each of the
// three dir-local-eligible [worktree] keys, plus a human-readable rejection
// description for each dir-local default_location/path_template value that
// failed the boundary check in validateDirLocalWorktreeValue (keyed by
// WorktreeKeyDefaultLocation/WorktreeKeyPathTemplate; a key with no
// rejections is absent from the map, never present with an empty slice).
//
// Precedence, lowest to highest: built-in defaults < global config < outer
// dir-local file < inner dir-local file. Explicit CLI/session overrides are
// the caller's responsibility to apply on top of the returned settings.
func ResolveWorktreeSettingsForDir(targetDir string) (WorktreeSettings, map[string]string, map[string][]string, error) {
	settings := GetWorktreeSettings()

	sources := map[string]string{
		WorktreeKeyDefaultLocation: sourceDefault,
		WorktreeKeyPathTemplate:    sourceDefault,
		WorktreeKeySparseCheckout:  sourceDefault,
	}
	rejections := map[string][]string{}

	// Consult the RAW global config (pre-default-application) so "default"
	// vs. "global" is reported correctly.
	if globalCfg, err := LoadUserConfig(); err == nil && globalCfg != nil {
		if globalCfg.Worktree.DefaultLocation != "" {
			sources[WorktreeKeyDefaultLocation] = sourceGlobal
		}
		if globalCfg.Worktree.PathTemplate != nil {
			sources[WorktreeKeyPathTemplate] = sourceGlobal
		}
		if globalCfg.Worktree.SparseCheckout != "" {
			sources[WorktreeKeySparseCheckout] = sourceGlobal
		}
	}

	paths, err := DiscoverDirLocalConfigPaths(targetDir)
	if err != nil {
		return settings, sources, rejections, err
	}

	// dirBoundary returns the boundary an untrusted default_location/
	// path_template value read from path must resolve within: the directory
	// containing path itself (stripping the file name and ".agent-deck").
	// This bounds every file to its OWN subtree — an inner file nested below
	// an outer workspace-parent file cannot borrow the outer file's wider
	// boundary. The outermost file's own directory IS the workspace boundary
	// by construction, so it (and only it) still gets the workspace-wide
	// bound for values it contributes itself.
	dirBoundary := func(path string) string {
		return resolveSymlinks(filepath.Dir(filepath.Dir(path)))
	}

	// accept reports whether an untrusted dir-local value for key, read from
	// path, may be applied. On acceptance it credits path as the key's new
	// source; on rejection it records the reason and the source the key falls
	// back to (which is still the pre-existing, lower-precedence one).
	accept := func(key, raw, path string) bool {
		reason := validateDirLocalWorktreeValue(key, raw, targetDir, dirBoundary(path))
		if reason != "" {
			rejections[key] = append(rejections[key], fmt.Sprintf(
				"%s: %s %q rejected (%s); falling back to %s value",
				path, key, raw, reason, sources[key]))
			return false
		}
		sources[key] = path
		return true
	}

	for _, path := range paths {
		local, err := loadDirLocalConfig(path)
		if err != nil {
			return settings, sources, rejections, err
		}

		if local.Worktree.DefaultLocation != nil {
			if raw := *local.Worktree.DefaultLocation; accept(WorktreeKeyDefaultLocation, raw, path) {
				settings.DefaultLocation = raw
			}
		}
		if local.Worktree.PathTemplate != nil {
			// raw is a fresh variable per iteration, so taking its address
			// never aliases back into the decoded dir-local struct.
			if raw := *local.Worktree.PathTemplate; accept(WorktreeKeyPathTemplate, raw, path) {
				settings.PathTemplate = &raw
			}
		}
		if local.Worktree.SparseCheckout != nil {
			settings.SparseCheckout = *local.Worktree.SparseCheckout
			sources[WorktreeKeySparseCheckout] = path
		}
	}

	return settings, sources, rejections, nil
}

// resolveSymlinksBestEffort resolves symlinks for path even when path itself
// does not exist yet — the common case for a candidate worktree path, which
// by definition is usually a directory that has not been created. Unlike
// resolveSymlinks (which calls filepath.EvalSymlinks on the whole path and
// gives up unchanged the moment ANY component, including a not-yet-created
// leaf, is missing), this walks up to the longest existing ancestor,
// resolves THAT with resolveSymlinks, and rejoins the missing suffix
// literally. This is what makes a symlink planted inside the workspace (e.g.
// a dir-local file's sibling symlink pointing outside it) catchable even
// though the final worktree path it leads to has never been created.
func resolveSymlinksBestEffort(path string) string {
	dir := path
	var missing []string
	for {
		if _, err := os.Lstat(dir); err == nil {
			resolved := resolveSymlinks(dir)
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path // no existing ancestor found at all; nothing to resolve
		}
		missing = append(missing, filepath.Base(dir))
		dir = parent
	}
}

// validateDirLocalWorktreeValue checks a dir-local-sourced default_location
// or path_template value before it is allowed into WorktreeSettings, and
// returns a non-empty rejection reason if it must be refused. It returns ""
// for a value that passes (global config and CLI flags never call this — they
// remain fully trusted).
//
// Two layers, per the P1 review follow-up (#2093):
//  1. Raw-string checks, before any template/variable expansion: reject an
//     absolute path, a "~"-prefixed path, and (default_location only, since
//     it is used verbatim and never templated) a literal ".." path segment.
//     path_template is exempt from the literal ".." check because the
//     legitimate workspace-parent case ("{repo-root}/../wt-{branch}") relies
//     on one; its escapes are instead caught by the resolved-path check below.
//  2. A resolved-path bound check, simulating resolution with targetDir as
//     the repo root (see the call sites this mirrors: every real caller
//     passes the SAME directory to GetWorktreeSettingsForDir/
//     ResolveWorktreeSettingsForDir and to WorktreePath/GenerateWorktreePath
//     as RepoDir). This catches template ".." escapes, symlink escapes, and
//     anything the raw check misses.
//
// boundary is the directory containing the specific file raw was read from
// (see dirBoundary in ResolveWorktreeSettingsForDir), never a workspace-wide
// bound shared across files — an inner dir-local file may only target its
// own subtree, even when an outer workspace-parent file exists above it.
func validateDirLocalWorktreeValue(key, raw, targetDir, boundary string) string {
	if filepath.IsAbs(raw) {
		return "absolute path not allowed"
	}
	if strings.HasPrefix(raw, "~") {
		return "home-relative (~) path not allowed"
	}
	if key == WorktreeKeyDefaultLocation && slices.Contains(strings.Split(raw, "/"), "..") {
		return "'..' path segment not allowed"
	}

	if boundary == "" {
		// No boundary to bound against; unreachable in practice (the only
		// caller derives boundary from the path of the file that supplied
		// raw, which is always non-empty), but fail safe.
		return "no workspace boundary available"
	}

	var candidate string
	switch key {
	case WorktreeKeyDefaultLocation:
		// GenerateWorktreePath only treats default_location as a custom path
		// (leaving targetDir's vicinity) when it contains "/" or starts with
		// "~"; "sibling"/"subdirectory"/"" never escape targetDir, so skip
		// the bound check for those — they're inherently safe.
		if !strings.Contains(raw, "/") && !strings.HasPrefix(raw, "~") {
			return ""
		}
		candidate = git.GenerateWorktreePath(targetDir, "boundary-check", raw)
	case WorktreeKeyPathTemplate:
		// An empty template clears any inherited template and restores the
		// built-in default (sibling) strategy — the same "inherently safe,
		// no bound check" treatment default_location's own empty/sibling/
		// subdirectory values get above. Without this, the default sibling
		// candidate (which sits beside targetDir by design) gets rejected by
		// the bound check whenever the boundary has collapsed to targetDir
		// itself (the ordinary single-file, no-outer-file case).
		if raw == "" {
			return ""
		}
		candidate = git.WorktreePath(git.WorktreePathOptions{
			Branch:    "boundary-check",
			RepoDir:   targetDir,
			SessionID: "boundary-check",
			Template:  raw,
		})
	default:
		// Only the two keys above are validated; nothing else reaches here.
		return ""
	}

	resolvedCandidate := resolveSymlinksBestEffort(candidate)
	if !isWithinDir(boundary, resolvedCandidate) {
		return fmt.Sprintf("resolves outside workspace boundary %s: %s", boundary, resolvedCandidate)
	}
	return ""
}

// GetWorktreeSettingsForDir returns the merged worktree settings (built-in
// defaults, global config, and any directory-local overrides) for targetDir.
//
// Unlike GetWorktreeSettings, this can fail: a dir-local config file may be
// present but invalid (unknown key/section), which per #2093's safety
// requirements must be refused rather than silently ignored. Callers
// creating a new session/worktree should treat a non-nil error as fatal to
// the operation (fail closed) and surface it to the user, rather than
// falling back to global/default settings.
//
// A rejected default_location/path_template value (see
// validateDirLocalWorktreeValue) is NOT an error here: unlike an unknown key,
// it falls back silently to the next-highest-precedence source. Callers that
// need to surface rejections to the user (e.g. `agent-deck config show
// --effective`) should call ResolveWorktreeSettingsForDir directly.
func GetWorktreeSettingsForDir(targetDir string) (WorktreeSettings, error) {
	settings, _, _, err := ResolveWorktreeSettingsForDir(targetDir)
	return settings, err
}
