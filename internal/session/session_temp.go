package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"al.essio.dev/pkg/shellescape"
)

const (
	repositorySessionTempMarker = ".agent-deck-session-temp"
	repositoryGitExcludeRule    = ".agent-deck/"
	sandboxSessionTempDir       = "/agent-deck-session-tmp"
)

var repositoryGitExcludeMu sync.Mutex

func (i *Instance) repositorySessionTempDir() string {
	root, err := i.repositorySessionProjectRoot()
	if err != nil {
		root = i.ProjectPath
	}
	return filepath.Join(root, ".agent-deck", "tmp", i.ID)
}

func (i *Instance) commandSessionTempDir() string {
	if i.IsSandboxed() {
		return sandboxSessionTempDir
	}
	return i.repositorySessionTempDir()
}

func (i *Instance) buildSessionTempExportPrefix() string {
	if i == nil || i.IsSSH() {
		return ""
	}
	tempDir := shellescape.Quote(i.commandSessionTempDir())
	var prefix strings.Builder
	for _, name := range []string{"TMPDIR", "TMP", "TEMP", "AGENT_DECK_SESSION_TMPDIR"} {
		fmt.Fprintf(&prefix, "export %s=%s; ", name, tempDir)
	}
	return prefix.String()
}

func (i *Instance) repositorySessionProjectRoot() (string, error) {
	if i == nil || i.ProjectPath == "" {
		return "", fmt.Errorf("project path is required")
	}
	project, err := filepath.EvalSymlinks(i.ProjectPath)
	if err != nil {
		return "", fmt.Errorf("resolve project path %s: %w", i.ProjectPath, err)
	}
	if out, gitErr := exec.Command("git", "-C", project, "rev-parse", "--show-toplevel").Output(); gitErr == nil {
		root := strings.TrimSpace(string(out))
		if root == "" {
			return "", fmt.Errorf("Git returned an empty worktree root for %s", project)
		}
		resolved, resolveErr := filepath.EvalSymlinks(root)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve Git worktree root %s: %w", root, resolveErr)
		}
		return resolved, nil
	}
	return project, nil
}

