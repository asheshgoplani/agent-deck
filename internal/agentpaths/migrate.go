package agentpaths

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type PathCategory string

const (
	CategoryConfig PathCategory = "config"
	CategoryData   PathCategory = "data"
	CategoryCache  PathCategory = "cache"
)

var ErrMigrationConflict = errors.New("migration destination conflict")

// errDestinationExists is returned by the copy primitives when a destination
// leaf already exists at the moment of the atomic (O_EXCL) create. It signals a
// TOCTOU race: a concurrent Agent Deck process created the destination between
// the caller's existence check and the copy. Callers MUST treat it as a
// conflict (preserve the existing file, never truncate), never as a hard error.
var errDestinationExists = errors.New("migration destination already exists")

type MigrationOptions struct {
	DryRun bool
	Force  bool
}

type MigrationItem struct {
	Name        string
	Category    PathCategory
	Source      string
	Destination string
	Directory   bool
}

type MigrationResult struct {
	DryRun    bool
	Copied    []MigrationItem
	Skipped   []MigrationItem
	Conflicts []MigrationItem
}

func MigrateLegacyLayout(opts MigrationOptions) (*MigrationResult, error) {
	result := &MigrationResult{DryRun: opts.DryRun}
	items, err := migrationItems()
	if err != nil {
		return result, err
	}

	var ready []MigrationItem
	for _, item := range items {
		if exists, err := pathExists(item.Destination); err != nil {
			return result, err
		} else if exists && !opts.Force {
			result.Conflicts = append(result.Conflicts, item)
			continue
		}
		ready = append(ready, item)
	}
	if len(result.Conflicts) > 0 {
		return result, ErrMigrationConflict
	}

	for _, item := range ready {
		if opts.DryRun {
			result.Copied = append(result.Copied, item)
			continue
		}
		if opts.Force {
			// Data-safety (Blocker 2): merge legacy into the destination
			// per-file. NEVER os.RemoveAll the destination tree — that would
			// destroy newer XDG-only data. Per-file conflicts preserve the
			// existing (newer) XDG file and are reported as conflicts.
			conflicted, err := mergeMigrationPath(item.Source, item.Destination)
			if err != nil {
				return result, err
			}
			if conflicted {
				result.Conflicts = append(result.Conflicts, item)
			}
			result.Copied = append(result.Copied, item)
			continue
		}
		if err := copyMigrationPath(item.Source, item.Destination); err != nil {
			// A concurrent Agent Deck process created the destination (or a leaf
			// within it) after our pre-flight existence check. Preserve it and
			// report a conflict instead of truncating (Blocker 1 TOCTOU fix).
			if errors.Is(err, errDestinationExists) {
				result.Conflicts = append(result.Conflicts, item)
				return result, ErrMigrationConflict
			}
			return result, err
		}
		result.Copied = append(result.Copied, item)
	}

	return result, nil
}

func migrationItems() ([]MigrationItem, error) {
	legacyDir, err := LegacyDir()
	if err != nil {
		return nil, err
	}
	configDir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	dataDir, err := DataDir()
	if err != nil {
		return nil, err
	}
	cacheDir, err := CacheDir()
	if err != nil {
		return nil, err
	}

	var items []MigrationItem
	seen := make(map[string]struct{})
	add := func(category PathCategory, root, name string) error {
		source := filepath.Join(legacyDir, name)
		info, err := os.Lstat(source)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("stat legacy item %q: %w", source, err)
		}
		destination := filepath.Join(root, name)
		key := string(category) + "\x00" + name
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		items = append(items, MigrationItem{
			Name:        name,
			Category:    category,
			Source:      source,
			Destination: destination,
			Directory:   info.IsDir(),
		})
		return nil
	}
	addGlob := func(category PathCategory, root, pattern string) error {
		matches, err := filepath.Glob(filepath.Join(legacyDir, pattern))
		if err != nil {
			return err
		}
		for _, match := range matches {
			if err := add(category, root, filepath.Base(match)); err != nil {
				return err
			}
		}
		return nil
	}

	for _, name := range []string{"config.toml", "config.json", "skills"} {
		if err := add(CategoryConfig, configDir, name); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{
		"profiles",
		"sessions.json",
		"hooks",
		"locks",
		"cost-events",
		"events",
		"runtime",
		"inboxes",
		"conductor",
		"watcher",
		"watchers",
		"triage",
		"logs",
		"feedback-state.json",
		"ack-signal",
		".ack-signal-legacy",
		"badge-updates",
		"worker-scratch",
	} {
		if err := add(CategoryData, dataDir, name); err != nil {
			return nil, err
		}
	}
	if err := addGlob(CategoryData, dataDir, "sessions.json*"); err != nil {
		return nil, err
	}
	for _, name := range []string{"update-cache.json", "pricing.json", "debug.log", "cost-debug.log"} {
		if err := add(CategoryCache, cacheDir, name); err != nil {
			return nil, err
		}
	}
	for _, pattern := range []string{"debug-dump-*.jsonl", "crash-dump-*.jsonl"} {
		if err := addGlob(CategoryCache, cacheDir, pattern); err != nil {
			return nil, err
		}
	}

	return items, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat %q: %w", path, err)
}

