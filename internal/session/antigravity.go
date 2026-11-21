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
	"unicode"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/shellwords"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const (
	toolDataAntigravitySessionIDKey  = "antigravity_session_id"
	toolDataAntigravityDetectedAtKey = "antigravity_detected_at"

	antigravityScanMinInterval = 2 * time.Second
	antigravityStartupTimeSkew = 5 * time.Second
	// antigravityMetadataMaxBytes caps how much of the CLI's metadata cache a
	// scan reads. A large file must not stall every status pass.
	antigravityMetadataMaxBytes = 8 << 20
	// antigravityForkChildTimeout bounds how long a fork with an initial
	// message waits for the child conversation to appear before giving up.
	antigravityForkChildTimeout = 20 * time.Second
	// antigravityForkReadyTimeout bounds the wait for the composer before
	// /fork, so a fork on a sign-in or trust screen cannot hold a web
	// mutation lock for the full launch readiness budget.
	antigravityForkReadyTimeout      = 90 * time.Second
	toolDataAntigravityForkParentKey = "antigravity_fork_parent_id"
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

// WriteAntigravityForkParentToToolData records the conversation a forked child
// started from until it binds its own, so a reload during the /fork transition
// still excludes the parent. An empty parent leaves the blob untouched: the
// value is tiny and is ignored once a binding exists.
func WriteAntigravityForkParentToToolData(td json.RawMessage, parentID string) json.RawMessage {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return td
	}
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	raw, err := json.Marshal(parentID)
	if err != nil {
		return td
	}
	m[toolDataAntigravityForkParentKey] = raw
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// antigravityForkParentForLoad applies a stored fork exclusion only while the
// session is still unbound; once it has its own ID the exclusion is stale.
func antigravityForkParentForLoad(boundID, parentID string) string {
	if strings.TrimSpace(boundID) != "" {
		return ""
	}
	return strings.TrimSpace(parentID)
}

// ReadAntigravityForkParentFromToolData returns the parent conversation a fork
// excludes, or "".
func ReadAntigravityForkParentFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		return ""
	}
	var id string
	if err := json.Unmarshal(m[toolDataAntigravityForkParentKey], &id); err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

// persistAntigravitySessionBinding writes the binding through the owning state
// DB. It reports whether the write landed so the caller can retry a failed
// write later; a lost write must not leave an in-memory binding that looks
// durable.
func (i *Instance) persistAntigravitySessionBinding(sessionID string, detectedAt time.Time) bool {
	if sessionID != "" {
		// Validate at the sink as well as at the source: the value is logged
		// and written, and a future caller must not be able to skip the check
		// (CodeQL go/log-injection).
		normalized, err := normalizeToolSessionID(FieldAntigravitySessionID, sessionID)
		if err != nil || normalized != sessionID {
			sessionLog.Warn("antigravity_session_bind_invalid_id",
				slog.String("instance_id", logging.SanitizeValue(i.ID)),
				slog.String("session_id", logging.SanitizeValue(sessionID)))
			return false
		}
	}
	err := i.withOwningStateDB(func(db *statedb.StateDB) error {
		return db.WriteAntigravitySessionBinding(i.ID, sessionID, detectedAt)
	})
	if errors.Is(err, errNoStateDB) {
		// No DB to write to in this context, so there is nothing to retry.
		return true
	}
	if err != nil {
		sessionLog.Warn("antigravity_session_bind_persist_failed",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("new_id", logging.SanitizeValue(sessionID)),
			slog.String("error", err.Error()))
		return false
	}
	return true
}

func splitAntigravityTokens(s string) []string {
	if tokens, ok := shellwords.Split(s); ok {
		return tokens
	}
	return strings.Fields(s)
}

// antigravityValueOptions are the options whose next token is a value. A
// prompt such as `--conversation=conv-other` after --prompt-interactive is
// data, never a selector.
var antigravityValueOptions = map[string]bool{
	"--conversation":       true,
	"--prompt-interactive": true,
	"-i":                   true,
	"--add-dir":            true,
}

func antigravityValidConversationID(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate != "" && antigravitySessionIDPattern.MatchString(candidate) {
		return candidate
	}
	return ""
}

