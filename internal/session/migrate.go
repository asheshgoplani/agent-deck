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

// ErrAmbiguousConversation reports that the source project dir holds several
// conversations while the instance has no id that resolves to one of them, so
// no transcript can be attributed to it (#1815). Distinct from
// ErrNoConversation: something IS there, it just cannot be shown to be this
// session's, and copying a guess across accounts is not acceptable.
var ErrAmbiguousConversation = errors.New("conversation cannot be attributed to this session")

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
	// #1851: the file this would move is located through the local placeholder
	// ProjectPath, so for an --ssh session it belongs to a LOCAL session. Moving
	// it takes a conversation away from the session that owns it.
	if !inst.TranscriptIsResolvableLocally() {
		return "", nil
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
	srcProjDir, err := containedConversationPath(src, "projects", projDirName)
	if err != nil {
		return "", fmt.Errorf("source project dir: %w", err)
	}

	sid := inst.ClaudeSessionID
	srcFile := ""
	if sid != "" {
		// A stored id that is not a single path segment is refused outright
		// rather than falling back to discovery: it must never select a file
		// outside the project dir, and it must not be silently replaced.
		candidate, err := containedConversationPath(srcProjDir, sid+".jsonl")
		if err != nil {
			return "", fmt.Errorf("source conversation: %w", err)
		}
		if fileIsRegular(candidate) {
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
		// #1815: "newest conversation in the project dir" is a guess, and it
		// is a guess whether or not an older id was stored — the fallback is
		// choosing a DIFFERENT conversation by mtime either way, so a stale
		// stored id does not make the replacement owned.
		//
		// Where the guess is AMBIGUOUS (more than one conversation in the
		// directory, i.e. sessions share this cwd) it is refused outright
		// rather than copied: copying first and refusing to resume later has
		// already carried a neighbouring session's conversation into another
		// account's config dir. Where the directory holds exactly one
		// conversation there is nothing to confuse it with, so the repair the
		// fallback exists for still works — but the id stays suspect until
		// something vouches for it, so it cannot authorize a `--resume`.
		if conversationCount(srcProjDir) > 1 {
			return "", fmt.Errorf("%w: %s holds several conversations and %s has no resolvable id of its own, so the newest one cannot be attributed to it",
				ErrAmbiguousConversation, srcProjDir, inst.Title)
		}
		srcFile, sid = newestFile, newestID
		inst.adoptDiscoveredClaudeSessionID(newestID)
	}
	if _, err := conversationPathComponent(sid + ".jsonl"); err != nil {
		return "", fmt.Errorf("source conversation: %w", err)
	}

	dstProjDir, err := containedConversationPath(dst, "projects", projDirName)
	if err != nil {
		return "", fmt.Errorf("target project dir: %w", err)
	}
	// Do not let a writable destination symlink redirect a migration into an
	// unrelated tree. This check is intentionally destination-only: source
	// accounts remain untouched, and configured source roots may legitimately be
	// symlinked by operators.
	if err := ensureNoSymlinkPath(dstProjDir); err != nil {
		return "", fmt.Errorf("unsafe target project dir: %w", err)
	}
	if err := os.MkdirAll(dstProjDir, 0o700); err != nil {
		return "", fmt.Errorf("create target project dir: %w", err)
	}
	dstFile, err := containedConversationPath(dstProjDir, sid+".jsonl")
	if err != nil {
		return "", fmt.Errorf("target conversation: %w", err)
	}
	if info, err := os.Lstat(dstFile); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to overwrite symlink destination: %s", dstFile)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect target conversation: %w", err)
	}
	bak := ""
	if fileIsRegular(dstFile) {
		// Backup before any destructive write (2026-06-04 incident, S2). Use a
		// collision-free name so two retries never overwrite the prior backup.
		for n := 0; ; n++ {
			suffix := fmt.Sprintf("%d", time.Now().UnixNano())
			if n > 0 {
				suffix = fmt.Sprintf("%s-%d", suffix, n)
			}
			bak = fmt.Sprintf("%s.bak-%s", dstFile, suffix)
			if _, statErr := os.Lstat(bak); os.IsNotExist(statErr) {
				break
			}
		}
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

	// Migrate the companion subagent directory (#1571): subagent transcripts
	// live in projects/<encoded-path>/<sid>/ next to the jsonl. Resume works
	// without it, but leaving it behind silently loses those transcripts.
	// Copy-only, per-file size-verified; failure aborts before the caller
	// flips the account field (the already-copied jsonl is harmless and a
	// rerun is idempotent).
	srcSubagentDir, err := containedConversationPath(srcProjDir, sid)
	if err != nil {
		return "", fmt.Errorf("source subagent dir: %w", err)
	}
	dstSubagentDir, err := containedConversationPath(dstProjDir, sid)
	if err != nil {
		return "", fmt.Errorf("target subagent dir: %w", err)
	}
	if info, err := os.Stat(srcSubagentDir); err == nil && info.IsDir() {
		if err := copyDirVerified(srcSubagentDir, dstSubagentDir); err != nil {
			return "", fmt.Errorf("copy subagent dir: %w", err)
		}
	}
	return dstFile, nil
}

