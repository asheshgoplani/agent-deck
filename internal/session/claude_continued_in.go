package session

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Issue #2683: pressing ← in Claude Code's agent view moves the conversation
// to the background supervisor under a NEW session id, and the old
// transcript ends with {"type":"continued-in","continuedInSessionId":"<new>"}.
// The instance stays bound to the old id, so a restart reopened the stalled
// pre-handoff copy. followClaudeContinuedIn walks that pointer chain before
// the resume command is built.

const (
	// maxContinuedInHops bounds the chain walk; a cycle or a long chain
	// stops at whatever was reached.
	maxContinuedInHops = 8
	// continuedInTailBytes is how much of a transcript's tail is read to find
	// its last record.
	continuedInTailBytes = 64 * 1024
)

var claudeSessionIDRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// followClaudeContinuedIn rebinds the instance to the newest successor of its
// own conversation. Only an id this instance owns (the #1815 identity guard)
// is followed, and only to a successor transcript in the same project
// directory, so the pointer can never pull in another project's conversation.
func (i *Instance) followClaudeContinuedIn() {
	if i == nil || !i.TranscriptIsResolvableLocally() {
		return
	}
	i.mu.RLock()
	start := i.ClaudeSessionID
	i.mu.RUnlock()
	if !validClaudeSessionIDForPath(start) || !i.resumeIdentityAllowed(start).Allow {
		return
	}
	configDir := GetClaudeConfigDirForInstance(i)
	root := claudeProjectsRoot(configDir)
	if root == "" {
		return
	}
	path, ok := confineClaudeTranscriptPath(root, claudeTranscriptPathIn(configDir, i, start))
	if !ok {
		return
	}
	dir := filepath.Dir(path)
	cur := start
	seen := map[string]bool{cur: true}
	for hop := 0; hop < maxContinuedInHops; hop++ {
		next := continuedInSuccessor(root, path)
		if next == "" || seen[next] || !validClaudeSessionIDForPath(next) {
			break
		}
		nextPath, ok := confineClaudeTranscriptPath(root, filepath.Join(dir, next+".jsonl"))
		// The successor must sit in the same project directory, also after
		// symlinks are resolved.
		if !ok || filepath.Dir(nextPath) != dir {
			break
		}
		if info, err := os.Stat(nextPath); err != nil || !info.Mode().IsRegular() {
			break
		}
		seen[next] = true
		cur, path = next, nextPath
	}
	if cur == start {
		return
	}

	// buildClaudeResumeCommand runs without i.mu, while UpdateStatus reads
	// claudeSupersededIDs and ClaudeSessionID under it: take the lock for the
	// rebind itself. The tmux and SQLite writes below stay outside it.
	i.mu.Lock()
	if i.ClaudeSessionID != start {
		// Something else rebound the session while the chain was walked.
		i.mu.Unlock()
		return
	}
	if i.claudeSupersededIDs == nil {
		i.claudeSupersededIDs = make(map[string]struct{}, len(seen))
	}
	for id := range seen {
		if id != cur {
			i.claudeSupersededIDs[id] = struct{}{}
		}
	}
	delete(i.claudeSupersededIDs, cur)
	i.ClaudeSessionID = cur
	i.markClaudeSessionIDVerified()
	i.ClaudeDetectedAt = time.Now()
	detectedAt := i.ClaudeDetectedAt
	i.mu.Unlock()

	i.publishClaudeContinuedInRebind(cur, detectedAt)
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID,
		Tool:       "claude",
		Action:     "rebind",
		Source:     "continued_in",
		OldID:      start,
		NewID:      cur,
		Reason:     "transcript_continued_in",
	})
	sessionLog.Info("resume: followed continued-in",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("old_session_id", logging.SanitizeValue(start)),
		slog.String("claude_session_id", logging.SanitizeValue(cur)))
}

// validClaudeSessionIDForPath reports whether id is a bare Claude session
// UUID that is safe to use as a transcript file name: no separators, no "..".
func validClaudeSessionIDForPath(id string) bool {
	return claudeSessionIDRE.MatchString(id) &&
		filepath.Base(id) == id &&
		!strings.ContainsAny(id, `/\`) &&
		!strings.Contains(id, "..")
}

// claudeProjectsRoot returns the resolved <configDir>/projects directory, or
// "" when it does not exist.
func claudeProjectsRoot(configDir string) string {
	if configDir == "" {
		return ""
	}
	root, err := filepath.EvalSymlinks(filepath.Join(configDir, "projects"))
	if err != nil {
		return ""
	}
	return filepath.Clean(root)
}

// confineClaudeTranscriptPath resolves p (symlinks included) and returns it
// only when it stays strictly under root, the resolved Claude projects
// directory. A transcript or project directory that links outside the root
// is refused.
func confineClaudeTranscriptPath(root, p string) (string, bool) {
	if root == "" || p == "" || !filepath.IsAbs(p) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", false
	}
	resolved = filepath.Clean(resolved)
	if strings.Contains(resolved, "..") || !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", false
	}
	return resolved, true
}

// claudeIDSuperseded reports whether id is a predecessor this instance already
// left behind by following a continued-in handoff.
func (i *Instance) claudeIDSuperseded(id string) bool {
	if id == "" || i.claudeSupersededIDs == nil {
		return false
	}
	_, ok := i.claudeSupersededIDs[id]
	return ok
}

// publishClaudeContinuedInRebind makes a continued-in rebind durable and
// visible, the same way bindClaudeSessionFromHook does for a hook rebind.
//
// The Restart path that reuses the existing pane (RespawnPane) never re-runs
// the start-time SetEnvironment, so without this the pane env keeps the old
// id: the respawned claude inherits it, and RefreshLiveSessionIDs later reads
// it back and reverts ClaudeSessionID to the stalled predecessor. The SQLite
// write keeps a fresh load (another process, the next CLI call, a reboot)
// from resurrecting the old id too.
func (i *Instance) publishClaudeContinuedInRebind(sessionID string, detectedAt time.Time) {
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", sessionID)
	}
	db := statedb.GetGlobal()
	if db == nil {
		return
	}
	if err := db.WriteClaudeSessionBinding(i.ID, sessionID, detectedAt); err != nil {
		sessionLog.Warn("claude_continued_in_persist_failed",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("new_id", logging.SanitizeValue(sessionID)),
			slog.String("error", logging.SanitizeValue(err.Error())))
		return
	}
	i.mu.Lock()
	i.linkedClaudeSessionID = sessionID
	i.mu.Unlock()
}

// continuedInSuccessor returns the continuedInSessionId of the transcript's
// LAST record when that record is a continued-in handoff, else "".
func continuedInSuccessor(root, path string) string {
	path = filepath.Clean(path)
	if root == "" || strings.Contains(path, "..") || !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := info.Size() - continuedInTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	tail = bytes.TrimRight(tail, "\r\n\t ")
	if idx := bytes.LastIndexByte(tail, '\n'); idx >= 0 {
		tail = tail[idx+1:]
	}
	var rec struct {
		Type                 string `json:"type"`
		ContinuedInSessionID string `json:"continuedInSessionId"`
	}
	if json.Unmarshal(tail, &rec) != nil || rec.Type != "continued-in" {
		return ""
	}
	if !claudeSessionIDRE.MatchString(rec.ContinuedInSessionID) {
		return ""
	}
	return rec.ContinuedInSessionID
}
