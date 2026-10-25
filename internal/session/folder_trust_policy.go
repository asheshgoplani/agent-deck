package session

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// folderTrustAllowed is the FX-8 folder-trust rule: may agent-deck mark dir
// trusted for inst (seed the tool's "trust this folder?" answer, source a
// relative env_file from it)? It returns the reason it may, or "" when it
// may not.
//
//   - [security] auto_trust_folders (unset: true on an existing install,
//     false on a new one; see InstallKind),
//   - a folder agent-deck owns: under its data, legacy or config root
//     (conductor, triage, scratch and loadout dirs),
//   - the worktree or multi-repo dir agent-deck made for this session, or a
//     conductor session,
//   - a folder in [security] trusted_folders, or under one.
func folderTrustAllowed(inst *Instance, dir string) string {
	if dir == "" {
		return ""
	}
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		cfg = &UserConfig{}
	}
	if cfg.Security.GetAutoTrustFolders() {
		return "auto_trust_folders"
	}
	for _, get := range []func() (string, error){GetAgentDeckDir, agentpaths.DataDir, agentpaths.LegacyDir, agentpaths.ConfigDir} {
		if root, err := get(); err == nil && folderWithin(dir, root) {
			return "agent_deck_dir"
		}
	}
	if inst != nil {
		if inst.IsConductor {
			return "conductor"
		}
		if folderWithin(dir, inst.WorktreePath) {
			return "worktree"
		}
		if folderWithin(dir, inst.MultiRepoTempDir) {
			return "multi_repo"
		}
	}
	for _, entry := range cfg.Security.TrustedFolders {
		if folderWithin(dir, ExpandPath(strings.TrimSpace(entry))) {
			return "trusted_folders"
		}
	}
	return ""
}

// folderWithin reports whether path is root or below it, comparing cleaned
// absolute paths and, where they exist, their symlink-resolved forms.
func folderWithin(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	norm := func(p string) []string {
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return nil
		}
		out := []string{abs}
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			out = append(out, real)
		}
		return out
	}
	for _, p := range norm(path) {
		for _, r := range norm(root) {
			rel, err := filepath.Rel(r, p)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return true
			}
		}
	}
	return false
}

// folderTrustSkipped logs a pre-seed that the rule refused.
func folderTrustSkipped(tool string, inst *Instance, dir string) {
	attrs := []any{slog.String("tool", tool), slog.String("dir", trustLogValue(dir))}
	if inst != nil {
		attrs = append(attrs, slog.String("instance_id", inst.ID))
	}
	sessionLog.Info("folder_trust_skipped", attrs...)
}

// folderTrustHint is the stderr line a CLI launch prints when it did not
// pre-trust the folder.
func folderTrustHint(dir string) string {
	return fmt.Sprintf("Note: agent-deck did not mark %s as trusted, so the tool may ask \"trust this folder?\" first. "+
		"To let agent-deck trust it: launch with --trust-folder, or add it to [security] trusted_folders in config.toml.", dir)
}

// AddTrustedFolder adds dir to [security] trusted_folders (once) through the
// regular config writer. Used by `launch --trust-folder` / `add --trust-folder`.
func AddTrustedFolder(dir string) error {
	abs, err := filepath.Abs(ExpandPath(dir))
	if err != nil {
		return fmt.Errorf("resolve %q: %w", dir, err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	ClearUserConfigCache()
	cfg, err := LoadUserConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &UserConfig{}
	}
	for _, entry := range cfg.Security.TrustedFolders {
		if folderWithin(abs, ExpandPath(entry)) {
			return nil
		}
	}
	updated := *cfg
	updated.Security.TrustedFolders = append(append([]string(nil), cfg.Security.TrustedFolders...), abs)
	if err := SaveUserConfig(&updated); err != nil {
		return fmt.Errorf("add %s to [security] trusted_folders: %w", abs, err)
	}
	ClearUserConfigCache()
	return nil
}