// extractAntigravityConversationID returns an explicit --conversation <id>
// value from command or wrapper, if present and shell-safe. Options are parsed
// with their values so a prompt or path that spells another option is not read
// as a selector.
func extractAntigravityConversationID(command, wrapper string) string {
	for _, raw := range []string{command, wrapper} {
		tokens := splitAntigravityTokens(raw)
		for idx := 0; idx < len(tokens); idx++ {
			tok := tokens[idx]
			switch {
			case tok == "--conversation" && idx+1 < len(tokens):
				if id := antigravityValidConversationID(tokens[idx+1]); id != "" {
					return id
				}
				idx++
			case strings.HasPrefix(tok, "--conversation="):
				if id := antigravityValidConversationID(strings.TrimPrefix(tok, "--conversation=")); id != "" {
					return id
				}
			case antigravityValueOptions[tok]:
				idx++ // the value is data, never another option
			}
		}
	}
	return ""
}

func antigravityHasSessionSelector(command, wrapper string) bool {
	for _, raw := range []string{command, wrapper} {
		tokens := splitAntigravityTokens(raw)
		for idx := 0; idx < len(tokens); idx++ {
			tok := tokens[idx]
			switch {
			case tok == "--conversation" || strings.HasPrefix(tok, "--conversation=") || tok == "-c" || tok == "--continue":
				return true
			case antigravityValueOptions[tok]:
				idx++
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
		case antigravityValueOptions[tok]:
			idx++
		}
		if candidate != "" && normalizePath(candidate) == want {
			return true
		}
	}
	return false
}

// antigravityTokenNeedsShell reports whether a token carries shell syntax that
// re-quoting cannot preserve (expansions, globs, operators). Such a launcher
// is rejected rather than rewritten into something with a different meaning.
func antigravityTokenNeedsShell(tok string) bool {
	if tok == "" {
		return false
	}
	if strings.ContainsAny(tok, ";&|<>`$()") {
		return true
	}
	if strings.ContainsAny(tok, "*?[]") {
		return true
	}
	return strings.HasPrefix(tok, "~")
}

// antigravityAssignment splits a leading shell assignment (NAME=value) from a
// word, so rewriting keeps the NAME=value syntax and quotes only the value.
func antigravityAssignment(tok string) (name, value string, ok bool) {
	name, value, ok = strings.Cut(tok, "=")
	if !ok || name == "" {
		return "", "", false
	}
	for idx, r := range name {
		if r == '_' || unicode.IsLetter(r) || (idx > 0 && unicode.IsDigit(r)) {
			continue
		}
		return "", "", false
	}
	return name, value, true
}

// quoteAntigravityToken re-quotes one shell word without changing its meaning:
// assignments keep NAME=value syntax and the wrapper placeholder stays
// literal. It reports false for shell syntax the rewrite cannot preserve.
func quoteAntigravityToken(tok string) (string, bool) {
	if tok == wrapperPlaceholder {
		return tok, true
	}
	if name, value, ok := antigravityAssignment(tok); ok {
		if antigravityTokenNeedsShell(value) {
			return "", false
		}
		return name + "=" + shellescape.Quote(value), true
	}
	if antigravityTokenNeedsShell(tok) {
		return "", false
	}
	return quoteAntigravityArg(tok), true
}

// stripAntigravitySessionFlags removes conversation-selection and one-shot
// prompt-interactive flags from a base Antigravity command while preserving
// unrelated user flags (such as --mode, --model, --effort, --sandbox). It
// reports false when the command uses shell syntax that cannot be rewritten
// safely; the fork is rejected rather than launched with a changed meaning.
func stripAntigravitySessionFlags(command string) (string, bool) {
	tokens := splitAntigravityTokens(command)
	if len(tokens) == 0 {
		return "", true
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
		}
		quoted, ok := quoteAntigravityToken(tok)
		if !ok {
			return "", false
		}
		kept = append(kept, quoted)
	}
	return strings.Join(kept, " "), true
}