// copyDirVerified recursively copies the regular files under src into dst
// (created 0700), size-verifying each copy. Symlinks and other special files
// are skipped.
func copyDirVerified(src, dst string) error {
	if err := ensureNoSymlinkPath(dst); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if err := ensureNoSymlinkPath(target); err != nil {
				return err
			}
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFileVerified(path, target)
	})
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
	// #1851: the .bak- orphan this would restore lives in a LOCAL project dir
	// keyed on the placeholder ProjectPath; for an --ssh session it is another
	// session's residue, not this one's.
	if !inst.TranscriptIsResolvableLocally() {
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
	projDir, err := containedConversationPath(cfgDir, "projects", encodedPath)
	if err != nil {
		return "", fmt.Errorf("project dir: %w", err)
	}
	live, err := containedConversationPath(projDir, inst.ClaudeSessionID+".jsonl")
	if err != nil {
		return "", fmt.Errorf("conversation: %w", err)
	}
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

// conversationCount reports how many UUID-named conversation files live in
// projDir. More than one means the directory is shared, so "the newest one"
// identifies nothing (#1815).
func conversationCount(projDir string) int {
	files, err := filepath.Glob(filepath.Join(projDir, "*.jsonl"))
	if err != nil {
		return 0
	}
	n := 0
	for _, file := range files {
		base := filepath.Base(file)
		if strings.HasPrefix(base, "agent-") || !uuidSessionFileRegex.MatchString(base) {
			continue
		}
		n++
	}
	return n
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
// files) and verifies the written size matches the source. Both the source and
// destination are checked with Lstat so a writable destination symlink cannot
// redirect the copy and a source is never removed or followed unexpectedly.
func copyFileVerified(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if !srcInfo.Mode().IsRegular() || srcInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source is not a regular file: %s", src)
	}
	if err := ensureNoSymlinkPath(filepath.Dir(dst)); err != nil {
		return fmt.Errorf("unsafe target path: %w", err)
	}
	if dstInfo, statErr := os.Lstat(dst); statErr == nil {
		if dstInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlink destination: %s", dst)
		}
		if !dstInfo.Mode().IsRegular() {
			return fmt.Errorf("destination is not a regular file: %s", dst)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect target: %w", statErr)
	}

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
	if written != srcInfo.Size() {
		return fmt.Errorf("size mismatch after copy: wrote %d bytes, source has %d", written, srcInfo.Size())
	}
	return nil
}

// conversationPathComponent validates one path segment that is derived from
// session state (an encoded project path or a native session id) before it is
// joined under a config root. Migration and restore build filenames from these
// values, so a segment must be exactly one name: no separator, no "..", no NUL.
// The returned value is the canonical single-segment form, which for a valid
// input is the input itself.
func conversationPathComponent(name string) (string, error) {
	if name == "" || name == "." || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`+"\x00") {
		return "", fmt.Errorf("conversation path component %q is not a single path segment", name)
	}
	clean := strings.TrimPrefix(filepath.Clean("/"+name), "/")
	if clean != name || filepath.Base(clean) != clean {
		return "", fmt.Errorf("conversation path component %q is not a single path segment", name)
	}
	return clean, nil
}

// containedConversationPath joins validated components under root and proves
// that the result still resolves inside root. Symlink redirection is a
// separate concern handled by ensureNoSymlinkPath at the write sites.
func containedConversationPath(root string, components ...string) (string, error) {
	root = filepath.Clean(root)
	parts := []string{root}
	for _, component := range components {
		clean, err := conversationPathComponent(component)
		if err != nil {
			return "", err
		}
		parts = append(parts, clean)
	}
	joined := filepath.Join(parts...)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes %q", joined, root)
	}
	return joined, nil
}

// ensureNoSymlinkPath verifies every existing component of path. MkdirAll and
// OpenFile otherwise follow a writable symlink in an intermediate destination
// directory, defeating copy-only source preservation.
func ensureNoSymlinkPath(path string) error {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			// Missing descendants will be created by the caller; no later
			// component can be inspected until that creation occurs.
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component is a symlink: %s", current)
		}
		if !info.IsDir() && current != abs {
			return fmt.Errorf("path component is not a directory: %s", current)
		}
	}
	return nil
}

func fileIsRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
