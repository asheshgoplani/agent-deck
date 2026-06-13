package main

import (
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// findClaudeSessionName scans claudeDir/sessions/*.json and returns the `name`
// field of the entry whose `sessionId` matches. Empty string if no match, no
// name, or the sessions dir doesn't exist.
//
// Thin wrapper over session.ClaudeSessionNameIn so the hook path and the
// on-attach reconcile (internal/session) share one scanner implementation.
func findClaudeSessionName(claudeDir, sessionID string) string {
	return session.ClaudeSessionNameIn(claudeDir, sessionID)
}

// applyClaudeTitleSync looks up the Claude session name for sessionID and, if
// non-empty and different from the current agent-deck session title for
// instanceID, updates the title in storage.
//
// No-op (and silent) when:
//   - instance can't be resolved across profiles
//   - Claude session file doesn't exist or has no name
//   - the stored title already matches
//   - sync_title is disabled, or the instance is TitleLocked (both enforced by
//     Instance.ReconcileTitleFromClaude)
//
// Scans profiles in order so the first match wins. This is the right shape for
// hook_handler which doesn't know which profile owns the session — the instance
// ID is globally unique.
func applyClaudeTitleSync(instanceID, sessionID string) {
	if instanceID == "" || sessionID == "" {
		return
	}

	// Global, tool-agnostic switch (config: sync_title = false). Short-circuit
	// before touching storage; ReconcileTitleFromClaude enforces it too, so
	// this is purely to skip the profile scan when sync is off.
	if cfg, err := session.LoadUserConfig(); err == nil && cfg != nil && !cfg.GetSyncTitle() {
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	// Cheap pre-check: if Claude has no name for this session at all, skip the
	// profile scan entirely (the common case for sessions started without
	// --name). ReconcileTitleFromClaude re-reads it authoritatively below.
	if findClaudeSessionName(filepath.Join(home, ".claude"), sessionID) == "" {
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

		// Instance IDs are globally unique: once found, this profile owns the
		// session — act and stop, never fall through to another profile.
		newName, changed := target.ReconcileTitleFromClaude(sessionID)
		if changed {
			target.SetAutoName(false) // Claude/user-chosen name replaces the auto handle
			groupTree := session.NewGroupTreeWithGroups(instances, groups)
			_ = storage.SaveWithGroups(instances, groupTree)
		}
		_ = storage.Close()

		if changed {
			// #1114: ReconcileTitleFromClaude already wrote the badge-update
			// file the attached process watches (the path that works without a
			// controlling tty). Also attempt the direct via-tty emit for the
			// rare hook that DOES own a tty — silent no-op otherwise.
			tmux.EmitITermBadgeViaTty(newName, session.GetTerminalSettings().GetITermBadge())
		}
		return
	}
}