// stripAntigravityConversationFromWrapper removes a parent's conversation
// selector from a wrapper that carries {command}, so a fork does not stay
// pinned to its parent's conversation across future restarts. A wrapper
// without {command} is opaque: its argv is the user's, so it cannot be
// rewritten safely and the fork is rejected. If only {command} remains, it
// returns "".
func stripAntigravityConversationFromWrapper(wrapper string) (string, bool) {
	trimmed := strings.TrimSpace(wrapper)
	if trimmed == "" {
		return "", true
	}
	// Only a standalone, unquoted {command} word can be rewritten without
	// guessing the wrapper's quote context: `bash -lc "{command}"` and
	// `x{command}` are opaque.
	hasPlaceholder := false
	for _, field := range strings.Fields(trimmed) {
		if field == wrapperPlaceholder {
			hasPlaceholder = true
			break
		}
	}
	if !hasPlaceholder {
		return "", false
	}
	tokens := splitAntigravityTokens(trimmed)
	for _, tok := range tokens {
		if tok == wrapperPlaceholder {
			hasPlaceholder = true
			break
		}
	}
	if !hasPlaceholder {
		return "", false
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
		}
		quoted, ok := quoteAntigravityToken(tok)
		if !ok {
			return "", false
		}
		kept = append(kept, quoted)
	}
	if len(kept) == 1 && kept[0] == wrapperPlaceholder {
		return "", true
	}
	return strings.Join(kept, " "), true
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
// is bound and no explicit session selector is already set.
//
// A wrapper that carries {command} gets the flag through the substitution, so
// `nice {command}` resumes too. A wrapper without {command} is opaque: its
// argv is the user's, so the flag is not appended and the binding does not
// resume (documented in docs/guide/tools.md).
func (i *Instance) buildAntigravityResumeCommand() string {
	command := i.antigravityCommand()
	i.mu.RLock()
	sessionID := strings.TrimSpace(i.AntigravitySessionID)
	wrapper := strings.TrimSpace(i.Wrapper)
	i.mu.RUnlock()
	if sessionID == "" || antigravityHasSessionSelector(command, wrapper) {
		return command
	}
	normalized, err := normalizeToolSessionID(FieldAntigravitySessionID, sessionID)
	if err != nil || normalized == "" {
		return command
	}
	resume := command + " --conversation " + shellescape.Quote(normalized)
	if wrapper == "" || strings.Contains(wrapper, wrapperPlaceholder) {
		return resume
	}
	return command
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

// antigravityConversationEntryID returns the entry's own conversation ID,
// falling back to the map key when the summary omits it.
func antigravityConversationEntryID(mapID string, entry antigravityConversationEntry) string {
	id := strings.TrimSpace(entry.Summary.ID)
	if id == "" {
		id = strings.TrimSpace(mapID)
	}
	if !antigravitySessionIDPattern.MatchString(id) {
		return ""
	}
	return id
}

// readAntigravityMetadata reads the CLI's conversation metadata, bounded so a
// large cache can never stall a status pass. It also returns the internal IDs
// the scan must keep out of every discovery path, including the
// last_conversations fallback.
func readAntigravityMetadata(path string) (antigravityConversationMetadataFile, map[string]bool, bool) {
	var meta antigravityConversationMetadataFile
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > antigravityMetadataMaxBytes {
		return meta, nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &meta) != nil {
		return meta, nil, false
	}
	internal := make(map[string]bool)
	for mapID, entry := range meta.Conversations {
		if entry.IsInternal || entry.Summary.Internal {
			if id := antigravityConversationEntryID(mapID, entry); id != "" {
				internal[id] = true
			}
		}
	}
	return meta, internal, true
}

// discoverAntigravityConversationFromDisk finds the Antigravity conversation ID
// matching workDir/projectPath from Antigravity CLI's local cache files.
//
// Rules:
//   - A bound ID that still exists on disk or in metadata is kept, even when
//     metadata is missing it or lists a newer sibling: the cache cannot prove
//     which pane owns it.
//   - Skips internal/subagent conversations in the metadata scan and in the
//     last_conversations fallback.
//   - Never adopts excludeID (prevents a newly forked child from binding to its
//     parent's conversation before /fork finishes).
//   - When unbound and startedAt > 0, only adopts conversations updated on or
//     after startedAt - antigravityStartupTimeSkew; an entry with no timestamp
//     cannot prove it belongs to this pane and is treated as old.
func discoverAntigravityConversationFromDisk(dataDir, workDir, projectPath, currentID, excludeID string, startedAt int64) string {
	if dataDir == "" {
		return ""
	}
	if currentID != "" && antigravityConversationExistsInDir(dataDir, currentID) {
		return currentID
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

	meta, internalIDs, ok := readAntigravityMetadata(filepath.Join(dataDir, "cache", "conversation_metadata.json"))
	if ok && len(meta.Conversations) > 0 {
		var bestID string
		var bestTime time.Time
		currentFound := false

		for mapID, entry := range meta.Conversations {
			if entry.IsInternal || entry.Summary.Internal {
				continue
			}
			id := antigravityConversationEntryID(mapID, entry)
			if id == "" {
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
			if currentID == "" && !threshold.IsZero() && (ts.IsZero() || ts.Before(threshold)) {
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
				if internalIDs[id] {
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

// confirmAntigravityWorkspaceCandidate requires a workspace-discovered ID to
// be seen twice, at least one scan interval apart, before it becomes durable.
// Shared-workspace metadata cannot prove which pane created a conversation, so
// a single sighting is not evidence: two unbound siblings that start together
// would otherwise adopt each other's conversation.
func (i *Instance) confirmAntigravityWorkspaceCandidate(candidate string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.antigravityPendingID != candidate {
		i.antigravityPendingID = candidate
		i.antigravityPendingAt = time.Now()
		return false
	}
	return !i.antigravityPendingAt.IsZero() && time.Since(i.antigravityPendingAt) >= antigravityScanMinInterval
}

// clearAntigravityPending forgets a workspace candidate, e.g. one another
// session already owns.
func (i *Instance) clearAntigravityPending() {
	i.mu.Lock()
	i.antigravityPendingID = ""
	i.antigravityPendingAt = time.Time{}
	i.mu.Unlock()
}

// antigravitySessionIDBoundElsewhere reports whether another agent-deck
// session in this profile already stores candidate. A conversation discovered
// only through shared workspace metadata must never be stolen from the session
// that bound it first.
func (i *Instance) antigravitySessionIDBoundElsewhere(candidate string) bool {
	if candidate == "" {
		return false
	}
	bound := false
	err := i.withOwningStateDB(func(db *statedb.StateDB) error {
		rows, err := db.LoadInstances()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == i.ID {
				continue
			}
			if strings.TrimSpace(ReadAntigravitySessionIDFromToolData(row.ToolData)) == candidate {
				bound = true
				return nil
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoStateDB) {
		sessionLog.Warn("antigravity_binding_lookup_failed",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("error", err.Error()))
		return true // fail closed: stay unbound rather than steal
	}
	return bound
}

// bindAntigravityConversation publishes a binding to memory, tmux and SQLite.
// The caller holds antigravitySyncMu, so the external writes cannot interleave
// with another scan that would publish a newer result.
func (i *Instance) bindAntigravityConversation(candidate string) {
	now := time.Now()
	i.mu.Lock()
	i.AntigravitySessionID = candidate
	i.AntigravityDetectedAt = now
	i.antigravitySessionCleared = false
	i.antigravityForkParentID = ""
	i.antigravityPendingID = ""
	i.antigravityPendingAt = time.Time{}
	i.antigravityBindingPending = false
	tmuxSess := i.tmuxSession
	i.mu.Unlock()

	if tmuxSess != nil {
		if err := tmuxSess.SetEnvironment("ANTIGRAVITY_SESSION_ID", candidate); err != nil {
			sessionLog.Warn("antigravity_set_env_failed", slog.String("error", err.Error()))
		}
	}
	if !i.persistAntigravitySessionBinding(candidate, now) {
		i.mu.Lock()
		if i.AntigravitySessionID == candidate {
			i.antigravityBindingPending = true
		}
		i.mu.Unlock()
	}
}

// SyncAntigravitySessionID resolves and binds the Antigravity conversation ID
// from explicit command flags, tmux environment, or local disk metadata.
func (i *Instance) SyncAntigravitySessionID() string {
	if i.Tool != "antigravity" {
		return ""
	}
	// Serialize discovery with its commit: a slower scan must not publish an
	// older result after a newer one.
	i.antigravitySyncMu.Lock()
	defer i.antigravitySyncMu.Unlock()

	i.mu.RLock()
	currentID := strings.TrimSpace(i.AntigravitySessionID)
	detectedAt := i.AntigravityDetectedAt
	bindingPending := i.antigravityBindingPending
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

	// A binding that reached memory but not SQLite is retried before the early
	// return; otherwise a recovered database would never see it again.
	if currentID != "" {
		if bindingPending && i.persistAntigravitySessionBinding(currentID, detectedAt) {
			i.mu.Lock()
			if i.AntigravitySessionID == currentID {
				i.antigravityBindingPending = false
			}
			i.mu.Unlock()
		}
		return currentID
	}

	candidate := ""
	trusted := false
	if explicit := extractAntigravityConversationID(command, wrapper); explicit != "" && explicit != excludeID {
		candidate, trusted = explicit, true
	}
	if candidate == "" && tmuxSession != nil {
		if envID, err := tmuxSession.GetEnvironment("ANTIGRAVITY_SESSION_ID"); err == nil {
			envID = strings.TrimSpace(envID)
			if envID != "" && envID != excludeID && antigravitySessionIDPattern.MatchString(envID) {
				candidate, trusted = envID, true
			}
		}
	}
	if candidate == "" && sshHost == "" && !sandboxed {
		candidate = discoverAntigravityConversationFromDisk(antigravityDataDir(), workDir, projectPath, "", excludeID, startedAt)
	}
	if candidate == "" {
		return currentID
	}
	// A fork child has strong evidence: the parent is excluded and the child
	// can only appear after /fork. A plain unbound session has only shared
	// workspace metadata, so it needs a second observation and must not take
	// an ID another session already owns.
	if !trusted && excludeID == "" {
		if !i.confirmAntigravityWorkspaceCandidate(candidate) {
			return ""
		}
		if i.antigravitySessionIDBoundElsewhere(candidate) {
			sessionLog.Info("antigravity_binding_owned_elsewhere",
				slog.String("instance_id", logging.SanitizeValue(i.ID)),
				slog.String("session_id", logging.SanitizeValue(candidate)))
			i.clearAntigravityPending()
			return ""
		}
	}

	i.bindAntigravityConversation(candidate)
	return candidate
}

// UpdateAntigravitySession refreshes the bound Antigravity conversation ID,
// rate-limited to avoid unnecessary filesystem reads on render/status ticks.
// A durable binding stops the scan entirely; only an unbound session, or one
// whose SQLite write still has to be retried, reads the cache again.
func (i *Instance) UpdateAntigravitySession() {
	if i.Tool != "antigravity" {
		return
	}
	i.mu.Lock()
	if i.AntigravitySessionID != "" && !i.antigravityBindingPending {
		i.mu.Unlock()
		return
	}
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

// forkAntigravityInPane sends /fork through the ready composer and stops the
// spawned pane when the send fails, so a child no caller can manage never
// keeps running in tmux.
func (i *Instance) forkAntigravityInPane() error {
	if i.tmuxSession == nil {
		return fmt.Errorf("sending /fork to Antigravity session: tmux session not initialized")
	}
	if err := send.WaitForAgentReady(i.tmuxSession, i.Tool, antigravityForkReadyTimeout, send.PromptGates{
		ClaudeComposer:      IsClaudeCompatible(i.Tool),
		CodexPrompt:         IsCodexCompatible(i.Tool),
		AntigravityComposer: IsAntigravityCompatible(i.Tool),
	}); err != nil {
		return i.stopAntigravityForkPane(fmt.Errorf("waiting for the Antigravity composer before /fork: %w", err))
	}
	if err := i.sendMessageWhenReady("/fork"); err != nil {
		return i.stopAntigravityForkPane(fmt.Errorf("sending /fork to Antigravity session: %w", err))
	}
	return nil
}

// stopAntigravityForkPane kills the spawned fork pane after a failed /fork so
// no live child is left untracked, and reports both failures when the kill
// fails too.
func (i *Instance) stopAntigravityForkPane(err error) error {
	if i.tmuxSession == nil {
		return err
	}
	if killErr := i.tmuxSession.Kill(); killErr != nil {
		sessionLog.Warn("antigravity_fork_pane_kill_failed", slog.String("error", killErr.Error()))
		return fmt.Errorf("%w (stopping the pane also failed: %v)", err, killErr)
	}
	return err
}

// antigravityKnownConversations snapshots the conversations that already exist
// for this workspace, so a fork wait binds only the conversation /fork creates
// and not a sibling that happened to be newer.
func (i *Instance) antigravityKnownConversations() map[string]bool {
	known := map[string]bool{}
	dataDir := antigravityDataDir()
	if dataDir == "" {
		return known
	}
	wantDirs := make([]string, 0, 2)
	if norm := normalizePath(i.EffectiveWorkingDir()); norm != "" {
		wantDirs = append(wantDirs, norm)
	}
	if norm := normalizePath(i.ProjectPath); norm != "" && (len(wantDirs) == 0 || wantDirs[0] != norm) {
		wantDirs = append(wantDirs, norm)
	}
	if len(wantDirs) == 0 {
		return known
	}
	if meta, _, ok := readAntigravityMetadata(filepath.Join(dataDir, "cache", "conversation_metadata.json")); ok {
		for mapID, entry := range meta.Conversations {
			if entry.IsInternal || entry.Summary.Internal {
				continue
			}
			if !antigravityWorkspaceMatches(entry.Summary.WorkspaceURIs, wantDirs) {
				continue
			}
			if id := antigravityConversationEntryID(mapID, entry); id != "" {
				known[id] = true
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "cache", "last_conversations.json")); err == nil {
		var last map[string]string
		if json.Unmarshal(data, &last) == nil {
			for dir, rawID := range last {
				if !antigravityWorkspaceMatches([]string{dir}, wantDirs) {
					continue
				}
				if id := antigravityValidConversationID(rawID); id != "" {
					known[id] = true
				}
			}
		}
	}
	return known
}

// waitForAntigravityForkChild waits until /fork creates a conversation that
// did not exist before the send and binds it. The ready prompt can return while
// the parent's composer is still on screen, so an initial message must not be
// sent until the pane is known to target the child.
func (i *Instance) waitForAntigravityForkChild(forkSentAt time.Time, knownBefore map[string]bool, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = antigravityForkChildTimeout
	}
	i.antigravitySyncMu.Lock()
	defer i.antigravitySyncMu.Unlock()

	deadline := time.Now().Add(timeout)
	for {
		i.mu.RLock()
		parentID := strings.TrimSpace(i.antigravityForkParentID)
		currentID := strings.TrimSpace(i.AntigravitySessionID)
		i.mu.RUnlock()
		if currentID != "" && currentID != parentID {
			return true
		}
		if parentID == "" {
			return false // no exclusion to compare against; cannot confirm
		}
		candidate := discoverAntigravityConversationFromDisk(
			antigravityDataDir(), i.EffectiveWorkingDir(), i.ProjectPath, "", parentID, forkSentAt.UnixMilli())
		if candidate != "" && !knownBefore[candidate] {
			i.bindAntigravityConversation(candidate)
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
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
		// The fork would run agy locally against a conversation that lives on
		// the parent's host, so reject it rather than launch in the wrong
		// execution scope.
		return false
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
	cleanCmd, ok := stripAntigravitySessionFlags(resolvedBase)
	if !ok {
		return "", fmt.Errorf("cannot fork: Antigravity command %q uses shell syntax agent-deck cannot rewrite safely", resolvedBase)
	}
	if cleanCmd == "" {
		cleanCmd = GetToolCommand("antigravity")
	}

	parts := []string{cleanCmd, "--conversation", shellescape.Quote(parentID)}
	if target.ProjectPath != "" && i.ProjectPath != "" &&
		normalizePath(target.ProjectPath) != normalizePath(i.ProjectPath) &&
		!antigravityHasAddDir(cleanCmd, target.ProjectPath) {
		parts = append(parts, "--add-dir", shellescape.Quote(target.ProjectPath))
	}

	return strings.Join(parts, " "), nil
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
	wrapper, ok := stripAntigravityConversationFromWrapper(i.Wrapper)
	if !ok {
		return nil, "", fmt.Errorf("cannot fork: Antigravity wrapper %q has no {command} placeholder, so its arguments cannot be rewritten safely", i.Wrapper)
	}
	forked.Wrapper = wrapper

	baseCommand, ok := stripAntigravitySessionFlags(i.antigravityCommand())
	if !ok {
		return nil, "", fmt.Errorf("cannot fork: Antigravity command %q uses shell syntax agent-deck cannot rewrite safely", i.antigravityCommand())
	}
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
