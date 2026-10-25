package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

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

// trustedFoldersLine matches a one-line trusted_folders array.
var trustedFoldersLine = regexp.MustCompile(`(?m)^([ \t]*trusted_folders[ \t]*=[ \t]*\[)([^\]\n]*)\]`)

// appendTrustedFolderText adds dir to [security] trusted_folders as a text
// edit: into a one-line array under [security], as a new key under the
// header, or as a new [security] table at the end. The result must decode to
// the old document with exactly dir appended; otherwise it reports false.
func appendTrustedFolderText(content []byte, dir string) ([]byte, bool) {
	quoted := strconv.Quote(dir)
	var out []byte
	if loc := tomlTableHeader("security").FindIndex(content); loc != nil {
		section := content[loc[1]:]
		if next := regexp.MustCompile(`(?m)^[ \t]*\[`).FindIndex(section); next != nil {
			section = section[:next[0]]
		}
		if m := trustedFoldersLine.FindSubmatchIndex(section); m != nil {
			inner := strings.TrimSpace(string(section[m[4]:m[5]]))
			repl := quoted
			if inner != "" {
				repl = strings.TrimRight(inner, ", \t") + ", " + quoted
			}
			start := loc[1] + m[4]
			end := loc[1] + m[5]
			out = append(append(append([]byte(nil), content[:start]...), repl...), content[end:]...)
		} else if edited, ok := insertAfterTableHeader(content, "security", "trusted_folders = ["+quoted+"]\n"); ok {
			out = edited
		}
	} else {
		out = append([]byte(nil), content...)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, "\n[security]\ntrusted_folders = ["+quoted+"]\n"...)
	}
	if out == nil {
		return nil, false
	}
	var before, after UserConfig
	if _, err := toml.Decode(string(content), &before); err != nil {
		return nil, false
	}
	if _, err := toml.Decode(string(out), &after); err != nil {
		return nil, false
	}
	want := append(append([]string(nil), before.Security.TrustedFolders...), dir)
	if !reflect.DeepEqual(after.Security.TrustedFolders, want) {
		return nil, false
	}
	after.Security.TrustedFolders = before.Security.TrustedFolders
	if !reflect.DeepEqual(before, after) {
		return nil, false
	}
	return out, true
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
	// An existing config.toml is edited in place (comments and layout kept),
	// the same way as the one-time install-defaults write; only a layout the
	// edit cannot handle safely goes through the full rewrite.
	if path, err := GetUserConfigPath(); err == nil && pathPresent(path) {
		if content, err := os.ReadFile(path); err == nil {
			if edited, ok := appendTrustedFolderText(content, abs); ok {
				if err := writeConfigInPlace(path, edited); err != nil {
					return fmt.Errorf("add %s to [security] trusted_folders: %w", abs, err)
				}
				return nil
			}
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

// WarningFolderNotTrusted is the code of a LaunchWarning for a launch into a
// folder agent-deck did not mark trusted.
const WarningFolderNotTrusted = "FOLDER_NOT_TRUSTED"

// LaunchWarning is a machine-readable warning in a launch result.
type LaunchWarning struct {
	Code   string `json:"code"`
	Folder string `json:"folder,omitempty"`
	Hint   string `json:"hint"`
}

// folderNotTrustedWarning is the FOLDER_NOT_TRUSTED warning for dir.
func folderNotTrustedWarning(dir string) *LaunchWarning {
	return &LaunchWarning{
		Code:   WarningFolderNotTrusted,
		Folder: dir,
		Hint:   fmt.Sprintf("the tool may ask \"trust this folder?\" before it starts; run agent-deck launch --trust-folder %s, or add it to [security] trusted_folders in config.toml", shellQuote(dir)),
	}
}