// mergeMigrationPath copies a legacy source into the destination WITHOUT
// deleting the destination tree (Blocker 2 data-safety fix). It returns
// conflicted=true if any per-file conflict was encountered (an existing,
// newer XDG file at a destination leaf), in which case the existing XDG file
// is PRESERVED and the legacy file is NOT copied over it.
//
// Behavior by node type:
//   - source file, no destination          -> copy
//   - source file, destination is a file    -> CONFLICT, preserve XDG (skip)
//   - source file, destination is a dir      -> CONFLICT, preserve XDG (skip)
//   - source symlink                          -> copy if no destination, else CONFLICT
//   - source dir                              -> recurse, merging children
func mergeMigrationPath(source, destination string) (conflicted bool, err error) {
	srcInfo, err := os.Lstat(source)
	if err != nil {
		return false, fmt.Errorf("stat source %q: %w", source, err)
	}

	dstInfo, dstErr := os.Lstat(destination)
	dstExists := dstErr == nil
	if dstErr != nil && !os.IsNotExist(dstErr) {
		return false, fmt.Errorf("stat destination %q: %w", destination, dstErr)
	}

	// Source directory: recurse into children, creating the destination dir if
	// needed but never removing it.
	if srcInfo.Mode()&os.ModeSymlink == 0 && srcInfo.IsDir() {
		if dstExists && !dstInfo.IsDir() {
			// Destination is a non-dir blocking the merge. Preserve it; report.
			return true, nil
		}
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return false, fmt.Errorf("create destination dir %q: %w", destination, err)
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return false, fmt.Errorf("read source dir %q: %w", source, err)
		}
		anyConflict := false
		for _, entry := range entries {
			childConflict, err := mergeMigrationPath(
				filepath.Join(source, entry.Name()),
				filepath.Join(destination, entry.Name()),
			)
			if err != nil {
				return false, err
			}
			anyConflict = anyConflict || childConflict
		}
		return anyConflict, nil
	}

	// Source is a file or symlink. If a destination already exists, preserve
	// it (newer XDG data wins) and report the conflict.
	if dstExists {
		return true, nil
	}
	// The Lstat above is a best-effort fast path; the authoritative no-clobber
	// guarantee is the O_EXCL create inside copyMigrationPath. If a concurrent
	// Agent Deck process created the destination in the TOCTOU window, the copy
	// returns errDestinationExists — preserve the existing file, report conflict.
	if err := copyMigrationPath(source, destination); err != nil {
		if errors.Is(err, errDestinationExists) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func copyMigrationPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("stat source %q: %w", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return copyMigrationSymlink(source, destination)
	}
	if info.IsDir() {
		return copyMigrationDir(source, destination)
	}
	return copyMigrationFile(source, destination, info.Mode().Perm())
}

func copyMigrationDir(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create destination parent %q: %w", filepath.Dir(destination), err)
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destPath := destination
		if rel != "." {
			destPath = filepath.Join(destination, rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return copyMigrationSymlink(path, destPath)
		}
		if entry.IsDir() {
			if err := os.MkdirAll(destPath, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create directory %q: %w", destPath, err)
			}
			return nil
		}
		return copyMigrationFile(path, destPath, info.Mode().Perm())
	})
}

func copyMigrationFile(source, destination string, mode fs.FileMode) error {
	dstDir := filepath.Dir(destination)
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return fmt.Errorf("create destination parent %q: %w", dstDir, err)
	}
	src, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source %q: %w", source, err)
	}
	defer src.Close()

	// Atomic copy (data-safety): stream into a private temp file in the SAME
	// directory, fsync + close it, then atomically publish it to the final
	// name. A copy/close/sync failure therefore never leaves a partial file at
	// the destination that a later run would mistake for an existing XDG
	// conflict — the partial only ever exists under the temp name and is
	// removed on any error.
	tmp, err := os.CreateTemp(dstDir, ".agentdeck-migrate-*")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", dstDir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("copy %q to %q: %w", source, destination, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file for %q: %w", destination, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file for %q: %w", destination, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file for %q: %w", destination, err)
	}

	// Atomic no-clobber publish (Blocker 1, TOCTOU fix). os.Link refuses to
	// overwrite an existing name (EEXIST), so we never truncate a destination
	// that a concurrent Agent Deck process created after the caller's existence
	// check. On EEXIST we surface the race as a conflict sentinel; the caller
	// preserves the existing file. The temp is always removed afterwards.
	if err := os.Link(tmpName, destination); err != nil {
		cleanup()
		if errors.Is(err, os.ErrExist) {
			return errDestinationExists
		}
		return fmt.Errorf("publish destination %q: %w", destination, err)
	}
	cleanup()
	return nil
}

func copyMigrationSymlink(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create destination parent %q: %w", filepath.Dir(destination), err)
	}
	target, err := os.Readlink(source)
	if err != nil {
		return fmt.Errorf("read symlink %q: %w", source, err)
	}
	// os.Symlink fails with EEXIST if the destination already exists; map it to
	// the conflict sentinel so a concurrent create is preserved, never replaced.
	if err := os.Symlink(target, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errDestinationExists
		}
		return fmt.Errorf("create symlink %q: %w", destination, err)
	}
	return nil
}
