package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// claudeSessionMeta is the subset of ~/.claude/sessions/<PID>.json that
// agent-deck reads for title sync (issue #572).
type claudeSessionMeta struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	// NameSource distinguishes a user rename ("user", or absent on older Claude)
	// from a name Claude Code 2.1.19x auto-derives from the cwd folder
	// ("derived"). Only user renames are real intent; see ClaudeSessionNameIn.
	NameSource string `json:"nameSource"`
	UpdatedAt  *int64 `json:"updatedAt"` // unix ms; nil when absent
}

// ClaudeSessionNameIn scans claudeDir/sessions/*.json and returns the trimmed
// `name` of the entry whose sessionId matches. Empty string when there's no
// match, no name, or the sessions dir is unreadable.
//
// The files are per-PID, so a resumed session can match several entries — the
// live process plus stale files left by earlier runs. The freshest entry (by
// updatedAt, falling back to file mtime) is authoritative, even when its name
// is empty: returning a stale file's old name would re-sync a title the user
// has since changed or cleared.
//
// Issue #572: Claude Code writes per-process metadata here when the user starts
// with `claude --name X` or runs `/rename X` mid-session. claudeDir is an
// explicit parameter so tests can point it at a temp dir.
//
// Claude Code 2.1.19x also auto-derives a name from the cwd folder and stamps
// nameSource="derived". That is not a user rename, so a derived name is treated
// as no name at all — including on the freshest entry, where it suppresses any
// stale user name (mirrors the freshest-unnamed rule). A name with no
// nameSource (older Claude) is always a user rename, so it is honored.
func ClaudeSessionNameIn(claudeDir, sessionID string) string {
	claudeDir = strings.TrimSpace(claudeDir)
	sessionID = strings.TrimSpace(sessionID)
	if claudeDir == "" || sessionID == "" {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(claudeDir, "sessions"))
	if err != nil {
		return ""
	}
	bestName := ""
	bestTime := int64(-1)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(claudeDir, "sessions", entry.Name()))
		if err != nil {
			continue
		}
		var meta claudeSessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.SessionID != sessionID {
			continue
		}
		var ts int64
		if meta.UpdatedAt != nil {
			ts = *meta.UpdatedAt
		} else if info, err := entry.Info(); err == nil {
			ts = info.ModTime().UnixMilli()
		}
		if ts > bestTime {
			bestTime = ts
			// A folder-derived name is not user intent: treat it as unnamed so it
			// neither syncs nor lets a stale named entry win.
			if meta.NameSource == "derived" {
				bestName = ""
			} else {
				bestName = strings.TrimSpace(meta.Name)
			}
		}
	}
	return bestName
}

// ClaudeSessionName resolves the user's ~/.claude and returns the Claude
// session name for sessionID. Empty string on any error or no match.
func ClaudeSessionName(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return ClaudeSessionNameIn(filepath.Join(home, ".claude"), sessionID)
}

// ResolveTitleFromClaude is the pure decision half of ReconcileTitleFromClaude:
// it answers "does Claude's session name warrant a rename" without mutating i
// or touching tmux. Honors the same sync_title switch and TitleLocked flag.
//
// Split out for the hook-triggered sync (cmd/agent-deck/hook_name_sync.go),
// which persists via a conditional UPDATE ... WHERE title_locked = 0 that can
// legitimately no-op if a user rename landed and locked the title first. The
// combined ReconcileTitleFromClaude fires tmux/badge side effects unconditionally
// on a decision to rename, before the caller's persistence attempt is even
// known to have succeeded — a hook whose write gets rejected would still have
// already overwritten the live tmux window title and iTerm badge with Claude's
// name, leaving the terminal chrome out of sync with the correctly-preserved
// stored title. Callers in that situation should call this instead and only
// run the equivalent of ReconcileTitleFromClaude's side effects once their own
// write is confirmed applied.
func (i *Instance) ResolveTitleFromClaude(sessionID string) (string, bool) {
	if i == nil || i.TitleLocked {
		return "", false
	}
	if cfg, err := LoadUserConfig(); err == nil && cfg != nil && !cfg.GetSyncTitle() {
		return "", false
	}
	name := ClaudeSessionName(sessionID)
	if name == "" || name == i.Title {
		return "", false
	}
	return name, true
}

// ReconcileTitleFromClaude refreshes i.Title from the agent's current Claude
// session name. It is the shared core behind both the hook-event sync (#572)
// and the on-attach reconcile (#1114 follow-up): Claude's /rename fires no
// agent-deck hook, so an idle session's title and iTerm2 badge stay stale until
// the next turn boundary — reconciling on attach makes detach/reattach a
// reliable manual refresh.
//
// Honors the global sync_title switch and the per-session TitleLocked flag (so
// conductor titles like "SCRUM-351" survive Claude's own /rename). On a real
// change it mutates the in-memory instance (Title + tmux display name) and
// drops the iTerm2 badge-update signal so the attach-side WatchBadgeUpdates
// catch-up re-emits the fresh name instead of clobbering it with the old one.
//
// Returns the new name and true iff the title changed; the CALLER is
// responsible for persisting the instance to storage. The on-attach caller
// (internal/ui/home.go) saves under the same in-process lock it just read
// TitleLocked from, so applying side effects immediately here is safe for
// that caller; the hook-triggered sync is NOT that caller — see
// ResolveTitleFromClaude.
func (i *Instance) ReconcileTitleFromClaude(sessionID string) (string, bool) {
	name, ok := i.ResolveTitleFromClaude(sessionID)
	if !ok {
		return "", false
	}
	i.Title = name
	i.SyncTmuxDisplayName()
	if tmuxSess := i.GetTmuxSession(); tmuxSess != nil && tmuxSess.Name != "" {
		_ = tmux.WriteBadgeUpdate(tmuxSess.Name, name)
	}
	return name, true
}
