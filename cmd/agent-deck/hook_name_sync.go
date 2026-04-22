package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// claudeSessionMetaFile is the subset of ~/.claude/sessions/<PID>.json that
// agent-deck cares about for issue #572 title sync.
type claudeSessionMetaFile struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// findClaudeSessionName scans claudeDir/sessions/*.json and returns the
// `name` field of the entry whose `sessionId` matches. Empty string if no
// match, no name, or the sessions dir doesn't exist.
//
// Issue #572: Claude Code writes per-process metadata here when the user
// starts with `claude --name X` or runs `/rename X` mid-session.
func findClaudeSessionName(claudeDir, sessionID string) string {
	if claudeDir == "" || sessionID == "" {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(claudeDir, "sessions"))
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(claudeDir, "sessions", entry.Name()))
		if err != nil {
			continue
		}
		var meta claudeSessionMetaFile
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.SessionID == sessionID {
			return strings.TrimSpace(meta.Name)
		}
	}
	return ""
}

// applyClaudeTitleSync looks up the Claude session name for sessionID and,
// if non-empty and different from the current agent-deck session title for
// instanceID, updates the title in storage.
//
// No-op (and silent) when:
//   - instance can't be resolved across profiles
//   - Claude session file doesn't exist or has no name
//   - the stored title already matches
//
// Scans profiles in order so the first match wins. This is the right shape
// for hook_handler which doesn't know which profile owns the session — the
// instance ID is globally unique.
func applyClaudeTitleSync(instanceID, sessionID string) {
	if instanceID == "" || sessionID == "" {
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	name := findClaudeSessionName(filepath.Join(home, ".claude"), sessionID)
	if name == "" {
		return
	}

	profiles, err := session.ListProfiles()
	if err != nil || len(profiles) == 0 {
		p := os.Getenv("AGENTDECK_PROFILE")
		if p == "" {
			p = session.DefaultProfile
		}
		profiles = []string{p}
	}

	for _, profile := range profiles {
		storage, err := session.NewStorageWithProfile(profile)
		if err != nil {
			continue
		}
		instances, groups, err := storage.LoadWithGroups()
		if err != nil {
			_ = storage.Close()
			continue
		}
		var target *session.Instance
		for _, inst := range instances {
			if inst.ID == instanceID {
				target = inst
				break
			}
		}
		if target == nil {
			_ = storage.Close()
			continue
		}
		// #697: TitleLocked blocks Claude's session name from overwriting the
		// agent-deck title. Conductors rely on semantic titles (e.g.
		// "SCRUM-351") surviving Claude's own /rename.
		if target.TitleLocked {
			_ = storage.Close()
			return
		}
		if target.Title == name {
			_ = storage.Close()
			return
		}
		target.Title = name
		target.SyncTmuxDisplayName()
		groupTree := session.NewGroupTreeWithGroups(instances, groups)
		_ = storage.SaveWithGroups(instances, groupTree)
		_ = storage.Close()
		return
	}
}
