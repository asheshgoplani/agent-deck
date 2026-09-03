package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// claudeSessionMeta is the subset of ~/.claude/sessions/<PID>.json that
// agent-deck reads for title sync (issue #572) and, as of #2089, for the
// Claude messaging-socket send transport.
type claudeSessionMeta struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	// NameSource distinguishes a user rename ("user", or absent on older Claude)
	// from a name Claude Code 2.1.19x auto-derives from the cwd folder
	// ("derived"). Only user renames are real intent; see ClaudeSessionNameIn.
	NameSource string `json:"nameSource"`
	UpdatedAt  *int64 `json:"updatedAt"` // unix ms; nil when absent

	// The fields below are read for ClaudeSessionRecordIn (#2089) and are not
	// used by the title-sync path.
	Pid                 int    `json:"pid"`
	ProcStart           string `json:"procStart"`   // `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>`
	ProcStartFt         string `json:"procStartFt"` // Linux-side twin of ProcStart
	PeerProtocol        int    `json:"peerProtocol"`
	MessagingSocketPath string `json:"messagingSocketPath"`
}

// freshestClaudeSessionMetaIn scans claudeDir/sessions/*.json and returns the
// entry whose sessionId matches sessionID with the highest updatedAt
// (falling back to file mtime). ok is false when there's no match or the
// sessions dir is unreadable.
//
// The files are per-PID, so a resumed session can match several entries — the
// live process plus stale files left by earlier runs. The freshest entry is
// authoritative, even when its name is empty: returning a stale file's old
// name would re-sync a title the user has since changed or cleared.
//
// claudeDir is an explicit parameter so tests can point it at a temp dir.
func freshestClaudeSessionMetaIn(claudeDir, sessionID string) (claudeSessionMeta, bool) {
	claudeDir = strings.TrimSpace(claudeDir)
	sessionID = strings.TrimSpace(sessionID)
	if claudeDir == "" || sessionID == "" {
		return claudeSessionMeta{}, false
	}
	entries, err := os.ReadDir(filepath.Join(claudeDir, "sessions"))
	if err != nil {
		return claudeSessionMeta{}, false
	}
	var best claudeSessionMeta
	bestTime := int64(-1)
	found := false
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
			best = meta
			found = true
		}
	}
	return best, found
}

// ClaudeSessionNameIn scans claudeDir/sessions/*.json and returns the trimmed
// `name` of the entry whose sessionId matches. Empty string when there's no
// match, no name, or the sessions dir is unreadable.
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
	meta, ok := freshestClaudeSessionMetaIn(claudeDir, sessionID)
	if !ok {
		return ""
	}
	// A folder-derived name is not user intent: treat it as unnamed so it
	// neither syncs nor lets a stale named entry win.
	if meta.NameSource == "derived" {
		return ""
	}
	return strings.TrimSpace(meta.Name)
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

// ClaudeSessionRecord is the subset of ~/.claude/sessions/<pid>.json
// agent-deck needs to deliver a message over Claude Code's messaging socket
// (#2089). It is a type alias for send.ClaudeSocketRecord, not a separate
// struct: internal/session already imports internal/send (instance.go, for
// the #1777 Enter-attribution machinery), so defining the record once in
// internal/send and aliasing it here avoids both an import cycle and a
// field-by-field mirror that can silently drift out of sync. See
// internal/send/claudesocket.go for the transport that consumes it.
type ClaudeSessionRecord = send.ClaudeSocketRecord

// ClaudeSessionRecordIn scans claudeDir/sessions/*.json and returns the
// freshest record whose sessionId matches sessionID. ok is false when
// there's no match or the sessions dir is unreadable — the selector, not
// this reader, decides what to do about a record with an empty
// MessagingSocketPath or a PeerProtocol below the minimum.
func ClaudeSessionRecordIn(claudeDir, sessionID string) (ClaudeSessionRecord, bool) {
	meta, ok := freshestClaudeSessionMetaIn(claudeDir, sessionID)
	if !ok {
		return ClaudeSessionRecord{}, false
	}
	return ClaudeSessionRecord{
		Pid:                 meta.Pid,
		SessionID:           meta.SessionID,
		ProcStart:           meta.ProcStart,
		ProcStartFt:         meta.ProcStartFt,
		PeerProtocol:        meta.PeerProtocol,
		MessagingSocketPath: meta.MessagingSocketPath,
	}, true
}

// ClaudeSessionRecordFor resolves the user's ~/.claude and returns the
// Claude session record for sessionID. Mirrors ClaudeSessionName.
func ClaudeSessionRecordFor(sessionID string) (ClaudeSessionRecord, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeSessionRecord{}, false
	}
	return ClaudeSessionRecordIn(filepath.Join(home, ".claude"), sessionID)
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
	i.SetTitleThreadSafe(name)
	i.SyncTmuxDisplayName()
	if tmuxSess := i.GetTmuxSession(); tmuxSess != nil && tmuxSess.Name != "" {
		_ = tmux.WriteBadgeUpdate(tmuxSess.Name, name)
	}
	return name, true
}
