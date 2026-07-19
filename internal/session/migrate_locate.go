package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocateConversationConfigDir finds the Claude config dir that actually holds
// the instance's conversation file on disk (#1571).
//
// Why: legacy session records carry an empty Account field. The config-dir
// resolver then falls through to env/profile/global defaults, which can
// lexically equal the switch target — MigrateConversationFrom sees src == dst,
// returns ("", nil), and switch-account declares "nothing to migrate" while
// the real conversation sits in a different profile's config dir. The
// restarted `claude --resume <id>` then finds no conversation and the session
// loops in error. The disk is authoritative: scan every configured config dir
// for projects/<encoded-cwd>/<sid>.jsonl and treat the dir that contains it
// as the source.
//
// Candidates scanned (deduped, in order): extraCandidates (callers pass the
// resolver's answer first), every [profiles.<name>.claude].config_dir, the
// global [claude].config_dir, and ~/.claude.
//
// When inst.ClaudeSessionID is set, only exact <sid>.jsonl matches count and
// the LARGEST match wins — a poisoned few-hundred-byte stub left by a failed
// restart must never shadow the real conversation. When the id is empty, the
// newest UUID-named conversation file across all candidates wins.
//
// Returns ("", "", 0) when no conversation exists anywhere (fresh session).
func LocateConversationConfigDir(cfg *UserConfig, inst *Instance, extraCandidates ...string) (configDir, sessionID string, size int64) {
	if inst == nil || inst.Tool != "claude" || inst.ProjectPath == "" {
		return "", "", 0
	}
	candidates := conversationConfigDirCandidates(cfg, extraCandidates...)
	projDirName := ConvertToClaudeDirName(inst.ProjectPath)

	if sid := inst.ClaudeSessionID; sid != "" {
		bestSize := int64(-1)
		bestDir := ""
		for _, dir := range candidates {
			info, err := os.Stat(filepath.Join(dir, "projects", projDirName, sid+".jsonl"))
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if info.Size() > bestSize {
				bestSize, bestDir = info.Size(), dir
			}
		}
		if bestDir == "" {
			return "", "", 0
		}
		return bestDir, sid, bestSize
	}

	// No stored session id: newest conversation file across candidates.
	var bestDir, bestID string
	var bestSize int64
	var bestMod time.Time
	for _, dir := range candidates {
		path, id := newestConversationFile(filepath.Join(dir, "projects", projDirName))
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if bestDir == "" || info.ModTime().After(bestMod) {
			bestDir, bestID, bestSize, bestMod = dir, id, info.Size(), info.ModTime()
		}
	}
	return bestDir, bestID, bestSize
}

// conversationConfigDirCandidates returns the deduped list of config dirs to
// scan for a conversation: extras first (resolver answer), then every
// configured profile dir (sorted for determinism), the global dir, and the
// ~/.claude default.
func conversationConfigDirCandidates(cfg *UserConfig, extras ...string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		dir = ExpandPath(strings.TrimSpace(dir))
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		key := resolveRealPath(dir)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, dir)
	}
	for _, e := range extras {
		add(e)
	}
	if cfg != nil {
		names := make([]string, 0, len(cfg.Profiles))
		for name := range cfg.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add(cfg.GetProfileClaudeConfigDir(name))
		}
		add(cfg.Claude.ConfigDir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".claude"))
	}
	return out
}

// VerifyConversationInDir confirms cfgDir holds the instance's conversation
// file (#1571 post-switch verification: never report a successful switch when
// the target dir cannot actually resume the conversation). wantSize > 0
// additionally requires the file size to be within 1% of wantSize, so a
// poisoned stub at the resume path fails loudly instead of masquerading as
// the migrated conversation.
func VerifyConversationInDir(inst *Instance, cfgDir string, wantSize int64) error {
	if inst == nil || inst.ClaudeSessionID == "" {
		return fmt.Errorf("no claude session id to verify")
	}
	dir := ExpandPath(strings.TrimSpace(cfgDir))
	if dir == "" {
		return fmt.Errorf("empty config dir")
	}
	path := filepath.Join(dir, "projects", ConvertToClaudeDirName(inst.ProjectPath), inst.ClaudeSessionID+".jsonl")
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("conversation missing from target: %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("conversation path is not a regular file: %s", path)
	}
	if wantSize > 0 {
		diff := info.Size() - wantSize
		if diff < 0 {
			diff = -diff
		}
		tolerance := wantSize / 100
		if diff > tolerance {
			return fmt.Errorf("conversation size mismatch in target: %s has %d bytes, expected ~%d", path, info.Size(), wantSize)
		}
	}
	return nil
}
