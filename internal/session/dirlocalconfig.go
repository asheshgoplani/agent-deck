package session

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
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
	var globalPaths []string
	if path, err := GetUserConfigPath(); err == nil {
		globalPaths = append(globalPaths, resolveSymlinks(path))
	}
	if legacyDir, err := agentpaths.LegacyDir(); err == nil {
		globalPaths = append(globalPaths, resolveSymlinks(filepath.Join(legacyDir, UserConfigFileName)))
	}

	var home string
	if h, err := os.UserHomeDir(); err == nil {
		home = resolveSymlinks(h)
	}
	stopAtHome := home != "" && isWithinDir(home, resolved)

	var found []string
	for dir := resolved; ; {
		candidate := filepath.Join(dir, filepath.FromSlash(dirLocalConfigRelPath))
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && !slices.Contains(globalPaths, candidate) {
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
// three dir-local-eligible [worktree] keys.
//
// Precedence, lowest to highest: built-in defaults < global config < outer
// dir-local file < inner dir-local file. Explicit CLI/session overrides are
// the caller's responsibility to apply on top of the returned settings.
func ResolveWorktreeSettingsForDir(targetDir string) (WorktreeSettings, map[string]string, error) {
	settings := GetWorktreeSettings()

	sources := map[string]string{
		WorktreeKeyDefaultLocation: sourceDefault,
		WorktreeKeyPathTemplate:    sourceDefault,
		WorktreeKeySparseCheckout:  sourceDefault,
	}

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
		return settings, sources, err
	}

	for _, path := range paths {
		local, err := loadDirLocalConfig(path)
		if err != nil {
			return settings, sources, err
		}

		if local.Worktree.DefaultLocation != nil {
			settings.DefaultLocation = *local.Worktree.DefaultLocation
			sources[WorktreeKeyDefaultLocation] = path
		}
		if local.Worktree.PathTemplate != nil {
			// Copy the pointer's pointee so callers mutating settings.PathTemplate
			// never alias back into the decoded dir-local struct.
			v := *local.Worktree.PathTemplate
			settings.PathTemplate = &v
			sources[WorktreeKeyPathTemplate] = path
		}
		if local.Worktree.SparseCheckout != nil {
			settings.SparseCheckout = *local.Worktree.SparseCheckout
			sources[WorktreeKeySparseCheckout] = path
		}
	}

	return settings, sources, nil
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
func GetWorktreeSettingsForDir(targetDir string) (WorktreeSettings, error) {
	settings, _, err := ResolveWorktreeSettingsForDir(targetDir)
	return settings, err
}
