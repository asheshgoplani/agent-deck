package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
	"github.com/asheshgoplani/agent-deck/internal/shellwords"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const (
	toolDataAntigravitySessionIDKey  = "antigravity_session_id"
	toolDataAntigravityDetectedAtKey = "antigravity_detected_at"

	antigravityScanMinInterval = 2 * time.Second
	antigravityStartupTimeSkew = 5 * time.Second
)

var antigravitySessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

// WriteAntigravitySessionIDToToolData merges the Antigravity CLI conversation
// id and detection timestamp into the tool_data extras zone.
//
// An empty sessionID with intentionalClear=false omits the keys so
// MergeToolDataExtras carries forward any stored binding written by another
// process. An intentional clear writes explicit empty/zero values so sticky
// merge clears the stored binding.
func WriteAntigravitySessionIDToToolData(td json.RawMessage, sessionID string, detectedAt time.Time, intentionalClear bool) json.RawMessage {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" && !intentionalClear {
		return td
	}
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if sessionID == "" {
		m[toolDataAntigravitySessionIDKey] = json.RawMessage(`""`)
		m[toolDataAntigravityDetectedAtKey] = json.RawMessage(`0`)
	} else {
		rawID, err := json.Marshal(sessionID)
		if err != nil {
			return td
		}
		m[toolDataAntigravitySessionIDKey] = rawID
		if !detectedAt.IsZero() {
			if rawAt, err := json.Marshal(detectedAt.Unix()); err == nil {
				m[toolDataAntigravityDetectedAtKey] = rawAt
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

func stripAntigravityClearFromToolData(td json.RawMessage) json.RawMessage {
	if len(td) == 0 {
		return td
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(td, &m); err != nil {
		return td
	}
	delete(m, toolDataAntigravitySessionIDKey)
	delete(m, toolDataAntigravityDetectedAtKey)
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadAntigravitySessionIDFromToolData returns the recorded Antigravity CLI
// conversation id, or "" if none is stored.
func ReadAntigravitySessionIDFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		return ""
	}
	var id string
	if err := json.Unmarshal(m[toolDataAntigravitySessionIDKey], &id); err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

// ReadAntigravityDetectedAtFromToolData returns when the Antigravity
// conversation id was recorded, or zero.
func ReadAntigravityDetectedAtFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		At int64 `json:"antigravity_detected_at"`
	}
	if json.Unmarshal(td, &blob) != nil || blob.At == 0 {
		return time.Time{}
	}
	return time.Unix(blob.At, 0).UTC()
}

func (i *Instance) persistAntigravitySessionBinding(sessionID string, detectedAt time.Time) {
	err := i.withOwningStateDB(func(db *statedb.StateDB) error {
		return db.WriteAntigravitySessionBinding(i.ID, sessionID, detectedAt)
	})
	if errors.Is(err, errNoStateDB) {
		return
	}
	if err != nil {
		sessionLog.Warn("antigravity_session_bind_persist_failed",
			slog.String("instance_id", i.ID),
			slog.String("new_id", sessionID),
			slog.String("error", err.Error()))
	}
}

func splitAntigravityTokens(s string) []string {
	if tokens, ok := shellwords.Split(s); ok {
		return tokens
	}
	return strings.Fields(s)
}

// extractAntigravityConversationID returns an explicit --conversation <id>
// value from command or wrapper, if present and shell-safe.
func extractAntigravityConversationID(command, wrapper string) string {
	for _, raw := range []string{command, wrapper} {
		tokens := splitAntigravityTokens(raw)
		for idx := 0; idx < len(tokens); idx++ {
			tok := tokens[idx]
			var candidate string
			switch {
			case tok == "--conversation" && idx+1 < len(tokens):
				candidate = tokens[idx+1]
				idx++
			case strings.HasPrefix(tok, "--conversation="):
				candidate = strings.TrimPrefix(tok, "--conversation=")
			}
			candidate = strings.TrimSpace(candidate)
			if candidate != "" && antigravitySessionIDPattern.MatchString(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func antigravityHasSessionSelector(command, wrapper string) bool {
	for _, raw := range []string{command, wrapper} {
		for _, tok := range splitAntigravityTokens(raw) {
			if tok == "--conversation" || strings.HasPrefix(tok, "--conversation=") || tok == "-c" || tok == "--continue" {
				return true
			}
		}
	}
	return false
}

func antigravityHasAddDir(command, dir string) bool {
	want := normalizePath(dir)
	if want == "" {
		return false
	}
	tokens := splitAntigravityTokens(command)
	for idx := 0; idx < len(tokens); idx++ {
		tok := tokens[idx]
		var candidate string
		switch {
		case tok == "--add-dir" && idx+1 < len(tokens):
			candidate = tokens[idx+1]
			idx++
		case strings.HasPrefix(tok, "--add-dir="):
			candidate = strings.TrimPrefix(tok, "--add-dir=")
		}
		if candidate != "" && normalizePath(candidate) == want {
			return true
		}
	}
	return false
}

// stripAntigravitySessionFlags removes conversation-selection and one-shot
// prompt-interactive flags from a base Antigravity command while preserving
// unrelated user flags (such as --mode, --model, --effort, --sandbox).
func stripAntigravitySessionFlags(command string) string {
	tokens := splitAntigravityTokens(command)
	if len(tokens) == 0 {
		return ""
	}
	kept := make([]string, 0, len(tokens))
	for idx := 0; idx < len(tokens); idx++ {
		tok := tokens[idx]
		switch {
		case tok == "--conversation" || tok == "-i" || tok == "--prompt-interactive":
			idx++ // skip paired value
			continue
		case strings.HasPrefix(tok, "--conversation=") || strings.HasPrefix(tok, "--prompt-interactive="):
			continue
		case tok == "-c" || tok == "--continue":
			continue
		default:
			kept = append(kept, quoteAntigravityArg(tok))
		}
	}
	return strings.Join(kept, " ")
}

// stripAntigravityConversationFromWrapper removes a parent's --conversation
// selector from an auto-wrapped `{command} ...` wrapper so a fork does not
// stay pinned to its parent's conversation across future restarts. If only
// `{command}` remains, it returns "".
func stripAntigravityConversationFromWrapper(wrapper string) string {
	trimmed := strings.TrimSpace(wrapper)
	if trimmed == "" {
		return ""
	}
	tokens := splitAntigravityTokens(trimmed)
	if len(tokens) == 0 || tokens[0] != "{command}" {
		return trimmed
	}
	kept := make([]string, 0, len(tokens))
	for idx := 0; idx < len(tokens); idx++ {
		tok := tokens[idx]
		switch {
		case tok == "--conversation" || tok == "-i" || tok == "--prompt-interactive":
			idx++
			continue
		case strings.HasPrefix(tok, "--conversation=") || strings.HasPrefix(tok, "--prompt-interactive="):
			continue
		case tok == "-c" || tok == "--continue":
			continue
		default:
			if tok == "{command}" {
				kept = append(kept, tok)
			} else {
				kept = append(kept, quoteAntigravityArg(tok))
			}
		}
	}
	if len(kept) == 1 && kept[0] == "{command}" {
		return ""
	}
	return strings.Join(kept, " ")
}

func quoteAntigravityArg(tok string) string {
	if tok == "" {
		return "''"
	}
	if antigravitySessionIDPattern.MatchString(tok) || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "/") {
		if !strings.ContainsAny(tok, " \t\n'\"\\$`!;|&<>()[]{}") {
			return tok
		}
	}
	return shellescape.Quote(tok)
}

// buildAntigravityResumeCommand returns the normal launch/restart command for
// an Antigravity session, appending --conversation <id> when a conversation ID
// is bound and no explicit session selector or custom wrapper is already set.
func (i *Instance) buildAntigravityResumeCommand() string {
	command := i.antigravityCommand()
	i.mu.RLock()
	sessionID := strings.TrimSpace(i.AntigravitySessionID)
	wrapper := strings.TrimSpace(i.Wrapper)
	i.mu.RUnlock()
	if sessionID == "" || wrapper != "" || antigravityHasSessionSelector(command, wrapper) {
		return command
	}
	normalized, err := normalizeToolSessionID(FieldAntigravitySessionID, sessionID)
	if err != nil || normalized == "" {
		return command
	}
	return command + " --conversation " + shellescape.Quote(normalized)
}

func antigravityDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return sandboxguard.MustHome("Antigravity CLI data dir (~/.gemini/antigravity-cli)", filepath.Join(home, ".gemini", "antigravity-cli"))
}

type antigravityConversationMetadataFile struct {
	Conversations map[string]antigravityConversationEntry `json:"conversations"`
}

type antigravityConversationEntry struct {
	Summary struct {
		ID            string   `json:"ID"`
		UpdatedAt     string   `json:"UpdatedAt"`
		WorkspaceURIs []string `json:"WorkspaceURIs"`
		Internal      bool     `json:"Internal"`
	} `json:"summary"`
	IsInternal       bool   `json:"is_internal"`
	LastModifiedTime string `json:"last_modified_time"`
}

func antigravityWorkspaceURIToPath(rawURI string) string {
	rawURI = strings.TrimSpace(rawURI)
	if rawURI == "" {
		return ""
	}
	if strings.HasPrefix(rawURI, "file://") {
		if parsed, err := url.Parse(rawURI); err == nil && parsed.Path != "" {
			return normalizePath(parsed.Path)
		}
		return normalizePath(strings.TrimPrefix(rawURI, "file://"))
	}
	return normalizePath(rawURI)
}

func parseAntigravityTimestamp(values ...string) time.Time {
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return ts
		}
		if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func antigravityConversationExistsInDir(dataDir, sessionID string) bool {
	if dataDir == "" || sessionID == "" {
		return false
	}
	if info, err := os.Stat(filepath.Join(dataDir, "conversations", sessionID+".db")); err == nil && !info.IsDir() {
		return true
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "cache", "conversation_metadata.json")); err == nil {
		var meta antigravityConversationMetadataFile
		if json.Unmarshal(data, &meta) == nil {
			if entry, ok := meta.Conversations[sessionID]; ok && !entry.IsInternal && !entry.Summary.Internal {
				return true
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "cache", "last_conversations.json")); err == nil {
		var last map[string]string
		if json.Unmarshal(data, &last) == nil {
			for _, id := range last {
				if strings.TrimSpace(id) == sessionID {
					return true
				}
			}
		}
	}
	return false
}

func antigravityConversationExistsOnDisk(sessionID string) bool {
	return antigravityConversationExistsInDir(antigravityDataDir(), sessionID)
}

func antigravityWorkspaceMatches(uriList []string, wantDirs []string) bool {
	for _, rawURI := range uriList {
		norm := antigravityWorkspaceURIToPath(rawURI)
		if norm == "" {
			continue
		}
		for _, want := range wantDirs {
			if want != "" && norm == want {
				return true
			}
		}
	}
	return false
}

// discoverAntigravityConversationFromDisk finds the Antigravity conversation ID
// matching workDir/projectPath from Antigravity CLI's local cache files.
//
// Rules:
//   - Skips internal/subagent conversations.
//   - Never adopts excludeID (prevents a newly forked child from binding to its
//     parent's conversation ID before /fork finishes).
//   - Preserves currentID when it still exists (prevents sibling Antigravity
//     sessions in the same directory from stealing each other's bindings).
//   - When unbound and startedAt > 0, only adopts conversations updated on or
//     after startedAt - antigravityStartupTimeSkew.
func discoverAntigravityConversationFromDisk(dataDir, workDir, projectPath, currentID, excludeID string, startedAt int64) string {
	if dataDir == "" {
		return ""
	}
	wantDirs := make([]string, 0, 2)
	if norm := normalizePath(workDir); norm != "" {
		wantDirs = append(wantDirs, norm)
	}
	if norm := normalizePath(projectPath); norm != "" && (len(wantDirs) == 0 || wantDirs[0] != norm) {
		wantDirs = append(wantDirs, norm)
	}
	if len(wantDirs) == 0 {
		return ""
	}

	var threshold time.Time
	if startedAt > 0 {
		threshold = time.UnixMilli(startedAt).Add(-antigravityStartupTimeSkew)
	}

	if data, err := os.ReadFile(filepath.Join(dataDir, "cache", "conversation_metadata.json")); err == nil {
		var meta antigravityConversationMetadataFile
		if json.Unmarshal(data, &meta) == nil && len(meta.Conversations) > 0 {
			var bestID string
			var bestTime time.Time
			currentFound := false

			for mapID, entry := range meta.Conversations {
				if entry.IsInternal || entry.Summary.Internal {
					continue
				}
				id := strings.TrimSpace(entry.Summary.ID)
				if id == "" {
					id = strings.TrimSpace(mapID)
				}
				if id == "" || !antigravitySessionIDPattern.MatchString(id) {
					continue
				}
				if excludeID != "" && id == excludeID {
					continue
				}
				if !antigravityWorkspaceMatches(entry.Summary.WorkspaceURIs, wantDirs) {
					continue
				}
				if currentID != "" && id == currentID {
					currentFound = true
					continue
				}
				ts := parseAntigravityTimestamp(entry.Summary.UpdatedAt, entry.LastModifiedTime)
				if currentID == "" && !threshold.IsZero() && !ts.IsZero() && ts.Before(threshold) {
					continue
				}
				if bestID == "" || ts.After(bestTime) {
					bestID = id
					bestTime = ts
				}
			}

			if currentID != "" && currentFound {
				return currentID
			}
			if bestID != "" {
				return bestID
			}
		}
	}

	// Fallback: cache/last_conversations.json maps workspace path -> conversation ID.
	lastPath := filepath.Join(dataDir, "cache", "last_conversations.json")
	if data, err := os.ReadFile(lastPath); err == nil {
		var last map[string]string
		if json.Unmarshal(data, &last) == nil {
			for dir, rawID := range last {
				id := strings.TrimSpace(rawID)
				if id == "" || !antigravitySessionIDPattern.MatchString(id) {
					continue
				}
				if excludeID != "" && id == excludeID {
					continue
				}
				normDir := normalizePath(dir)
				matched := false
				for _, want := range wantDirs {
					if normDir == want {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				if currentID != "" && id == currentID {
					return currentID
				}
				if currentID == "" && !threshold.IsZero() {
					var modTime time.Time
					if dbInfo, err := os.Stat(filepath.Join(dataDir, "conversations", id+".db")); err == nil {
						modTime = dbInfo.ModTime()
					} else if fileInfo, err := os.Stat(lastPath); err == nil {
						modTime = fileInfo.ModTime()
					}
					if !modTime.IsZero() && modTime.Before(threshold) {
						continue
					}
				}
				return id
			}
		}
	}

	if currentID != "" && antigravityConversationExistsInDir(dataDir, currentID) {
		return currentID
	}
	return ""
}

// effectiveAntigravitySessionID returns the session's bound Antigravity
// conversation ID, falling back to an explicit --conversation flag in the
// configured command or wrapper.
func (i *Instance) effectiveAntigravitySessionID() string {
	i.mu.RLock()
	bound := strings.TrimSpace(i.AntigravitySessionID)
	command := i.Command
	wrapper := i.Wrapper
	i.mu.RUnlock()
	if bound != "" {
		return bound
	}
	return extractAntigravityConversationID(command, wrapper)
}

// SyncAntigravitySessionID resolves and binds the Antigravity conversation ID
// from explicit command flags, tmux environment, or local disk metadata.
func (i *Instance) SyncAntigravitySessionID() string {
	if i.Tool != "antigravity" {
		return ""
	}

	i.mu.RLock()
	currentID := strings.TrimSpace(i.AntigravitySessionID)
	command := i.Command
	wrapper := i.Wrapper
	tmuxSession := i.tmuxSession
	excludeID := strings.TrimSpace(i.antigravityForkParentID)
	startedAt := i.AntigravityStartedAt
	if startedAt <= 0 && !i.LastStartedAt.IsZero() {
		startedAt = i.LastStartedAt.UnixMilli()
	}
	sshHost := i.SSHHost
	sandboxed := i.IsSandboxed()
	workDir := i.EffectiveWorkingDir()
	projectPath := i.ProjectPath
	i.mu.RUnlock()

	var candidate string
	if currentID == "" {
		if explicit := extractAntigravityConversationID(command, wrapper); explicit != "" && explicit != excludeID {
			candidate = explicit
		}
	}
	if candidate == "" && currentID == "" && tmuxSession != nil {
		if envID, err := tmuxSession.GetEnvironment("ANTIGRAVITY_SESSION_ID"); err == nil {
			envID = strings.TrimSpace(envID)
			if envID != "" && envID != excludeID && antigravitySessionIDPattern.MatchString(envID) {
				candidate = envID
			}
		}
	}
	if candidate == "" && sshHost == "" && !sandboxed {
		candidate = discoverAntigravityConversationFromDisk(antigravityDataDir(), workDir, projectPath, currentID, excludeID, startedAt)
	}
	if candidate == "" {
		return currentID
	}
	if candidate == currentID {
		return currentID
	}

	now := time.Now()
	i.mu.Lock()
	i.AntigravitySessionID = candidate
	i.AntigravityDetectedAt = now
	i.antigravitySessionCleared = false
	if excludeID != "" && candidate != excludeID {
		i.antigravityForkParentID = ""
	}
	tmuxSess := i.tmuxSession
	i.mu.Unlock()

	if tmuxSess != nil {
		if err := tmuxSess.SetEnvironment("ANTIGRAVITY_SESSION_ID", candidate); err != nil {
			sessionLog.Warn("antigravity_set_env_failed", slog.String("error", err.Error()))
		}
	}
	i.persistAntigravitySessionBinding(candidate, now)
	return candidate
}

// UpdateAntigravitySession refreshes the bound Antigravity conversation ID,
// rate-limited to avoid unnecessary filesystem reads on render/status ticks.
func (i *Instance) UpdateAntigravitySession() {
	if i.Tool != "antigravity" {
		return
	}
	i.mu.Lock()
	if !i.lastAntigravityScanAt.IsZero() && time.Since(i.lastAntigravityScanAt) < antigravityScanMinInterval {
		i.mu.Unlock()
		return
	}
	i.lastAntigravityScanAt = time.Now()
	i.mu.Unlock()

	i.SyncAntigravitySessionID()
}

func (i *Instance) postStartSyncAntigravity(maxWait time.Duration) {
	if i.Tool != "antigravity" {
		return
	}
	if id := i.SyncAntigravitySessionID(); id != "" {
		return
	}
	if maxWait <= 0 {
		return
	}
	if maxWait > 2*time.Second {
		maxWait = 2 * time.Second
	}
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if id := i.SyncAntigravitySessionID(); id != "" {
			return
		}
	}
}

// CanForkAntigravity reports whether this Antigravity session currently has a
// resolvable conversation ID to fork from.
func (i *Instance) CanForkAntigravity() bool {
	if i.Tool != "antigravity" {
		return false
	}
	sid := i.effectiveAntigravitySessionID()
	if sid == "" {
		return false
	}
	normalized, err := normalizeToolSessionID(FieldAntigravitySessionID, sid)
	if err != nil || normalized == "" || normalized != sid {
		return false
	}
	if i.SSHHost != "" || i.IsSandboxed() {
		return true
	}
	i.mu.RLock()
	detectedAt := i.AntigravityDetectedAt
	explicit := extractAntigravityConversationID(i.Command, i.Wrapper) != ""
	i.mu.RUnlock()
	if explicit || (!detectedAt.IsZero() && time.Since(detectedAt) < 5*time.Minute) {
		return true
	}
	return antigravityConversationExistsOnDisk(normalized)
}

// buildAntigravityForkCommandForTarget builds the one-shot launch command for
// a forked Antigravity CLI session.
//
// Open the parent conversation first. Start or StartWithMessage delivers the
// /fork slash command through tmux once the composer is ready.
func (i *Instance) buildAntigravityForkCommandForTarget(target *Instance, baseCommand string) (string, error) {
	if target == nil {
		return "", fmt.Errorf("cannot build Antigravity fork command: target instance is nil")
	}
	if !i.CanForkAntigravity() {
		return "", fmt.Errorf("cannot fork: no resumable Antigravity conversation")
	}
	parentID, err := normalizeToolSessionID(FieldAntigravitySessionID, i.effectiveAntigravitySessionID())
	if err != nil || parentID == "" {
		return "", fmt.Errorf("cannot fork: invalid Antigravity conversation id")
	}

	resolvedBase := strings.TrimSpace(baseCommand)
	if resolvedBase == "" {
		resolvedBase = GetToolCommand("antigravity")
	} else {
		fields := strings.Fields(resolvedBase)
		if len(fields) > 0 && strings.EqualFold(fields[0], "antigravity") {
			resolvedBase = GetToolCommand("antigravity") + resolvedBase[len(fields[0]):]
		}
	}
	cleanCmd := stripAntigravitySessionFlags(resolvedBase)
	if cleanCmd == "" {
		cleanCmd = GetToolCommand("antigravity")
	}

	parts := []string{cleanCmd, "--conversation", shellescape.Quote(parentID)}
	if target.ProjectPath != "" && i.ProjectPath != "" &&
		normalizePath(target.ProjectPath) != normalizePath(i.ProjectPath) &&
		!antigravityHasAddDir(cleanCmd, target.ProjectPath) {
		parts = append(parts, "--add-dir", shellescape.Quote(target.ProjectPath))
	}

	cmd := strings.Join(parts, " ")
	if strings.TrimSpace(target.Wrapper) == "" {
		if envPrefix := target.buildEnvSourceCommand(); envPrefix != "" {
			cmd = envPrefix + cmd
		}
	}
	return cmd, nil
}

// CreateForkedAntigravityInstanceWithOptions creates a forked Antigravity CLI
// instance. Mirrors CreateForkedCodexInstanceWithOptions /
// CreateForkedPiInstanceWithOptions: opts is the shared worktree carrier (only
// WorkDir and Worktree* fields are consumed); launch is deferred via
// ForkStartCommand.
func (i *Instance) CreateForkedAntigravityInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
) (*Instance, string, error) {
	if !i.CanForkAntigravity() {
		return nil, "", fmt.Errorf("cannot fork: no resumable Antigravity conversation")
	}
	parentID := i.effectiveAntigravitySessionID()

	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}

	forked := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.tmuxSession.SetGroupPath(forked.GroupPath)
	forked.Tool = "antigravity"
	forked.Wrapper = stripAntigravityConversationFromWrapper(i.Wrapper)

	baseCommand := stripAntigravitySessionFlags(i.antigravityCommand())
	if baseCommand == "" {
		baseCommand = GetToolCommand("antigravity")
	}
	forked.Command = baseCommand

	cmd, err := i.buildAntigravityForkCommandForTarget(forked, i.antigravityCommand())
	if err != nil {
		return nil, "", err
	}
	forked.ForkStartCommand = cmd
	forked.IsForkAwaitingStart = true
	forked.antigravityForkParentID = parentID

	if opts != nil && opts.WorktreePath != "" {
		forked.WorktreePath = opts.WorktreePath
		forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
		forked.WorktreeBranch = opts.WorktreeBranch
	}

	return forked, cmd, nil
}