func (i *Instance) prepareRepositorySessionTemp() error {
	if i == nil || i.ID == "" || i.ProjectPath == "" {
		return fmt.Errorf("session id and project path are required")
	}
	if err := ensureRepositoryGitExclude(); err != nil {
		return err
	}
	projectRoot, err := i.repositorySessionProjectRoot()
	if err != nil {
		return err
	}
	if filepath.Base(i.ID) != i.ID || i.ID == "." || i.ID == ".." {
		return fmt.Errorf("invalid session id for repository temp root: %q", i.ID)
	}
	pinnedProject, err := openProjectRoot(projectRoot)
	if err != nil {
		return fmt.Errorf("open project root: %w", err)
	}
	defer pinnedProject.root.Close()
	tempParent, _, err := pinnedProject.openPinnedDir(filepath.Join(".agent-deck", "tmp"), true)
	if err != nil {
		return fmt.Errorf("open repository temp parent: %w", err)
	}
	defer tempParent.Close()
	tempParentInfo, err := tempParent.Stat(".")
	if err != nil || !ownedByCurrentUser(tempParentInfo) {
		return fmt.Errorf("repository temp parent is not owned by the current user")
	}
	if _, err := tempParent.Lstat(i.ID); os.IsNotExist(err) {
		if err := tempParent.Mkdir(i.ID, 0o700); err != nil {
			return fmt.Errorf("create session temp root: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect session temp root: %w", err)
	}
	sessionRoot, err := pinnedProject.openPinnedChildDir(tempParent, i.ID, false)
	if err != nil {
		return fmt.Errorf("open session temp root: %w", err)
	}
	defer sessionRoot.Close()
	sessionRootInfo, err := sessionRoot.Stat(".")
	if err != nil || !ownedByCurrentUser(sessionRootInfo) {
		return fmt.Errorf("session temp root is not owned by the current user")
	}
	if err := sessionRoot.Chmod(".", 0o700); err != nil {
		return fmt.Errorf("secure session temp root: %w", err)
	}

	marker := []byte("schema=1\nsession_id=" + i.ID + "\n")
	markerInfo, err := sessionRoot.Lstat(repositorySessionTempMarker)
	if err == nil {
		if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() || !ownedByCurrentUser(markerInfo) {
			return fmt.Errorf("refusing non-regular session temp marker")
		}
		markerFile, err := sessionRoot.Open(repositorySessionTempMarker)
		if err != nil {
			return fmt.Errorf("open session temp marker: %w", err)
		}
		existing, readErr := io.ReadAll(io.LimitReader(markerFile, 4096))
		_ = markerFile.Close()
		if readErr != nil {
			return fmt.Errorf("read session temp marker: %w", readErr)
		}
		if string(existing) != string(marker) {
			return fmt.Errorf("session temp marker does not belong to session %s", i.ID)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect session temp marker: %w", err)
	}
	markerFile, err := sessionRoot.OpenFile(repositorySessionTempMarker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create session temp marker: %w", err)
	}
	if _, err := markerFile.Write(marker); err != nil {
		_ = markerFile.Close()
		return fmt.Errorf("write session temp marker: %w", err)
	}
	if err := markerFile.Close(); err != nil {
		return fmt.Errorf("close session temp marker: %w", err)
	}
	return nil
}

// CleanupRepositorySessionTemp removes only this session's marked repository
// temp root. A missing root is a no-op so sessions that were never started can
// still be removed.
func (i *Instance) CleanupRepositorySessionTemp() error {
	if i == nil || i.IsSSH() {
		return nil
	}
	projectRoot, err := i.repositorySessionProjectRoot()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	pinnedProject, err := openProjectRoot(projectRoot)
	if err != nil {
		return fmt.Errorf("open project root: %w", err)
	}
	defer pinnedProject.root.Close()
	tempParent, _, err := pinnedProject.openPinnedDir(filepath.Join(".agent-deck", "tmp"), false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open repository temp parent: %w", err)
	}
	defer tempParent.Close()
	rootInfo, err := tempParent.Lstat(i.ID)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect session temp root: %w", err)
	}
	if !ownedByCurrentUser(rootInfo) {
		return fmt.Errorf("session temp root is not owned by the current user")
	}
	sessionRoot, err := pinnedProject.openPinnedChildDir(tempParent, i.ID, false)
	if err != nil {
		return fmt.Errorf("open session temp root: %w", err)
	}
	markerInfo, err := sessionRoot.Lstat(repositorySessionTempMarker)
	if err != nil {
		_ = sessionRoot.Close()
		return fmt.Errorf("inspect session temp marker: %w", err)
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() || !ownedByCurrentUser(markerInfo) {
		_ = sessionRoot.Close()
		return fmt.Errorf("session temp marker is not a current-user-owned regular file")
	}
	markerFile, err := sessionRoot.Open(repositorySessionTempMarker)
	if err != nil {
		_ = sessionRoot.Close()
		return fmt.Errorf("open session temp marker: %w", err)
	}
	marker, readErr := io.ReadAll(io.LimitReader(markerFile, 4096))
	_ = markerFile.Close()
	_ = sessionRoot.Close()
	if readErr != nil {
		return fmt.Errorf("read session temp marker: %w", readErr)
	}
	want := "schema=1\nsession_id=" + i.ID + "\n"
	if string(marker) != want {
		return fmt.Errorf("session temp marker does not belong to session %s", i.ID)
	}
	if err := makePinnedTreeOwnerWritable(pinnedProject, tempParent, i.ID); err != nil {
		return fmt.Errorf("make session temp root writable: %w", err)
	}
	if err := pinnedProject.removePinned(tempParent, i.ID); err != nil {
		return fmt.Errorf("remove session temp root: %w", err)
	}
	return nil
}

func makePinnedTreeOwnerWritable(project *projectRoot, parent *os.Root, name string) error {
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil
	}
	child, err := project.openPinnedChildDir(parent, name, false)
	if err != nil {
		return err
	}
	defer child.Close()
	if err := child.Chmod(".", info.Mode().Perm()|0o700); err != nil {
		return err
	}
	dir, err := child.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := makePinnedTreeOwnerWritable(project, child, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func ensureRepositoryGitExclude() error {
	ignorePath, err := repositoryGlobalGitExcludePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ignorePath), 0o700); err != nil {
		return fmt.Errorf("create global Git excludes directory: %w", err)
	}

	repositoryGitExcludeMu.Lock()
	defer repositoryGitExcludeMu.Unlock()
	lock, err := os.OpenFile(ignorePath+".agent-deck.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open global Git excludes lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock global Git excludes: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := os.ReadFile(ignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read global Git excludes: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == repositoryGitExcludeRule {
			return nil
		}
	}
	f, err := os.OpenFile(ignorePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open global Git excludes: %w", err)
	}
	defer f.Close()
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return fmt.Errorf("separate global Git exclude rule: %w", err)
		}
	}
	if _, err := f.WriteString(repositoryGitExcludeRule + "\n"); err != nil {
		return fmt.Errorf("append global Git exclude rule: %w", err)
	}
	return nil
}

func repositoryGlobalGitExcludePath() (string, error) {
	cmd := exec.Command("git", "config", "--global", "--path", "--get", "core.excludesFile")
	out, err := cmd.CombinedOutput()
	if err == nil {
		if path := strings.TrimSpace(string(out)); path != "" {
			return path, nil
		}
	} else if !errors.Is(err, exec.ErrNotFound) {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			return "", fmt.Errorf("resolve global Git excludes: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git", "ignore"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolve home for global Git excludes: %w", err)
	}
	return filepath.Join(home, ".config", "git", "ignore"), nil
}
