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
		instances, _, err := storage.LoadWithGroups()
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
		//
		// newName/changed reflect ResolveTitleFromClaude's decision against
		// THIS process's (possibly stale) in-memory snapshot — that's only a
		// fast local check. The actual persistence below is a single targeted
		// UPDATE ... WHERE title_locked = 0, so even if a user rename landed
		// and locked the title after this snapshot was loaded, the write is a
		// silent no-op instead of clobbering it (unlike the old whole-instance
		// SaveWithGroups round-trip, which had no such guard).
		//
		// Deliberately uses ResolveTitleFromClaude (pure decision), NOT
		// ReconcileTitleFromClaude (which also renames the live tmux window
		// and writes the badge-update file immediately). If we ran those side
		// effects on every "decided to rename" and then discovered the DB
		// write was rejected as locked, the tmux window title and iTerm badge
		// would already show Claude's name while the stored title correctly
		// stayed put — visible chrome out of sync with the persisted value.
		// So: decide, persist, and only apply the tmux/badge side effects once
		// persistence is confirmed.
		newName, changed := target.ResolveTitleFromClaude(sessionID)
		applied := false
		if changed {
			applied, _ = storage.UpdateTitleIfUnlocked(instanceID, newName)
		}
		_ = storage.Close()

		if applied {
			target.Title = newName
			target.SyncTmuxDisplayName()
			if tmuxSess := target.GetTmuxSession(); tmuxSess != nil && tmuxSess.Name != "" {
				_ = tmux.WriteBadgeUpdate(tmuxSess.Name, newName)
			}
			// #1114: WriteBadgeUpdate above wrote the file the attached
			// process watches (the path that works without a controlling
			// tty). Also attempt the direct via-tty emit for the rare hook
			// that DOES own a tty — silent no-op otherwise.
			tmux.EmitITermBadgeViaTty(newName, session.GetTerminalSettings().GetITermBadge())
		}
		return
	}
}
