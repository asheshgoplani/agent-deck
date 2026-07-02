package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoConversation reports that the session has no conversation file on disk
// yet (e.g. a fresh session that never exchanged a message). Callers switching
// accounts should treat this as non-fatal: there is simply nothing to migrate.
var ErrNoConversation = errors.New("no conversation file found")

// MigrateConversation copies the session's Claude conversation file from its
// currently resolved config dir into targetConfigDir, so `claude --resume`
// finds the history after an account switch (#924 follow-up). Copy-only: the
// source is never modified or deleted. Returns the destination path, or ""
// when source and target resolve to the same directory (no-op).
//
// Validated against two real accounts (2026-06-10): `--resume <id>` is a pure
// file lookup under <config-dir>/projects/<encoded-path>/<id>.jsonl; the id is
// not bound to the logged-in account.
func MigrateConversation(inst *Instance, targetConfigDir string) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("nil instance")
	}
	return MigrateConversationFrom(inst, GetClaudeConfigDirForInstance(inst), targetConfigDir)
}

// MigrateConversationFrom is MigrateConversation with an explicit source
// config dir. Callers that mutate inst.Account before migrating must capture
// the old account's dir first — after the mutation the resolver returns the
// new dir.
//
// When the stored ClaudeSessionID has no file on disk (resume renames
// conversations to a fresh UUID), it falls back to the newest conversation
// file in the project dir and updates inst.ClaudeSessionID accordingly; the
// caller is responsible for persisting the instance.
func MigrateConversationFrom(inst *Instance, srcConfigDir, targetConfigDir string) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("nil instance")
	}
	if inst.Tool != "claude" {
		return "", fmt.Errorf("conversation migration is only supported for claude sessions (tool: %s)", inst.Tool)
	}
	src := ExpandPath(strings.TrimSpace(srcConfigDir))
	dst := ExpandPath(strings.TrimSpace(targetConfigDir))
	if src == "" || dst == "" {
		return "", fmt.Errorf("source and target config dirs must be non-empty")
	}
	if filepath.Clean(src) == filepath.Clean(dst) || resolveRealPath(src) == resolveRealPath(dst) {
		return "", nil
	}

	projDirName := ConvertToClaudeDirName(inst.ProjectPath)
	srcProjDir := filepath.Join(src, "projects", projDirName)

	sid := inst.ClaudeSessionID
	srcFile := ""
	if sid != "" {
		if candidate := filepath.Join(srcProjDir, sid+".jsonl"); fileIsRegular(candidate) {
			srcFile = candidate
		}
	}
	if srcFile == "" {
		// Stored id stale (resume renamed the file) or never captured: take
		// the newest conversation file in the project dir.
		newestFile, newestID := newestConversationFile(srcProjDir)
		if newestFile == "" {
			return "", fmt.Errorf("%w under %s", ErrNoConversation, srcProjDir)
		}
		srcFile, sid = newestFile, newestID
		inst.ClaudeSessionID = newestID
	}

	dstProjDir := filepath.Join(dst, "projects", projDirName)
	if err := os.MkdirAll(dstProjDir, 0o700); err != nil {
		return "", fmt.Errorf("create target project dir: %w", err)
	}
	dstFile := filepath.Join(dstProjDir, sid+".jsonl")
	bak := ""
	if fileIsRegular(dstFile) {
		// Backup before any destructive write (2026-06-04 incident, S2).
		bak = fmt.Sprintf("%s.bak-%d", dstFile, time.Now().Unix())
		if err := os.Rename(dstFile, bak); err != nil {
			return "", fmt.Errorf("backup existing conversation: %w", err)
		}
	}
	if err := copyFileVerified(srcFile, dstFile); err != nil {
		if bak != "" {
			_ = os.Remove(dstFile)
			if restoreErr := os.Rename(bak, dstFile); restoreErr != nil {
				return "", fmt.Errorf("%w (restore backup failed: %v)", err, restoreErr)
			}
		}
		return "", err
	}
	return dstFile, nil
}

// RestoreOrphanedConversationBackup restores a conversation whose live
// <id>.jsonl went missing but whose most recent <id>.jsonl.bak-<epoch>
// orphan still exists in the project dir (the #1533 data-loss residue).
// It is a no-op when a live <id>.jsonl is already present, when inst has
// no ClaudeSessionID, or when no matching .bak- orphan exists. Returns the
// restored path (or "" for no-op) and any error.
func RestoreOrphanedConversationBackup(inst *Instance, configDir string) (string, error) {
	if inst == nil || inst.Tool != "claude" || inst.ClaudeSessionID == "" || strings.TrimSpace(configDir) == "" {
		return "", nil
	}

	cfgDir := ExpandPath(strings.TrimSpace(configDir))
	projectPath := inst.EffectiveWorkingDir()
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}
	encodedPath := ConvertToClaudeDirName(resolvedPath)
	if encodedPath == "" {
		encodedPath = "-"
	}
	projDir := filepath.Join(cfgDir, "projects", encodedPath)
	live := filepath.Join(projDir, inst.ClaudeSessionID+".jsonl")
	if fileIsRegular(live) {
		return "", nil
	}

	bak, err := newestConversationBackup(projDir, inst.ClaudeSessionID)
	if err != nil {
		return "", err
	}
	if bak == "" {
		return "", nil
	}
	if err := os.Rename(bak, live); err == nil {
		return live, nil
	}
	if err := copyFileVerified(bak, live); err != nil {
		return "", err
	}
	return live, nil
}

// newestConversationFile returns the most recently modified UUID-named
// conversation file in projDir (and its session id), skipping agent-*.jsonl.
// Unlike findActiveSessionIDExcluding it has no recency cutoff: a conversation
// being migrated may be arbitrarily old.
func newestConversationFile(projDir string) (path, sessionID string) {
	files, err := filepath.Glob(filepath.Join(projDir, "*.jsonl"))
	if err != nil {
		return "", ""
	}
	var newest time.Time
	for _, file := range files {
		base := filepath.Base(file)
		if strings.HasPrefix(base, "agent-") || !uuidSessionFileRegex.MatchString(base) {
			continue
		}
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
			path = file
			sessionID = strings.TrimSuffix(base, ".jsonl")
		}
	}
	return path, sessionID
}

func newestConversationBackup(projDir, sessionID string) (string, error) {
	files, err := filepath.Glob(filepath.Join(projDir, sessionID+".jsonl.bak-*"))
	if err != nil {
		return "", fmt.Errorf("glob orphaned conversation backups: %w", err)
	}
	var newest string
	var newestMod time.Time
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest = file
			newestMod = info.ModTime()
		}
	}
	return newest, nil
}

// copyFileVerified copies src to dst (0600, matching Claude's conversation
// files) and verifies the written size matches the source.
func copyFileVerified(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open conversation: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create target conversation: %w", err)
	}
	written, copyErr := io.Copy(out, in)
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("copy conversation: %w", copyErr)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if written != srcInfo.Size() {
		return fmt.Errorf("size mismatch after copy: wrote %d bytes, source has %d", written, srcInfo.Size())
	}
	return nil
}

func fileIsRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
