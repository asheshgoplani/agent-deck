package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// PruneHookArtifacts removes orphaned hook artifact groups after a 24-hour
// creation grace period. IDs in any profile, including archived sessions, are
// protected. An unknown registry prevents all cleanup.
func PruneHookArtifacts() error { return pruneHookArtifacts("") }

func pruneHookArtifacts(deletedID string) error {
	ids, err := hookRegistryIDs()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(GetHooksDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	groups := make(map[string][]string)
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "sandbox" || entry.Name() == ".codex-consumed" {
			if !entry.IsDir() {
				continue
			}
			children, err := fs.ReadDir(root.FS(), entry.Name())
			if err != nil {
				return err
			}
			for _, child := range children {
				id := child.Name()
				if entry.Name() == ".codex-consumed" && strings.HasSuffix(id, ".lock") {
					id = strings.TrimSuffix(id, ".lock")
				} else if !child.IsDir() {
					continue
				}
				if validHookArtifactID(id) {
					groups[id] = append(groups[id], filepath.Join(entry.Name(), child.Name()))
				}
			}
			continue
		}
		if entry.IsDir() {
			continue
		}
		if id := hookArtifactID(entry.Name()); id != "" {
			groups[id] = append(groups[id], entry.Name())
		}
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	var errs []error
	for id, paths := range groups {
		if ids[id] {
			continue
		}
		// Age is checked for the whole group, including nested sandbox evidence.
		old, err := hookArtifactGroupOld(root, paths, cutoff)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !old && id != deletedID {
			continue
		}
		if err := removeHookArtifactGroup(root, paths, cutoff, id == deletedID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func validHookArtifactID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\") && !strings.HasPrefix(id, ".")
}

func hookArtifactID(name string) string {
	// atomic hook writes use .<id>.<suffix>.tmp-<random>.
	if strings.HasPrefix(name, ".") {
		index := strings.LastIndex(name, ".tmp-")
		if index < 0 {
			return ""
		}
		name = name[1:index]
	}
	name = strings.TrimSuffix(name, ".tmp")
	for _, suffix := range []string{".generation.json", ".codex-writer.lock", ".projectdir-missing", ".json", ".sid", ".lock"} {
		if strings.HasSuffix(name, suffix) {
			id := strings.TrimSuffix(name, suffix)
			if validHookArtifactID(id) {
				return id
			}
			return ""
		}
	}
	return ""
}

func hookArtifactGroupOld(root *os.Root, paths []string, cutoff time.Time) (bool, error) {
	old := true
	for _, path := range paths {
		if err := fs.WalkDir(root.FS(), path, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := root.Lstat(name)
			if err != nil {
				return err
			}
			if !info.ModTime().Before(cutoff) {
				old = false
			}
			return nil
		}); err != nil {
			return false, err
		}
	}
	return old, nil
}

func removeHookArtifactGroup(root *os.Root, paths []string, cutoff time.Time, deleted bool) error {
	// Cooperate with hook generation and consumption writers. Never wait for
	// active writers, and keep all acquired locks until the group is removed.
	var locks []*os.File
	defer func() {
		for _, f := range locks {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		}
	}()
	var lockPaths []string
	for _, path := range paths {
		if err := fs.WalkDir(root.FS(), path, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(name, ".lock") {
				lockPaths = append(lockPaths, name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for _, path := range lockPaths {
		f, err := root.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			return nil
		}
		locks = append(locks, f)
	}
	// A writer may have refreshed an artifact between the scan and lock acquisition.
	if !deleted {
		old, err := hookArtifactGroupOld(root, paths, cutoff)
		if err != nil {
			return err
		}
		if !old {
			return nil
		}
	}
	// Root confines recursive removals even if a sandbox replaces an entry with
	// a symlink while cleanup is in progress. Remove lock names last.
	for _, lockPass := range []bool{false, true} {
		for _, path := range paths {
			if strings.HasSuffix(path, ".lock") != lockPass {
				continue
			}
			if err := root.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func hookRegistryIDs() (map[string]bool, error) {
	root, err := profileDataRootDir()
	if err != nil {
		return nil, err
	}
	profiles := filepath.Join(root, ProfilesDirName)
	entries, err := os.ReadDir(profiles)
	if err != nil {
		return nil, fmt.Errorf("hook cleanup cannot enumerate profiles: %w", err)
	}
	ids := make(map[string]bool)
	registries := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("hook cleanup cannot inspect symlink profile %q", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(profiles, entry.Name())
		dbPath := filepath.Join(dir, "state.db")
		info, err := os.Lstat(dbPath)
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("invalid hook registry %s", dbPath)
			}
			if err := readHookRegistry(dbPath, ids); err != nil {
				return nil, err
			}
			registries++
			legacyPath := filepath.Join(dir, "sessions.json")
			if _, err := os.Lstat(legacyPath); err == nil {
				if err := readLegacyHookRegistry(legacyPath, ids); err != nil {
					return nil, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := readLegacyHookRegistry(filepath.Join(dir, "sessions.json"), ids); err != nil {
			return nil, err
		}
		registries++
	}
	// The pre-profile registry may still own artifacts during migration.
	legacy := filepath.Join(root, "sessions.json")
	if _, err := os.Lstat(legacy); err == nil {
		if err := readLegacyHookRegistry(legacy, ids); err != nil {
			return nil, err
		}
		registries++
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if registries == 0 {
		return nil, fmt.Errorf("hook cleanup has no known registry")
	}
	return ids, nil
}

func readHookRegistry(path string, ids map[string]bool) error {
	// Do not use immutable=1: it ignores committed rows still in the WAL.
	uri := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", uri.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT id FROM instances")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids[id] = true
	}
	return rows.Err()
}

func readLegacyHookRegistry(path string, ids map[string]bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var registry struct {
		Instances json.RawMessage `json:"instances"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return err
	}
	if registry.Instances == nil || string(registry.Instances) == "null" {
		return fmt.Errorf("unknown hook registry %s", path)
	}
	var instances []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registry.Instances, &instances); err != nil {
		return err
	}
	for _, instance := range instances {
		if !validHookArtifactID(instance.ID) {
			return fmt.Errorf("invalid registry ID")
		}
		ids[instance.ID] = true
	}
	return nil
}
