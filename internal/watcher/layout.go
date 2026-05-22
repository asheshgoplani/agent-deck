package watcher

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "embed"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

//go:embed assets/watcher-templates/CLAUDE.md
var watcherClaudeTemplate []byte

//go:embed assets/watcher-templates/HERMES.md
var watcherHermesTemplate []byte

//go:embed assets/watcher-templates/POLICY.md
var watcherPolicyTemplate []byte

//go:embed assets/watcher-templates/LEARNINGS.md
var watcherLearningsTemplate []byte

// watcherNameRegex mirrors internal/session conductorNameRegex. T-21-PI mitigation.
var watcherNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func validateWatcherName(name string) error {
	if name == "" {
		return fmt.Errorf("watcher name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("watcher name too long (max 64 characters)")
	}
	if !watcherNameRegex.MatchString(name) {
		return fmt.Errorf("invalid watcher name %q: must start alphanumeric; only alphanumeric, dots, underscores, hyphens allowed", name)
	}
	return nil
}

// LayoutDir returns the root watcher dir (~/.agent-deck/watcher). Delegates to session.WatcherDir for single-source-of-truth.
func LayoutDir() (string, error) {
	return session.WatcherDir()
}

// WatcherDir returns ~/.agent-deck/watcher/<name> after validating name. T-21-PI mitigation.
func WatcherDir(name string) (string, error) {
	if err := validateWatcherName(name); err != nil {
		return "", err
	}
	return session.WatcherNameDir(name)
}

// ScaffoldWatcherLayout creates the singular watcher/ dir and writes default shared files if absent.
// Uses O_CREATE|O_EXCL for TOCTOU-safe idempotent writes.
func ScaffoldWatcherLayout() error {
	dir, err := LayoutDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create watcher dir: %w", err)
	}
	for _, f := range []struct {
		name    string
		content []byte
	}{
		{"CLAUDE.md", watcherClaudeTemplate},
		{"HERMES.md", watcherHermesTemplate},
		{"POLICY.md", watcherPolicyTemplate},
		{"LEARNINGS.md", watcherLearningsTemplate},
		{"clients.json", []byte("{}\n")},
	} {
		if err := writeIfAbsent(filepath.Join(dir, f.name), f.content); err != nil {
			return fmt.Errorf("scaffold %s: %w", f.name, err)
		}
	}
	return nil
}

func writeIfAbsent(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	_, err = f.Write(content)
	return err
}

// MigrateLegacyWatchersDir performs the one-shot ~/.agent-deck/watchers -> ~/.agent-deck/watcher rename
// and creates a relative compatibility symlink watchers -> watcher. Single-shot; subsequent calls no-op.
// SECURITY T-21-SL: uses os.Lstat (not os.Stat) and refuses if watcher/ is a symlink targeting outside the deck root.
func MigrateLegacyWatchersDir() error {
	deck, err := session.GetAgentDeckDir()
	if err != nil {
		return err
	}
	absDeck, err := filepath.Abs(deck)
	if err != nil {
		return err
	}
	legacy := filepath.Join(deck, "watchers")
	current := filepath.Join(deck, "watcher")

	curInfo, curErr := os.Lstat(current)      // Lstat: do NOT follow symlink
	legacyInfo, legacyErr := os.Lstat(legacy) // Lstat: do NOT follow symlink

	// Symlink-traversal refusal (T-21-SL).
	if curErr == nil && curInfo.Mode()&os.ModeSymlink != 0 {
		target, terr := os.Readlink(current)
		if terr != nil {
			return fmt.Errorf("readlink watcher: %w", terr)
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(deck, resolved)
		}
		absResolved, _ := filepath.Abs(resolved)
		if !strings.HasPrefix(absResolved, absDeck+string(os.PathSeparator)) && absResolved != absDeck {
			return fmt.Errorf("refusing migration: ~/.agent-deck/watcher is a symlink targeting %q (outside %s)", target, deck)
		}
		// Symlink inside the deck — treat as "already migrated" (no-op).
		return nil
	}

	// Happy path: legacy exists as real dir, current missing -> rename + symlink.
	if legacyErr == nil && legacyInfo.Mode()&os.ModeSymlink == 0 && os.IsNotExist(curErr) {
		if err := os.Rename(legacy, current); err != nil {
			return fmt.Errorf("migrate watchers dir: %w", err)
		}
		if err := os.Symlink("watcher", legacy); err != nil {
			slog.Warn("watcher: symlink creation failed (non-fatal)", "error", err)
		}
		slog.Info("watcher: migrated legacy ~/.agent-deck/watchers/ → ~/.agent-deck/watcher/",
			slog.String("note", "legacy ~/.agent-deck/issue-watcher/ NOT migrated (out of scope per REQ-WF-6)"))
		return nil
	}

	// Collision: both exist as real dirs — log and skip.
	if legacyErr == nil && legacyInfo.Mode()&os.ModeSymlink == 0 && curErr == nil && curInfo.Mode()&os.ModeSymlink == 0 {
		slog.Warn("watcher: both ~/.agent-deck/watchers/ and ~/.agent-deck/watcher/ exist; skipping migration")
		return nil
	}
	return nil
}
