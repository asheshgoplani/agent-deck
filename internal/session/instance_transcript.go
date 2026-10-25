package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ResponseOutput represents a parsed response from an agent session
type ResponseOutput struct {
	Tool                string `json:"tool"`                            // Tool type (claude, gemini, etc.)
	Role                string `json:"role"`                            // Always "assistant" for now
	Content             string `json:"content"`                         // The actual response text
	Timestamp           string `json:"timestamp,omitempty"`             // When the response was generated
	SessionID           string `json:"session_id,omitempty"`            // Conversation session ID, if available
	CodexTurnGeneration string `json:"codex_turn_generation,omitempty"` // Exact thread:turn identity, if retained
	ClaudeTurnUUID      string `json:"claude_turn_uuid,omitempty"`      // Bound Claude user record, if retained
	// AssistantUUID is the transcript uuid of the Claude assistant record
	// Content was read from; the turn identity an inbox record carries as
	// turn_uuid. Empty for pane, terminal and non-Claude reads. Not part of
	// the CLI output.
	AssistantUUID string `json:"-"`
}

// GetLastResponse returns the last assistant response from the session
// For Claude: Parses the JSONL file for the last assistant message
// For Gemini: Parses the JSON session file for the last assistant message
// For Codex/Others: Attempts to parse terminal output
func (i *Instance) GetLastResponse() (*ResponseOutput, error) {
	if IsClaudeCompatible(i.Tool) {
		return i.getClaudeLastResponse()
	}
	if i.Tool == "gemini" {
		return i.getGeminiLastResponse()
	}
	if i.Tool == "pi" {
		return i.getPiLastResponse()
	}
	return i.getTerminalLastResponse()
}

type codexRolloutRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type             string  `json:"type"`
		TurnID           string  `json:"turn_id"`
		LastAgentMessage *string `json:"last_agent_message"`
		Item             struct {
			Type string `json:"type"`
		} `json:"item"`
	} `json:"payload"`
}

func (i *Instance) codexRolloutPath() (string, error) {
	if !i.CodexRolloutIsResolvableLocally() {
		return "", fmt.Errorf("Codex rollout is not locally readable")
	}
	if err := validateExactSessionID(i.CodexSessionID); err != nil {
		return "", err
	}
	paths, err := exactCodexRolloutMatches(i.CodexSessionID, i.getCodexHomeDir())
	if err != nil {
		return "", err
	}
	path, err := uniqueRegularArtifact(paths, "rollout for "+i.CodexSessionID)
	if err != nil {
		return "", err
	}
	return path, nil
}

func (i *Instance) codexRolloutTail() ([]string, error) {
	path, err := i.codexRolloutPath()
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, nil
	}
	return TranscriptTailLines(path, 50)
}

// scanCodexRolloutBackward offers the rollout's records to visit, newest
// first, until visit reports it is done. The byte window widens up to
// codexTurnGenerationScanMaxBytes; each wider pass starts again from the
// newest record, so newVisit returns a fresh visitor for every pass. It
// reports whether a visitor finished.
func scanCodexRolloutBackward(path string, newVisit func() func(codexRolloutRecord) bool) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- codexRolloutPath resolves one exact, non-symlink rollout
	if err != nil {
		return false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	size := info.Size()
	if size == 0 {
		return false, nil
	}
	scanBytes := min(size, codexTurnGenerationInitialScanBytes)
	for {
		offset := size - scanBytes
		data := make([]byte, int(scanBytes))
		if _, err := f.ReadAt(data, offset); err != nil {
			return false, err
		}

		// When the byte window lands mid-record, discard that incomplete
		// leading fragment. If it lands exactly after a newline, the first
		// record is whole and remains eligible.
		if offset > 0 {
			var previous [1]byte
			if _, err := f.ReadAt(previous[:], offset-1); err != nil {
				return false, err
			}
			if previous[0] != '\n' {
				newline := bytes.IndexByte(data, '\n')
				if newline < 0 {
					data = nil
				} else {
					data = data[newline+1:]
				}
			}
		}

		visit := newVisit()
		for end := len(data); end > 0; {
			newline := bytes.LastIndexByte(data[:end], '\n')
			line := bytes.TrimSpace(data[newline+1 : end])
			end = newline
			if newline < 0 {
				end = 0
			}
			if len(line) == 0 {
				continue
			}
			var record codexRolloutRecord
			if json.Unmarshal(line, &record) != nil {
				continue
			}
			if visit(record) {
				return true, nil
			}
		}

		if offset == 0 || scanBytes == codexTurnGenerationScanMaxBytes {
			return false, nil
		}
		scanBytes = min(size, min(scanBytes*2, codexTurnGenerationScanMaxBytes))
	}
}

func isCodexTurnStart(record codexRolloutRecord) bool {
	return record.Type == "event_msg" && record.Payload.TurnID != "" &&
		(record.Payload.Type == "task_started" || record.Payload.Type == "turn_started")
}

func latestCodexTurnIDFromRollout(path string) (string, error) {
	turnID := ""
	_, err := scanCodexRolloutBackward(path, func() func(codexRolloutRecord) bool {
		return func(record codexRolloutRecord) bool {
			if !isCodexTurnStart(record) {
				return false
			}
			turnID = record.Payload.TurnID
			return true
		}
	})
	return turnID, err
}

// codexUserInputEncoding names how a rollout record carries user input, or
// returns "" when it carries none. Current Codex writes an item_completed
// UserMessage item; older releases wrote a user_message event.
func codexUserInputEncoding(record codexRolloutRecord) string {
	if record.Type != "event_msg" {
		return ""
	}
	switch {
	case record.Payload.Type == "item_completed" && record.Payload.Item.Type == "UserMessage":
		return "item"
	case record.Payload.Type == "user_message":
		return "event"
	}
	return ""
}

// codexTurnTookInputSinceFromRollout reports whether turnID is the newest turn
// in the rollout and took up user input recorded at or after since, beyond
// the input that opened it (issue #2836). Codex starts no turn for input sent
// while a turn runs; the running turn records it as one more user input. The
// earliest input of each encoding in the turn is its opening input. Only
// record types, turn ids and timestamps are read. A turn start beyond the
// scan window, or a newer turn, reports false.
func codexTurnTookInputSinceFromRollout(path, turnID string, since time.Time) (bool, error) {
	took := false
	_, err := scanCodexRolloutBackward(path, func() func(codexRolloutRecord) bool {
		// Newest first: the last time kept per encoding is the opening input.
		inputs := map[string][]time.Time{}
		return func(record codexRolloutRecord) bool {
			if isCodexTurnStart(record) {
				if record.Payload.TurnID == turnID {
					for _, times := range inputs {
						for _, at := range times[:len(times)-1] {
							if !at.Before(since) {
								took = true
							}
						}
					}
				}
				return true
			}
			encoding := codexUserInputEncoding(record)
			if encoding == "" || (record.Payload.TurnID != "" && record.Payload.TurnID != turnID) {
				return false
			}
			// An unreadable timestamp keeps its place as an input but is
			// the zero time, so it never counts as later.
			at, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
			inputs[encoding] = append(inputs[encoding], at)
			return false
		}
	})
	return took, err
}

func (i *Instance) getCodexLastResponse() (*ResponseOutput, error) {
	lines, err := i.codexRolloutTail()
	if err != nil {
		return nil, err
	}
	return parseCodexLastAssistantMessage(lines, i.CodexSessionID)
}

func parseCodexLastAssistantMessage(lines []string, sessionID string) (*ResponseOutput, error) {
	var last *ResponseOutput
	for _, line := range lines {
		var record codexRolloutRecord
		if json.Unmarshal([]byte(line), &record) != nil || record.Type != "event_msg" ||
			(record.Payload.Type != "task_complete" && record.Payload.Type != "turn_complete") ||
			record.Payload.TurnID == "" {
			continue
		}
		content := ""
		if record.Payload.LastAgentMessage != nil {
			content = *record.Payload.LastAgentMessage
		}
		last = &ResponseOutput{Tool: "codex", Role: "assistant", Content: content,
			Timestamp: record.Timestamp, SessionID: sessionID,
			CodexTurnGeneration: sessionID + ":" + record.Payload.TurnID}
	}
	if last == nil {
		return nil, fmt.Errorf("no completed Codex turn found")
	}
	return last, nil
}

// LatestCodexTurnGeneration returns the newest durable turn-start identity in
// the exact rollout bound to this instance. It never derives identity from
// prompt or response content.
//
// A thread the pane's live Codex process owns but has not yet written a
// rollout for (fresh composer, no turn started) has no turn generation yet, so
// it reports "" without error, like an empty rollout.
func (i *Instance) LatestCodexTurnGeneration() (string, error) {
	path, err := i.codexRolloutPath()
	if errors.Is(err, errNoExactContextArtifact) && i.LiveCodexThreadID() == i.CodexSessionID {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	turnID, err := latestCodexTurnIDFromRollout(path)
	if err != nil || turnID == "" {
		return "", err
	}
	return i.CodexSessionID + ":" + turnID, nil
}

// CodexTurnTookInputSince reports whether generation still names the newest
// turn in the exact rollout bound to this instance and that turn took up user
// input recorded at or after since, beyond its opening input. Like
// LatestCodexTurnGeneration it never reads prompt or response content.
func (i *Instance) CodexTurnTookInputSince(generation string, since time.Time) (bool, error) {
	turnID, found := strings.CutPrefix(generation, i.CodexSessionID+":")
	if i.CodexSessionID == "" || !found || turnID == "" {
		return false, nil
	}
	path, err := i.codexRolloutPath()
	if err != nil {
		return false, err
	}
	return codexTurnTookInputSinceFromRollout(path, turnID, since)
}

// GetLastResponseBestEffortChecked is the collision-aware variant of
// GetLastResponseBestEffort (issue #1400). `session output` (including -q)
// parses the transcript that ClaudeSessionID resolves to; when multiple LIVE
// instances share one claude_session_id they all resolve to the SAME transcript
// and return byte-identical "last responses". Given the instance's profile
// peers, this refuses the read with the same collision semantics #1352 gave
// `session output --stream` (GetJSONLPathChecked: live peers sharing both the
// session id and the transcript dir), instead of silently returning another
// session's output. Non-Claude tools and the no-collision case delegate to
// GetLastResponseBestEffort unchanged.
func (i *Instance) GetLastResponseBestEffortChecked(peers []*Instance) (*ResponseOutput, error) {
	if IsClaudeCompatible(i.Tool) {
		if _, err := i.GetJSONLPathChecked(peers); err != nil {
			return nil, fmt.Errorf("refusing to read a colliding transcript: %w", err)
		}
	}
	return i.GetLastResponseBestEffort()
}

// responseVersionedFile is the one file the primary last-response read parses
// (the bound Claude transcript or the exact Codex rollout), or "" when the
// response would come from somewhere unversioned (pane text, a disk-scan
// recovery, another tool) or when peers share the Claude transcript (#1400).
func (i *Instance) responseVersionedFile(peers []*Instance) string {
	switch {
	case IsCodexCompatible(i.Tool):
		path, err := i.codexRolloutPath()
		if err != nil {
			return ""
		}
		return path
	case IsClaudeCompatible(i.Tool):
		path, err := i.GetJSONLPathChecked(peers)
		if err != nil {
			return ""
		}
		return path
	}
	return ""
}

// ResponseContentVersion returns an opaque version of the file the last
// response is parsed from (path, size and modification time), or "" when the
// response is not backed by one versioned file. Equal versions mean the
// parsed last response cannot have changed, so a poller can skip the read
// (issue #2481). It costs one stat, never a parse.
func (i *Instance) ResponseContentVersion(peers []*Instance) string {
	path := i.responseVersionedFile(peers)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	return fmt.Sprintf("v1-%x-%x-%x", h.Sum64(), info.Size(), info.ModTime().UnixNano())
}

// GetLastResponseAtVersion is GetLastResponseBestEffortChecked for a caller
// that took version from ResponseContentVersion just before. It reports true
// when the response was parsed from that versioned file, so the version may
// be handed out with it; a fallback response reports false. The version is
// taken before the read, so a write racing the read can only make the next
// comparison miss (one extra read), never hide a change.
func (i *Instance) GetLastResponseAtVersion(peers []*Instance, version string) (*ResponseOutput, bool, error) {
	if version != "" {
		read := i.getClaudeLastResponse
		if IsCodexCompatible(i.Tool) {
			read = i.getCodexLastResponse
		}
		if resp, err := read(); err == nil {
			return resp, true, nil
		}
	}
	resp, err := i.GetLastResponseBestEffortChecked(peers)
	return resp, false, err
}

// GetLastResponseBestEffort returns the last assistant response with fallback logic
// intended for CLI read paths (like `session output`) where we prefer useful output
// over hard errors.
//
// Behavior for Claude:
//  1. Try structured JSONL read via stored ClaudeSessionID.
//  2. Refresh ID from tmux env and retry.
//  3. If the currently bound ID already has a local transcript file, scan for a
//     /clear rollover. A missing file is not ownership of a neighboring JSONL.
//  4. Fallback to terminal parsing.
//  5. If still unavailable, return an empty response (no error).
//
// Behavior for Gemini (mirrors Claude):
// 1. Try structured JSON read via stored GeminiSessionID.
// 2. Refresh ID from tmux env and retry.
// 3. Scan disk for latest session and retry.
// 4. Fallback to terminal parsing.
// 5. If still unavailable, return an empty response (no error).
func (i *Instance) GetLastResponseBestEffort() (*ResponseOutput, error) {
	if IsCodexCompatible(i.Tool) {
		// Prefer the exact rollout response, avoiding a tmux subprocess on the
		// normal local path. On failure, retain the historical terminal fallback.
		if exact, exactErr := i.getCodexLastResponse(); exactErr == nil {
			return exact, nil
		}
	}
	resp, err := i.GetLastResponse()
	if err == nil {
		return resp, nil
	}

	// Claude-specific recovery path
	if IsClaudeCompatible(i.Tool) {
		// Refresh from tmux env (fast path)
		if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
			i.ClaudeSessionID = sessionID
			// #1815: own pane env — weak vouch.
			i.noteClaudeSessionIDFromOwnPane()
			i.ClaudeDetectedAt = time.Now()
			if recovered, recoverErr := i.getClaudeLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}

		// Disk scan: the tmux env var is fixed at launch, so after a /clear or
		// compaction it points at a stale, empty transcript. Find the newest
		// transcript on disk that carries a real assistant reply. Mirrors the
		// Gemini syncGeminiSessionFromDisk fallback below.
		//
		// Issue #2299 (samratashoka007, #2303) + carry fix: the mtime filter
		// inside findLatestClaudeTranscriptOnDisk already excludes candidates
		// older than i.LastStartedAt, but treats a ZERO LastStartedAt as
		// "unknown" and does not filter at all — the exact case #2303 caught,
		// where a session record predates the LastStartedAt field (or the
		// caller never started it) and LastStartedAt gives no evidence either
		// way. #2303's own fix (require i.GetJSONLPath() != "", i.e. the
		// bound id must already have a local file) is safe there, but applied
		// unconditionally it also blocks legitimate /clear-rollover recovery:
		// a session can be live (LastStartedAt known and reliable) with a
		// bound id that has not been flushed to disk yet while Claude has
		// already written the rollover under a NEW id — GetJSONLPath() on the
		// OLD id is "" in that window, so the unconditional gate would wrongly
		// refuse the newer, legitimate rollover transcript.
		//
		// Only require #2303's stronger "already owns a local file" evidence
		// when LastStartedAt itself gives no evidence (zero/unknown). When
		// LastStartedAt is known, the existing per-candidate mtime filter
		// alone is sufficient and rollover recovery must not be blocked by
		// the bound id's own file having not appeared yet.
		if !i.LastStartedAt.IsZero() || i.GetJSONLPath() != "" {
			if id, recovered := i.findLatestClaudeTranscriptOnDisk(); recovered != nil {
				// #1815: this is an mtime-based disk scan — the same evidence
				// class as the restart discovery prelude, and in a shared working
				// directory the newest transcript can belong to another session.
				// Good enough to READ a last response from; never ownership. Mark
				// it unverified so it cannot authorize a later `--resume`, and do
				// NOT write it into this pane's CLAUDE_SESSION_ID: that would
				// launder a scanned id into a "bound from my own tmux env" one on
				// the next status poll, which is exactly the resume this guard
				// exists to prevent.
				i.adoptDiscoveredClaudeSessionID(id)
				i.ClaudeDetectedAt = time.Now()
				return recovered, nil
			}
		}
	}

	// Gemini-specific recovery path (mirrors Claude recovery above)
	if i.Tool == "gemini" {
		// Refresh from tmux env (fast path)
		i.syncGeminiSessionFromTmux()
		if i.GeminiSessionID != "" {
			if recovered, recoverErr := i.getGeminiLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}

		// Fallback: detect latest session on disk (handles startup race / stale ID)
		i.syncGeminiSessionFromDisk()
		if i.GeminiSessionID != "" {
			if recovered, recoverErr := i.getGeminiLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}
	}

	// Final fallback: terminal parsing (works for all tools).
	if i.tmuxSession != nil {
		terminalResp, terminalErr := i.getTerminalLastResponse()
		if terminalErr == nil {
			return terminalResp, nil
		}
		err = terminalErr
	}

	// A vanished terminal is benign for every tool. Preserve the existing
	// graceful-empty behavior for Claude and Gemini when recovery fails.
	if errors.Is(err, tmux.ErrCaptureGone) || IsClaudeCompatible(i.Tool) || i.Tool == "gemini" {
		toolName := i.Tool
		if IsClaudeCompatible(toolName) {
			toolName = "claude"
		}
		return &ResponseOutput{
			Tool:    toolName,
			Role:    "assistant",
			Content: "",
		}, nil
	}

	return nil, err
}

// ClaudeSessionIDCollidesWith reports whether another LIVE instance in peers
// would resolve to the SAME transcript as this instance: it shares this
// instance's (non-empty) ClaudeSessionID AND resolves to the same transcript
// directory (same Claude config dir + same encoded project path). A many-to-one
// session-id → live-instance mapping on one transcript is the data-integrity
// hazard #1349 describes: two live instances pointed at one transcript would
// cross-route input/output. Two instances that happen to share a session id but
// resolve to different transcript dirs (different project/config) are NOT a
// collision and are not blocked.
func (i *Instance) ClaudeSessionIDCollidesWith(peers []*Instance) bool {
	if i.ClaudeSessionID == "" {
		return false
	}
	mine := i.claudeTranscriptDir()
	for _, p := range peers {
		if p == nil || p.ID == i.ID {
			continue
		}
		if p.ClaudeSessionID != i.ClaudeSessionID || !isLiveSessionStatus(p.Status) {
			continue
		}
		if p.claudeTranscriptDir() == mine {
			return true
		}
	}
	return false
}

// conversationIsResumable answers the question the spawn builders actually ask:
// should this session be started with `--resume <id>` (continue the existing
// conversation) or `--session-id <id>` (create a fresh one under that id)?
//
// For a LOCAL session that is the same question as "is there conversation data
// on disk", so it delegates unchanged.
//
// For an --ssh session it is NOT. The transcript lives on the remote host, so
// the controller cannot see it — and answering the local-disk question with a
// hard false (which is the correct answer to *that* question, see
// sessionHasConversationData) would make every remote restart emit
// `--session-id <already-used-uuid>`, asking claude to CREATE a session that
// already exists. That loses the conversation, or claude rejects the id
// outright. Review round 1, finding F1.
//
// The controller's evidence for a remote session is the id it recorded when the
// session last ran. A non-empty ClaudeSessionID means "this session previously
// had this conversation", so `--resume` is the right instruction and the remote
// claude resolves it against the remote disk — the only disk that can answer.
// If the remote transcript is genuinely gone, claude reports that on the remote,
// which is a far better failure than silently starting fresh over a live id.
func conversationIsResumable(inst *Instance, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return true
	}
	// #2269: this is the only path that turns the answer into a `--resume`,
	// so it is the one place a cross-config-dir import belongs. The predicate
	// below stays a pure read: the hook/tmux rebind gates call it on live
	// sessions, where copying a transcript mid-conversation would freeze a
	// stale snapshot into the new dir.
	importConversationForResume(inst, sessionID)
	return sessionHasConversationData(inst, sessionID)
}

// importConversationForResume handles the #2269 case at resume time: the
// config dir the new process will use either has no transcript for sessionID
// or holds only a stub (a `--session-id` launch that fabricated an empty
// conversation under the real id), while another known config dir
// (~/.claude, or any profile's config_dir) holds the real one. It copies the
// real transcript in - the same copy `session switch-account` does, stripping
// stale history-suppression records and backing up any existing destination
// - so the launcher emits `--resume` against the full history.
//
// A primary transcript that carries conversation data is authoritative and
// is never replaced: the import strips records, so a copy in another dir can
// be LARGER than the primary without being newer, and Claude appends to the
// primary from the moment it resumes. Only a missing primary or a stub (no
// "sessionId" line) is filled in, from the largest exact-id file elsewhere
// that does carry conversation data. Nothing is written otherwise.
func importConversationForResume(inst *Instance, sessionID string) {
	if inst == nil || sessionID == "" || !inst.TranscriptIsResolvableLocally() {
		return
	}
	// The scan below keys on the instance's own bound id. With an unbound or
	// different id there is nothing that can be attributed to this session.
	if strings.TrimSpace(inst.ClaudeSessionID) != sessionID {
		return
	}
	if sessionHasConversationData(inst, sessionID) {
		return // primary (or a same-dir cross-project hit) is authoritative
	}
	configDir := GetClaudeConfigDirForInstance(inst)
	if configDir == "" {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	sameDir := func(a, b string) bool {
		return filepath.Clean(a) == filepath.Clean(b) || resolveRealPath(a) == resolveRealPath(b)
	}
	// The id is session state, not a path: refuse anything that is not a
	// single path segment before it is joined under a config root.
	fileName, err := conversationPathComponent(sessionID + ".jsonl")
	if err != nil {
		return
	}
	cfg, _ := LoadUserConfig()
	srcDir, srcSize := "", int64(0)
	for _, dir := range conversationConfigDirCandidates(cfg, configDir) {
		if sameDir(dir, configDir) {
			continue
		}
		for _, projDirName := range conversationProjectDirNames(inst.ProjectPath) {
			path, err := containedConversationPath(dir, "projects", projDirName, fileName)
			if err != nil {
				continue
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() <= srcSize {
				continue
			}
			if !transcriptFileHasConversationData(path) {
				continue
			}
			srcDir, srcSize = dir, info.Size()
		}
	}
	if srcDir == "" {
		return // nowhere else on this host: nothing to import
	}
	dst, written, err := MigrateConversationFromSized(inst, srcDir, configDir)
	if err != nil || dst == "" {
		sessionLog.Warn("session_data_import_failed",
			slog.String("session_id", logging.SanitizeValue(sessionID)),
			slog.String("source_config_dir", logging.SanitizeValue(srcDir)),
			slog.String("target_config_dir", logging.SanitizeValue(configDir)),
			slog.String("error", logging.SanitizeValue(fmt.Sprintf("%v", err))))
		return
	}
	sessionLog.Info("session_data_imported_from_other_config_dir",
		slog.String("session_id", logging.SanitizeValue(sessionID)),
		slog.String("source_config_dir", logging.SanitizeValue(srcDir)),
		slog.String("target_config_dir", logging.SanitizeValue(configDir)),
		slog.Int64("source_bytes", srcSize),
		slog.Int64("written_bytes", written))
}

// transcriptFileHasConversationData is the file-level form of the
// sessionHasConversationData scan: true when any line carries a "sessionId"
// field, which a system-prompt-only stub never does.
func transcriptFileHasConversationData(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"sessionId"`) {
			return true
		}
	}
	return false
}

// TranscriptIsResolvableLocally reports whether this instance's conversation
// transcript can be looked up in the CONTROLLER's filesystem.
//
// It is false for an --ssh session, unconditionally (#1851). That session's
// harness runs on another host and writes its transcript into THAT host's config
// dir. All the controller holds is ProjectPath — a local placeholder defaulting
// to the directory `add --ssh` was run from — so a lookup through it does not
// merely miss: it HITS, on whatever local session happens to sit at that
// directory. Every local-disk transcript lookup must ask this first; the harm is
// symmetrical (a remote session reads a local conversation, and by adopting its
// id sends the next restart's duplicate sweeper after the local session that
// owns it).
//
// The complete list of entry points this gates is in the package doc for
// remote_transcript_boundary.go.
func (i *Instance) TranscriptIsResolvableLocally() bool {
	return i != nil && !i.IsSSH()
}

// CodexRolloutIsResolvableLocally reports whether the controller can read the
// exact rollout used for accepted-turn correlation. In addition to remote SSH
// sessions, this excludes sandboxed harnesses whose Codex home belongs to the
// container rather than the controller process.
func (i *Instance) CodexRolloutIsResolvableLocally() bool {
	return i != nil && !i.IsSSH() && !i.IsSandboxed()
}

// claudeTranscriptDir returns the key used to decide whether two instances would
// collide on one transcript file (issue #1349 defense-in-depth #2, via
// ClaudeSessionIDCollidesWith). For a local instance it mirrors GetJSONLPath's
// resolution — GetClaudeConfigDir + encoded ProjectPath — exactly, so the
// collision verdict matches the path the guard protects.
//
// Issue #1851: a remote session's transcript is not in this filesystem at all,
// and its ProjectPath is only a local placeholder, so two remote sessions
// sharing a placeholder were judged to share a transcript directory and
// GetJSONLPathChecked refused reads that were never in conflict. Remote sessions
// are therefore keyed on where they actually run (host + canonical remote path).
//
// Changing this key is only safe because it is changed together with every path
// that derives a file location from it: TranscriptIsResolvableLocally gates
// GetJSONLPath, getClaudeLastResponse and findLatestClaudeTranscriptOnDisk (see
// remote_transcript_boundary.go), so no remote instance resolves a local
// transcript path at all and a local/remote pair sharing one claude_session_id
// can no longer land on one file for the guard to have to catch. Without those
// gates this key would DISABLE the #1349/#1352 guard for exactly the pair this
// epic exists to separate. The result is a collision KEY that is never opened as
// a path, and the "remote-transcripts" segment keeps it from ever aliasing a
// real local projects/ directory.
func (i *Instance) claudeTranscriptDir() string {
	configDir := GetClaudeConfigDir()
	if i.IsSSH() {
		loc := LocationOf(i)
		return filepath.Join(configDir, "remote-transcripts", ConvertToClaudeDirName(loc.String()))
	}
	resolvedPath := i.ProjectPath
	if resolved, err := filepath.EvalSymlinks(i.ProjectPath); err == nil {
		resolvedPath = resolved
	}
	return filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolvedPath))
}

// GetJSONLPathChecked is the collision-aware variant of GetJSONLPath (issue
// #1349 defense-in-depth #2). Given the set of instances it shares a profile
// with, it refuses to resolve a transcript path when this instance's
// ClaudeSessionID collides with another LIVE instance's ClaudeSessionID —
// because two live instances mapped to one session-id would otherwise read the
// same transcript, corrupting routing. When there is no collision it delegates
// to GetJSONLPath.
func (i *Instance) GetJSONLPathChecked(peers []*Instance) (string, error) {
	if i.ClaudeSessionIDCollidesWith(peers) {
		_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
			InstanceID: i.ID, Tool: i.Tool, Action: "reject",
			Source: "jsonl_resolve", OldID: i.ClaudeSessionID, Candidate: i.ClaudeSessionID,
			Reason: "claude_session_id_collision_across_live_instances",
		})
		return "", fmt.Errorf("claude_session_id %q is shared by more than one live instance; refusing to resolve a colliding transcript path for instance %s", i.ClaudeSessionID, i.ID)
	}
	return i.GetJSONLPath(), nil
}

// resolveClaudeTranscriptPath returns the path to the Claude JSONL transcript for
// the given session, or "" if it cannot be found. It first tries the projects/
// subdirectory whose name Claude derives from the project path; if that misses, it
// falls back to locating the transcript by its uniquely-named <sessionID>.jsonl
// anywhere under projects/.
//
// The fallback matters on WSL: agent-deck stores a Linux project path (e.g.
// /home/user or /mnt/d/proj), but Claude Code runs as a Windows-native process and
// names its project directory from the Windows/UNC form of the cwd (e.g.
// \\wsl.localhost\Ubuntu\home\user -> --wsl-localhost-Ubuntu-home-user, or
// D:\proj -> D--proj). The two encodings never match, so the computed path misses
// and analytics / last-response silently break. The session ID is a UUID, so
// matching on the filename is unambiguous.
func resolveClaudeTranscriptPath(configDir, projectPath, sessionID string, additionalPaths ...string) string {
	if sessionID == "" {
		return ""
	}
	// The ID becomes a file name and a glob pattern: a separator or ".." could
	// name a file outside the projects tree and a glob metacharacter could
	// match another session's transcript, so such an ID resolves to nothing.
	fileName, err := conversationPathComponent(sessionID + ".jsonl")
	if err != nil || strings.ContainsAny(sessionID, "*?[") {
		return ""
	}

	projectsDir := filepath.Join(configDir, "projects")

	// Try ProjectPath first, then other known working directories (multi-repo
	// sessions launch in EffectiveWorkingDir). All exact candidates precede the
	// glob so a cached miss cannot delay a new transcript at a known location.
	// Resolve symlinks first, but retain the original encoding for copied
	// transcripts and to keep same-ID projects distinct (issue #1720).
	var candidateDirs []string
	for index, path := range append([]string{projectPath}, additionalPaths...) {
		if path == "" || (index > 0 && path == projectPath) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
			candidateDirs = append(candidateDirs, resolved)
		}
		candidateDirs = append(candidateDirs, path)
	}
	var candidates []string
	for _, candidateDir := range candidateDirs {
		if candidateDir == "" {
			continue
		}
		projectDir, ok := claudeProjectDir(configDir, ConvertToClaudeDirName(candidateDir))
		if !ok {
			continue
		}
		candidates = append(candidates, filepath.Join(projectDir, fileName))
	}
	if candidate := newestTranscriptCopy(candidates); candidate != "" {
		return candidate
	}

	// Fallback: the transcript may live under a differently-encoded directory name
	// (notably WSL Linux path vs. Windows/UNC cwd). Locate it by its unique
	// session-id filename. A UUID contains no glob metacharacters.
	return globClaudeTranscript(projectsDir, sessionID)
}

// newestTranscriptCopy returns the most recently written regular file among
// paths, or "" when none exists. Ties keep the earlier path. After
// switch-account the destination holds the conversation under every project
// key, and the restarted Claude appends to one of them: the first key that
// exists can be a copy frozen at the switch (LGFALSE1019).
func newestTranscriptCopy(paths []string) string {
	best := ""
	var bestMod time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = path, info.ModTime()
		}
	}
	return best
}

// transcriptGlob is filepath.Glob; tests count its calls.
var transcriptGlob = filepath.Glob

// transcriptGlobMissTTL bounds how long a glob that found nothing is reused.
// The glob lists every project directory (hundreds on a real host) and stats
// each one; the notify-daemon resolved the same missing transcript several
// times per pass, every second, which was most of its CPU (issue #2481).
const transcriptGlobMissTTL = 10 * time.Second

type transcriptGlobEntry struct {
	path    string
	checked time.Time
}

var (
	transcriptGlobMu    sync.Mutex
	transcriptGlobCache = map[string]transcriptGlobEntry{}
)

// globClaudeTranscript is the glob fallback of resolveClaudeTranscriptPath,
// memoized: a found path is reused while it still exists, a miss for
// transcriptGlobMissTTL. The exact-path checks above run on every call, so a
// transcript at its normal location is never delayed by a cached miss. A new
// transcript under a non-exact encoding (notably WSL) may take up to 10 s to
// discover. The key includes the config directory and session ID, so restarting
// with a new ID cannot reuse the previous transcript. Concurrent cold lookups
// may duplicate a glob; filesystem I/O stays outside the cache mutex.
func globClaudeTranscript(projectsDir, sessionID string) string {
	key := projectsDir + "\x00" + sessionID
	now := time.Now()
	transcriptGlobMu.Lock()
	entry, ok := transcriptGlobCache[key]
	transcriptGlobMu.Unlock()
	if ok {
		if entry.path != "" {
			if _, err := os.Stat(entry.path); err == nil {
				return entry.path
			}
		} else if now.Sub(entry.checked) < transcriptGlobMissTTL {
			return ""
		}
	}

	found := ""
	if matches, err := transcriptGlob(filepath.Join(projectsDir, "*", sessionID+".jsonl")); err == nil && len(matches) > 0 {
		found = matches[0]
	}
	transcriptGlobMu.Lock()
	// A lookup may finish after another caller has cached a hit. Preserve that
	// hit, while still allowing a missing file to invalidate the entry we read.
	if latest := transcriptGlobCache[key]; found == "" && latest.path != "" && latest != entry {
		transcriptGlobMu.Unlock()
		return ""
	}
	if len(transcriptGlobCache) >= 4096 {
		clear(transcriptGlobCache) // bound: one entry per session id resolved
	}
	transcriptGlobCache[key] = transcriptGlobEntry{path: found, checked: now}
	transcriptGlobMu.Unlock()
	return found
}

// GetJSONLPath returns the path to the Claude session JSONL file for analytics.
// Returns empty string if this is not a Claude session or the transcript is absent.
//
// A remote session's transcript is never absent-or-here: it is on the remote
// host, so "" is the only truthful answer (see TranscriptIsResolvableLocally).
// Resolving it through the local placeholder ProjectPath would hand a LOCAL
// session's conversation to every caller of this function — analytics, the
// transition notifier, the TUI, and GetJSONLPathChecked.
func (i *Instance) GetJSONLPath() string {
	if !IsClaudeCompatible(i.Tool) || i.ClaudeSessionID == "" {
		return ""
	}
	if !i.TranscriptIsResolvableLocally() {
		return ""
	}
	// The instance's own config dir first (account/conductor/group, #2769): a
	// session scoped to an account writes its transcript there, and the
	// process-wide dir never has it. The process-wide dir stays as a fallback
	// for transcripts written before an account switch.
	instanceDir := GetClaudeConfigDirForInstance(i)
	if path := resolveClaudeTranscriptPath(instanceDir, i.ProjectPath, i.ClaudeSessionID, i.EffectiveWorkingDir()); path != "" {
		return path
	}
	if globalDir := GetClaudeConfigDir(); globalDir != instanceDir {
		return resolveClaudeTranscriptPath(globalDir, i.ProjectPath, i.ClaudeSessionID, i.EffectiveWorkingDir())
	}
	return ""
}

// ResolveClaudeTranscriptPath returns the path to the Claude JSONL transcript
// for a session id under a specific config dir, or "" when none exists.
// Optional additional working directories are exact candidates before the glob.
//
// It is exported for callers that must resolve a transcript against a
// PER-INSTANCE config dir. GetJSONLPath resolves against the process-wide
// GetClaudeConfigDir(), which agent-deck's account/conductor/group scoping and
// per-session scratch homes make frequently wrong for a given instance.
func ResolveClaudeTranscriptPath(configDir, projectPath, sessionID string, additionalPaths ...string) string {
	if configDir == "" || sessionID == "" {
		return ""
	}
	return resolveClaudeTranscriptPath(configDir, projectPath, sessionID, additionalPaths...)
}

// getClaudeLastResponse extracts the last assistant message from Claude's JSONL file
func (i *Instance) getClaudeLastResponse() (*ResponseOutput, error) {
	// Require stored session ID - no fallback to file scanning
	if i.ClaudeSessionID == "" {
		return nil, fmt.Errorf("no Claude session ID available for this instance")
	}

	// The conversation of an --ssh session is on the remote host; reading the
	// controller's disk for it returns a LOCAL session's reply (#1851).
	if !i.TranscriptIsResolvableLocally() {
		return nil, fmt.Errorf("instance %s runs on %s; its Claude transcript is not on this machine", i.ID, i.SSHHost)
	}

	sessionFile := resolveClaudeTranscriptPath(GetClaudeConfigDir(), i.ProjectPath, i.ClaudeSessionID, i.EffectiveWorkingDir())
	if sessionFile == "" {
		return nil, fmt.Errorf("session file not found for claude_session_id %s", i.ClaudeSessionID)
	}

	// Read and parse the JSONL file
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	return parseClaudeLastAssistantMessage(data, filepath.Base(sessionFile))
}

// findLatestClaudeTranscriptOnDisk scans the instance's Claude project directory
// for the most recently modified transcript that carries a real (non-sidechain)
// assistant message, returning its session ID and parsed response.
//
// This recovers from a stale CLAUDE_SESSION_ID: when a Claude session rolls over
// (/clear or compaction starts a NEW transcript), the tmux env var — fixed at
// launch — still points at the OLD, now-empty transcript. Without this fallback
// the read path drops to raw tmux-pane parsing, which leaks tool output (e.g. a
// `list --json` dump) into conductor chat replies. Mirrors the Gemini
// syncGeminiSessionFromDisk fallback.
//
// Returns ("", nil) when no suitable transcript is found.
//
// It is the third door into "resolve a conversation against local disk", and the
// most dangerous one: its caller WRITES the recovered id back onto the instance
// and into the tmux pane's environment. For an --ssh session that would adopt a
// LOCAL conversation durably, so the next restart reaches
// sweepDuplicateToolSessions holding a local session's CLAUDE_SESSION_ID and
// kills it — #1851's data loss, reached past the restart bail-out (which returns
// early once ClaudeSessionID is non-empty). Hence the same unconditional gate the
// other two doors have.
func (i *Instance) findLatestClaudeTranscriptOnDisk() (string, *ResponseOutput) {
	if !i.TranscriptIsResolvableLocally() {
		return "", nil
	}

	configDir := GetClaudeConfigDir()

	resolvedPath := i.ProjectPath
	if resolved, err := filepath.EvalSymlinks(i.ProjectPath); err == nil {
		resolvedPath = resolved
	}
	projectDir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolvedPath))

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", nil
	}

	type candidate struct {
		id  string
		mod time.Time
	}
	var candidates []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{
			id:  strings.TrimSuffix(e.Name(), ".jsonl"),
			mod: info.ModTime(),
		})
	}

	// Newest first; the current conversation is the most recently written file
	// that still contains a real assistant reply.
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].mod.After(candidates[b].mod)
	})

	for _, c := range candidates {
		// Issue #2299: this scan exists to recover THIS instance's own
		// conversation after it rolls over (/clear or compaction), not to
		// adopt some other session's transcript that merely lives in the
		// same project directory. A transcript last written before this
		// instance's own LastStartedAt cannot be a rollover of a
		// conversation that instance hasn't started yet — it predates this
		// run entirely, so a pure newest-mtime pick would otherwise hand a
		// brand-new, never-messaged session another session's reply. Zero
		// LastStartedAt (legacy record, or a test that never calls Start())
		// means "unknown" and does not filter, preserving prior recovery
		// behavior for those records.
		if !i.LastStartedAt.IsZero() && c.mod.Before(i.LastStartedAt) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(projectDir, c.id+".jsonl"))
		if err != nil {
			continue
		}
		// ConvertToClaudeDirName is lossy: it maps every non-alphanumeric byte to
		// a hyphen, so two project paths that share a prefix but differ only at a
		// separator (e.g. /a/b and /a-b) encode to the SAME projects/ subdir. A
		// sibling session's transcript then sits alongside this instance's, and a
		// pure newest-mtime pick would cross-route the sibling's reply into this
		// session's output. Claude stamps the absolute cwd on its records, so when
		// a transcript declares a cwd that is NOT this instance's project path we
		// skip it — the mtime scan must never return a proven-foreign transcript.
		if cwd, ok := claudeTranscriptCWD(data); ok && !sameClaudeCWD(cwd, resolvedPath) {
			continue
		}
		resp, err := parseClaudeLastAssistantMessage(data, c.id+".jsonl")
		if err == nil && resp != nil && strings.TrimSpace(resp.Content) != "" {
			return c.id, resp
		}
	}
	return "", nil
}

// claudeTranscriptCWD returns the working directory Claude Code recorded in a
// transcript, scanning only the leading records (the cwd is stamped on every
// record, so the first one that carries it is authoritative). It returns
// ("", false) when no record within the scan window declares a cwd — e.g. an
// older or truncated transcript — so callers can treat "unknown" as "cannot
// prove foreign" rather than excluding it.
func claudeTranscriptCWD(data []byte) (string, bool) {
	const maxLines = 64
	start := 0
	for lines := 0; start < len(data) && lines < maxLines; lines++ {
		var line []byte
		if nl := bytes.IndexByte(data[start:], '\n'); nl < 0 {
			line = data[start:]
			start = len(data)
		} else {
			line = data[start : start+nl]
			start += nl + 1
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec struct {
			CWD string `json:"cwd"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.CWD != "" {
			return rec.CWD, true
		}
	}
	return "", false
}

// sameClaudeCWD reports whether a transcript-recorded cwd names the same
// directory as the instance's (already symlink-resolved) project path. It
// compares cleaned paths first, then falls back to symlink resolution so a
// transcript that recorded a pre-resolution cwd still matches.
func sameClaudeCWD(transcriptCWD, resolvedProjectPath string) bool {
	if transcriptCWD == resolvedProjectPath {
		return true
	}
	a, b := filepath.Clean(transcriptCWD), filepath.Clean(resolvedProjectPath)
	if a == b {
		return true
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		if ra == b {
			return true
		}
		if rb, err := filepath.EvalSymlinks(b); err == nil && ra == rb {
			return true
		}
	}
	return false
}

// parseClaudeLastAssistantMessage parses a Claude JSONL file to extract the last assistant message.
//
// It anchors at EOF and walks lines BACKWARD over the raw bytes with no
// line-length ceiling (issue #1568): a forward bufio.Scanner capped at 1MB
// stopped silently (bufio.ErrTooLong) at the multi-megabyte single-line
// records that Claude Code /compact inserts mid-file (file-history-snapshot,
// compact summaries), so every post-compact reply was invisible and the last
// PRE-compact assistant text was returned forever.
func parseClaudeLastAssistantMessage(data []byte, sessionID string) (*ResponseOutput, error) {
	// JSONL record structure (same as global_search.go)
	type claudeMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type claudeRecord struct {
		SessionID   string          `json:"sessionId"`
		Type        string          `json:"type"`
		Message     json.RawMessage `json:"message"`
		Timestamp   string          `json:"timestamp"`
		IsSidechain bool            `json:"isSidechain"`
		UUID        string          `json:"uuid"`
	}

	var lastAssistantContent string
	var lastTimestamp string
	var lastUUID string
	var foundSessionID string

	// Walk lines from EOF backward; the first non-sidechain assistant record
	// with text content is the newest reply.
	for end := len(data); end > 0; {
		nl := bytes.LastIndexByte(data[:end], '\n')
		line := bytes.TrimSpace(data[nl+1 : end])
		end = nl
		if nl < 0 {
			end = 0
		}
		if len(line) == 0 {
			continue
		}

		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue // Skip malformed lines
		}

		// Skip subagent sidechain records: the conversation's "last response"
		// is the parent agent's reply, not a Task subagent's output.
		if record.IsSidechain {
			continue
		}

		// Capture session ID (backfilled from earlier records when the
		// assistant record itself lacks one).
		if foundSessionID == "" && record.SessionID != "" {
			foundSessionID = record.SessionID
		}

		// Once the newest assistant text is found, keep walking only until a
		// session ID is known.
		if lastAssistantContent != "" {
			if foundSessionID != "" {
				break
			}
			continue
		}

		// Only care about messages
		if len(record.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(record.Message, &msg); err != nil {
			continue
		}

		// Only care about assistant messages
		if msg.Role != "assistant" {
			continue
		}

		// Extract content (can be string or array of blocks)
		var contentStr string
		var extractedText string
		if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
			// Simple string content
			extractedText = contentStr
		} else {
			// Try as array of content blocks
			var blocks []map[string]interface{}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				var sb strings.Builder
				for _, block := range blocks {
					// Check for text type blocks
					if blockType, ok := block["type"].(string); ok && blockType == "text" {
						if text, ok := block["text"].(string); ok {
							sb.WriteString(text)
							sb.WriteString("\n")
						}
					}
				}
				extractedText = strings.TrimSpace(sb.String())
			}
		}
		// Only accept records with actual text content (tool_use-only
		// assistant records are skipped).
		if extractedText != "" {
			lastAssistantContent = extractedText
			lastTimestamp = record.Timestamp
			lastUUID = record.UUID
			if foundSessionID != "" {
				break
			}
		}
	}

	if lastAssistantContent == "" {
		return nil, fmt.Errorf("no assistant response found in session")
	}

	return &ResponseOutput{
		Tool:          "claude",
		Role:          "assistant",
		Content:       lastAssistantContent,
		Timestamp:     lastTimestamp,
		SessionID:     foundSessionID,
		AssistantUUID: lastUUID,
	}, nil
}

// parseClaudeLatestUserPrompt parses a Claude JSONL file to extract the last user message
func parseClaudeLatestUserPrompt(data []byte) (string, error) {
	// JSONL record structure
	type claudeMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type claudeRecord struct {
		Message json.RawMessage `json:"message"`
	}

	var latestPrompt string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Handle large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue // Skip malformed lines
		}

		// Only care about messages
		if len(record.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(record.Message, &msg); err != nil {
			continue
		}

		// Only care about user messages
		if msg.Role != "user" {
			continue
		}

		// Extract content (can be string or array of blocks)
		var contentStr string
		var extractedText string
		if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
			// Simple string content
			extractedText = contentStr
		} else {
			// Try as array of content blocks
			var blocks []map[string]interface{}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				var sb strings.Builder
				for _, block := range blocks {
					if blockType, ok := block["type"].(string); ok && blockType == "text" {
						if text, ok := block["text"].(string); ok {
							sb.WriteString(text)
							sb.WriteString(" ")
						}
					}
				}
				extractedText = strings.TrimSpace(sb.String())
			}
		}

		// Sanitize: strip newlines and extra spaces for single-line display
		if extractedText != "" {
			content := strings.ReplaceAll(extractedText, "\n", " ")
			latestPrompt = strings.Join(strings.Fields(content), " ")
		}
	}

	return latestPrompt, nil
}

// readJSONLTail reads the last user prompt from a JSONL file using tail-read with size caching.
// Instead of reading the entire file (can be 100-800MB), it:
// 1. Stats the file to get current size (cheap syscall)
// 2. Skips reading entirely if size hasn't changed since last check
// 3. Only reads the last 32KB when the file has grown
func (i *Instance) readJSONLTail(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	size := info.Size()

	// If same file and same size, return cached prompt
	if path == i.lastJSONLPath && size == i.lastJSONLSize {
		return i.cachedPrompt
	}

	// File changed or new file - read the tail
	const tailSize int64 = 32 * 1024 // 32KB
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	offset := size - tailSize
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return ""
		}
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	// If we seeked into the middle of the file, skip to the first complete line
	if offset > 0 {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			data = data[idx+1:]
		}
	}

	prompt, err := parseClaudeLatestUserPrompt(data)
	if err != nil || prompt == "" {
		// Update cache even on empty result to avoid re-reading
		i.lastJSONLPath = path
		i.lastJSONLSize = size
		return i.cachedPrompt // Return previous cached value
	}

	i.lastJSONLPath = path
	i.lastJSONLSize = size
	i.cachedPrompt = prompt
	return prompt
}

// parseGeminiLatestUserPrompt parses a Gemini JSON file to extract the last user message
func parseGeminiLatestUserPrompt(data []byte) (string, error) {
	var session struct {
		Messages []struct {
			Type    string `json:"type"` // "user" or "gemini"
			Content string `json:"content"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return "", fmt.Errorf("failed to parse Gemini session: %w", err)
	}

	var latestPrompt string
	// Find last "user" type message
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if msg.Type == "user" {
			// Sanitize: strip newlines and extra spaces for single-line display
			content := strings.ReplaceAll(msg.Content, "\n", " ")
			latestPrompt = strings.Join(strings.Fields(content), " ")
			break
		}
	}

	return latestPrompt, nil
}

// getGeminiLastResponse extracts the last assistant message from Gemini's JSON file
func (i *Instance) getGeminiLastResponse() (*ResponseOutput, error) {
	// Require stored session ID - no fallback to file scanning
	if i.GeminiSessionID == "" || len(i.GeminiSessionID) < 8 {
		return nil, fmt.Errorf("no Gemini session ID available for this instance")
	}

	sessionsDir := GetGeminiSessionsDir(i.ProjectPath)

	// Find file by session ID (first 8 chars in filename)
	// Filename format is session-YYYY-MM-DDTHH-MM-<uuid8>.json
	pattern := filepath.Join(sessionsDir, "session-*-"+i.GeminiSessionID[:8]+".json")
	files, _ := filepath.Glob(pattern)

	// Fallback: cross-project search if not found in expected location
	if len(files) == 0 {
		if fallbackPath := findGeminiSessionInAllProjects(i.GeminiSessionID); fallbackPath != "" {
			files = []string{fallbackPath}
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("session file not found for ID: %s", i.GeminiSessionID)
	}
	sessionFile := files[0]

	// Read and parse the JSON file
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	return parseGeminiLastAssistantMessage(data)
}

// parseGeminiLastAssistantMessage parses a Gemini JSON file to extract the last assistant message
// VERIFIED: Message type is "gemini" (NOT role: "assistant")
func parseGeminiLastAssistantMessage(data []byte) (*ResponseOutput, error) {
	var session struct {
		SessionID string `json:"sessionId"` // VERIFIED: camelCase
		Messages  []struct {
			ID        string            `json:"id"`
			Timestamp string            `json:"timestamp"`
			Type      string            `json:"type"` // VERIFIED: "user" or "gemini"
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"toolCalls,omitempty"`
			Thoughts  []json.RawMessage `json:"thoughts,omitempty"`
			Model     string            `json:"model,omitempty"`
			Tokens    json.RawMessage   `json:"tokens,omitempty"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	// Find last "gemini" type message
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if msg.Type == "gemini" {
			return &ResponseOutput{
				Tool:      "gemini",
				Role:      "assistant",
				Content:   msg.Content,
				Timestamp: msg.Timestamp,
				SessionID: session.SessionID,
			}, nil
		}
	}

	return nil, fmt.Errorf("no assistant response found in session")
}

func (i *Instance) getPiLastResponse() (*ResponseOutput, error) {
	if i.IsSSH() || i.IsSandboxed() {
		return nil, fmt.Errorf("instance %s runs outside this host; its Pi transcript is not on this machine", i.ID)
	}
	dir, err := sandboxguard.HomeJoin("Pi instance session dir (~/.pi/agent-deck)", ".pi", "agent-deck", i.ID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var latest string
	var latestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err == nil && (latest == "" || info.ModTime().After(latestTime)) {
			latest = filepath.Join(dir, entry.Name())
			latestTime = info.ModTime()
		}
	}
	if latest == "" {
		return nil, fmt.Errorf("no Pi session transcript found")
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		return nil, err
	}
	return parsePiLastAssistantMessage(data)
}

func parsePiLastAssistantMessageLinear(data []byte) (*ResponseOutput, error) {
	type contentPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var sessionID string
	var last *ResponseOutput
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role    string        `json:"role"`
				Content []contentPart `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if event.Type == "session" {
			sessionID = event.ID
			continue
		}
		if event.Type != "message" || event.Message.Role != "assistant" {
			continue
		}
		var text []string
		for _, part := range event.Message.Content {
			if part.Type == "text" {
				text = append(text, part.Text)
			}
		}
		if len(text) != 0 {
			last = &ResponseOutput{
				Tool: "pi", Role: "assistant", Content: strings.Join(text, "\n"),
				Timestamp: event.Timestamp, SessionID: sessionID,
			}
		}
	}
	if last == nil {
		return nil, fmt.Errorf("no assistant response found in Pi session")
	}
	return last, nil
}

// getTerminalLastResponse extracts the last response from terminal output
// This is used for Gemini, Codex, and other tools without structured output
func (i *Instance) getTerminalLastResponse() (*ResponseOutput, error) {
	if i.tmuxSession == nil {
		return nil, fmt.Errorf("tmux session not initialized")
	}

	// Capture full history
	content, err := i.tmuxSession.CaptureFullHistory()
	if err != nil {
		return nil, fmt.Errorf("failed to capture terminal output: %w", err)
	}

	// Parse based on tool type
	switch {
	case i.Tool == "gemini":
		return parseGeminiOutput(content)
	case IsCodexCompatible(i.Tool):
		return parseCodexOutput(content)
	default:
		return parseGenericOutput(content, i.Tool)
	}
}

// parseGeminiOutput parses Gemini CLI output to extract the last response
func parseGeminiOutput(content string) (*ResponseOutput, error) {
	content = tmux.StripANSI(content)
	lines := strings.Split(content, "\n")

	// Gemini typically shows responses after "▸" prompt and before the next ">"
	// Look for response blocks in reverse order
	var responseLines []string
	inResponse := false

	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := lines[idx]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines at the end
		if trimmed == "" && !inResponse {
			continue
		}

		// Detect prompt line (end of response when reading backwards)
		// Common prompts: "> ", ">>> ", "$", "❯", "➜"
		isPrompt := geminiPromptRE.MatchString(trimmed)

		if isPrompt && inResponse {
			// We've found the start of the response block
			break
		}

		// Detect user input line (also marks start of assistant response when reading backwards)
		if strings.HasPrefix(trimmed, "> ") && len(trimmed) > 5 && inResponse {
			break
		}

		// We're in a response
		inResponse = true
		responseLines = append([]string{line}, responseLines...)
	}

	if len(responseLines) == 0 {
		return nil, fmt.Errorf("no response found in Gemini output")
	}

	// Clean up the response
	response := strings.TrimSpace(strings.Join(responseLines, "\n"))

	return &ResponseOutput{
		Tool:    "gemini",
		Role:    "assistant",
		Content: response,
	}, nil
}

// parseCodexOutput parses OpenAI Codex CLI output
func parseCodexOutput(content string) (*ResponseOutput, error) {
	// Codex has similar structure - adapt as needed
	return parseGenericOutput(content, "codex")
}

// parseGenericOutput is a fallback parser for unknown tools
func parseGenericOutput(content, tool string) (*ResponseOutput, error) {
	content = tmux.StripANSI(content)
	lines := strings.Split(content, "\n")

	// Look for the last substantial block of text (more than 2 lines)
	// before a prompt character
	var responseLines []string
	inResponse := false
	promptPattern := shellPromptRE

	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := lines[idx]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines at the end
		if trimmed == "" && !inResponse {
			continue
		}

		// Detect prompt line
		if promptPattern.MatchString(trimmed) {
			if inResponse {
				break
			}
			continue
		}

		inResponse = true
		responseLines = append([]string{line}, responseLines...)

		// Stop if we've collected enough lines (limit to prevent huge outputs)
		if len(responseLines) > 500 {
			break
		}
	}

	if len(responseLines) == 0 {
		return nil, fmt.Errorf("no response found in terminal output")
	}

	// Clean up
	response := strings.TrimSpace(strings.Join(responseLines, "\n"))

	return &ResponseOutput{
		Tool:    tool,
		Role:    "assistant",
		Content: response,
	}, nil
}

// geminiSessionHasConversationData checks whether a Gemini session file exists
// and contains at least one message record.
//
// Returns true on read/parse errors as a safe fallback, matching Claude quality-gate
// behavior (avoid dropping potentially valid sessions due to transient I/O issues).
func geminiSessionHasConversationData(sessionID, projectPath string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) < 8 {
		return false
	}

	sessionsDir := GetGeminiSessionsDir(projectPath)
	pattern := filepath.Join(sessionsDir, "session-*-"+sessionID[:8]+".json")
	filePath, _ := findNewestFile(pattern)
	if filePath == "" {
		filePath = findGeminiSessionInAllProjects(sessionID)
	}
	if filePath == "" {
		return false
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return true
	}

	var payload struct {
		SessionID string            `json:"sessionId"`
		Messages  []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return true
	}

	// If full ID is present in file and mismatches, treat candidate as invalid.
	if payload.SessionID != "" && payload.SessionID != sessionID {
		return false
	}
	return len(payload.Messages) > 0
}

// sessionConversationByteSize returns the size in bytes of the Claude
// session's jsonl file (or 0 if it cannot be located). Used as a robust
// "how much history does this session hold" proxy when choosing between
// two non-empty candidates during hook rebind — a 974KB historic jsonl
// should always win over a fresh 1-record jsonl, regardless of whether
// both pass the binary `sessionHasConversationData` check.
//
// Uses the PER-INSTANCE Claude config dir (same lookup as
// sessionHasConversationData) so conductors with config_dir overrides
// resolve correctly. Errors return 0 — this is a tiebreaker, not the
// primary gate, so a missing file degrades gracefully to "candidate
// doesn't appear larger, reject" rather than false-accepting.
func sessionConversationByteSize(inst *Instance, sessionID string) int64 {
	// #1851: see sessionHasConversationData — a remote session's transcript is
	// not in this filesystem, so any size found here belongs to someone else.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return 0
	}
	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	// Issue #663: for multi-repo sessions ProjectPath is a symlink into
	// MultiRepoTempDir; EvalSymlinks would resolve away from the parent
	// dir Claude actually used as cwd. EffectiveWorkingDir() is the
	// authoritative cwd for JSONL encoding.
	projectPath := ""
	if inst != nil {
		projectPath = inst.EffectiveWorkingDir()
	}
	fileName, err := conversationPathComponent(sessionID + ".jsonl")
	if err != nil {
		return 0
	}
	// The transcript is append-only, so the largest copy holds the most
	// history. Taking the size of the newest copy instead would let a touch
	// of a stale short copy shrink the bound conversation and flip the gate
	// from reject to rebind.
	if size, ok := largestTranscriptSize(conversationKeyPaths(configDir, projectPath, fileName)); ok {
		return size
	}
	if fallback := findSessionFileInAllProjects(inst, sessionID); fallback != "" {
		if info, err := os.Stat(fallback); err == nil {
			return info.Size()
		}
	}
	return 0
}

// conversationKeyPaths lists fileName under every project key of projectPath
// in configDir: the path as typed, its macOS /private form and its realpath
// (claudeProjectKeyCandidates). After switch-account several can hold the
// conversation and only one is still written, so the rebind gate takes the
// size from the largest copy (largestTranscriptSize) and the mtime from the
// newest (newestTranscriptCopy).
func conversationKeyPaths(configDir, projectPath, fileName string) []string {
	return conversationKeyPathsWith(configDir, projectPath, fileName, runtime.GOOS, filepath.EvalSymlinks)
}

// conversationKeyPathsWith is the testable form of conversationKeyPaths; goos
// and resolve are passed to claudeProjectKeyCandidatesWith.
func conversationKeyPathsWith(configDir, projectPath, fileName, goos string, resolve func(string) (string, error)) []string {
	keys := claudeProjectKeyCandidatesWith(projectPath, goos, resolve)
	if len(keys) == 0 {
		keys = []string{"-"}
	}
	paths := make([]string, 0, len(keys))
	for _, key := range keys {
		paths = append(paths, filepath.Join(configDir, "projects", key, fileName))
	}
	return paths
}

// largestTranscriptSize returns the size of the largest regular file among
// paths; ok is false when none exists.
func largestTranscriptSize(paths []string) (size int64, ok bool) {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if !ok || info.Size() > size {
			size, ok = info.Size(), true
		}
	}
	return size, ok
}

// sessionConversationMtime returns the modification time of the Claude
// session's jsonl file (or the zero time if it cannot be located). Issue
// #856: when both current and candidate jsonls have data, mtime gap is the
// discriminator between a stale flap (rich session still being actively
// written, candidate is a momentary blip) and a user-initiated new session
// like /clear (rich session is dormant, candidate is the new active jsonl).
// Path resolution mirrors sessionConversationByteSize.
func sessionConversationMtime(inst *Instance, sessionID string) time.Time {
	// #1851: see sessionHasConversationData.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return time.Time{}
	}
	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	projectPath := ""
	if inst != nil {
		projectPath = inst.EffectiveWorkingDir()
	}
	fileName, err := conversationPathComponent(sessionID + ".jsonl")
	if err != nil {
		return time.Time{}
	}
	if sessionFile := newestTranscriptCopy(conversationKeyPaths(configDir, projectPath, fileName)); sessionFile != "" {
		if info, err := os.Stat(sessionFile); err == nil {
			return info.ModTime()
		}
	}
	if fallback := findSessionFileInAllProjects(inst, sessionID); fallback != "" {
		if info, err := os.Stat(fallback); err == nil {
			return info.ModTime()
		}
	}
	return time.Time{}
}

// sessionHasConversationData checks if a Claude session file contains actual
// conversation data (has "sessionId" field in records).
//
// Uses the PER-INSTANCE Claude config dir (via GetClaudeConfigDirForInstance)
// so sessions with [conductors.<name>.claude].config_dir or
// [groups."<group>".claude].config_dir overrides detect their own JSONL
// history correctly. Prior versions (≤v1.7.6) consulted the process-wide
// GetClaudeConfigDir(), which silently ignored per-instance overrides and
// caused restart-with-history to mis-route to --session-id (Claude rejects
// that as "already in use") instead of --resume. Passing inst == nil
// degrades to the global lookup, preserving legacy call sites without an
// Instance.
//
// Returns true if:
// - File has any "sessionId" field (user interacted with session)
// - Error reading file (safe fallback - don't risk losing sessions)
//
// Returns false if:
// - File doesn't exist (nothing to resume, use --session-id)
// - File exists but has zero "sessionId" occurrences (never interacted)
// It answers a LOCAL-DISK question. Callers deciding "start fresh or resume?"
// must use conversationIsResumable instead — see its doc comment for why a hard
// false is the wrong answer for a remote session.
func sessionHasConversationData(inst *Instance, sessionID string) bool {
	// #1851: for an --ssh session every path below is a directory on THIS
	// machine, keyed on the local placeholder ProjectPath — so "yes, that id has
	// conversation data" would be a statement about a LOCAL session's transcript.
	// The honest answer for a conversation that lives on another host is "not
	// here"; the remote harness makes its own resume decision on its own disk.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return false
	}
	// Build the session file path
	// Format: {config_dir}/projects/{encoded_path}/{sessionID}.jsonl
	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}

	// Issue #663: see sessionConversationByteSize rationale above.
	// Multi-repo sessions must encode EffectiveWorkingDir(), not the
	// ProjectPath symlink.
	projectPath := ""
	if inst != nil {
		projectPath = inst.EffectiveWorkingDir()
	}

	// Resolve symlinks in project path (macOS: /tmp -> /private/tmp)
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}

	// Encode project path using Claude's directory format
	encodedPath := ConvertToClaudeDirName(resolvedPath)
	if encodedPath == "" {
		encodedPath = "-"
	}

	fileName, err := conversationPathComponent(sessionID + ".jsonl")
	if err != nil {
		return false
	}
	sessionFile := filepath.Join(configDir, "projects", encodedPath, fileName)

	// Issue #662 diagnostic contract: emit a single structured "decision"
	// log line per call with every field needed to reconstruct the false
	// negatives in production logs (config_dir, resolved_project_path,
	// encoded_path, primary_path_tested, primary_path_stat_err,
	// fallback_lookup_tried, fallback_path_found, final_result).
	primaryStatErr := ""
	fallbackTried := false
	fallbackPathFound := ""

	emitDecision := func(result bool, reason string) {
		sessionLog.Debug(
			"session_data_decision",
			slog.String("session_id", sessionID),
			slog.String("config_dir", configDir),
			slog.String("resolved_project_path", resolvedPath),
			slog.String("encoded_path", encodedPath),
			slog.String("primary_path_tested", sessionFile),
			slog.String("primary_path_stat_err", primaryStatErr),
			slog.Bool("fallback_lookup_tried", fallbackTried),
			slog.String("fallback_path_found", fallbackPathFound),
			slog.Bool("final_result", result),
			slog.String("reason", reason),
		)
	}

	sessionLog.Debug("session_data_checking_file", slog.String("file", sessionFile))

	// Check if file exists
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		if err != nil {
			primaryStatErr = err.Error()
		}
		// File doesn't exist at expected location - try cross-project search.
		// This handles ENCODING mismatches that still denote the same real
		// directory (symlinked project paths: the primary lookup encodes the
		// resolved path, while Claude may have filed under the unresolved
		// spelling, or vice versa).
		//
		// It must NOT accept a jsonl belonging to a genuinely different
		// project. `claude --resume` only consults the project dir derived
		// from its own cwd, so a foreign-directory hit is not evidence that
		// --resume will succeed — it guarantees the opposite: Claude prints
		// "No conversation found with session ID: <id>" and exits within a
		// second, flapping the session to `error` on every restart forever.
		// Returning false instead routes to --session-id, which starts
		// cleanly in the correct project dir.
		fallbackTried = true
		fallbackPath := findSessionFileInAllProjects(inst, sessionID)
		foreignHit := fallbackPath != "" &&
			!encodesSameWorkingDir(filepath.Base(filepath.Dir(fallbackPath)), projectPath, resolvedPath)
		if fallbackPath != "" {
			fallbackPathFound = fallbackPath
		}
		switch {
		case foreignHit:
			sessionLog.Debug(
				"session_data_cross_project_rejected",
				slog.String("found_path", logging.SanitizeValue(fallbackPath)),
				slog.String("instance_working_dir", logging.SanitizeValue(projectPath)),
				slog.String("result", "use_session_id"),
			)
			emitDecision(false, "foreign_project_dir_resume_would_fail")
			return false
		case fallbackPath == "":
			// File doesn't exist anywhere - use --session-id to create fresh session
			// (there's nothing to resume if the file doesn't exist). The
			// resume-time chokepoint (conversationIsResumable) has already
			// tried to import it from another config dir (#2269) before
			// asking here; this predicate itself stays read-only because the
			// hook/tmux rebind gates call it on live sessions.
			sessionLog.Debug("session_data_file_not_found", slog.String("result", "use_session_id"))
			emitDecision(false, "file_not_found")
			return false
		default:
			sessionLog.Debug("session_data_cross_project_found", slog.String("path", logging.SanitizeValue(fallbackPath)))
			sessionFile = fallbackPath
		}
	}

	sessionLog.Debug("session_data_file_exists", slog.String("file", sessionFile))

	// Read file and search for "sessionId" field
	file, err := os.Open(sessionFile)
	if err != nil {
		// Error opening - safe fallback to --resume
		sessionLog.Debug(
			"session_data_open_error",
			slog.String("error", err.Error()),
			slog.String("fallback", "use_resume"),
		)
		emitDecision(true, "open_error_safe_fallback")
		return true
	}
	defer file.Close()

	// Use scanner to read line by line (memory efficient for large files)
	scanner := bufio.NewScanner(file)
	// Increase buffer size for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		// Simple string search - faster than JSON parsing
		if strings.Contains(line, `"sessionId"`) {
			sessionLog.Debug("session_data_found_session_id", slog.String("result", "use_resume"))
			emitDecision(true, "session_id_line_present")
			return true // Found conversation data
		}
	}

	if err := scanner.Err(); err != nil {
		// Error reading - safe fallback to --resume
		sessionLog.Debug(
			"session_data_scanner_error",
			slog.String("error", err.Error()),
			slog.String("fallback", "use_resume"),
		)
		emitDecision(true, "scanner_error_safe_fallback")
		return true
	}

	// No sessionId found - session was never interacted with
	sessionLog.Debug("session_data_no_session_id", slog.String("result", "use_session_id"))
	emitDecision(false, "no_session_id_line")
	return false
}

// encodesSameWorkingDir reports whether encodedDir — a directory name found
// under {config_dir}/projects — is a plausible Claude encoding of this
// instance's own working directory, given both its literal and
// symlink-resolved spellings.
//
// This gates the cross-project fallback in sessionHasConversationData. The
// fallback globs every project dir, so it happily returns a jsonl belonging to
// an unrelated session. That is worse than not finding one: `claude --resume`
// derives its project dir from its own cwd, so acting on a foreign hit makes
// Claude exit immediately with "No conversation found with session ID: <id>".
// Only an encoding that denotes the SAME real directory (the symlinked-path
// case the fallback exists for) is safe to accept.
func encodesSameWorkingDir(encodedDir, projectPath, resolvedPath string) bool {
	if encodedDir == "" {
		return false
	}
	for _, candidate := range []string{projectPath, resolvedPath} {
		if candidate == "" {
			continue
		}
		if encodedDir == ConvertToClaudeDirName(candidate) {
			return true
		}
	}
	return false
}

// findSessionFileInAllProjects searches all Claude project directories for a session file
// This handles path hash mismatches when agent-deck runs from a different directory
// than where the Claude session was originally created.
// Returns the full path to the session file, or empty string if not found.
//
// CAUTION: a hit here means "this jsonl exists somewhere", NOT "Claude can
// resume it". `claude --resume` only consults the project dir derived from its
// own cwd. Callers deciding between --resume and --session-id must therefore
// gate this result through encodesSameWorkingDir; callers that merely need the
// file's size or mtime (sessionConversationByteSize, sessionConversationMtime)
// can use it unguarded.
// Uses the PER-INSTANCE config dir (via GetClaudeConfigDirForInstance) when
// inst is non-nil so sessions with conductor/group config_dir overrides find
// their own JSONLs. Passing inst == nil degrades to the global lookup.
func findSessionFileInAllProjects(inst *Instance, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	// #1851: this scans EVERY local project dir for <sessionID>.jsonl, so for a
	// remote session it is the widest door of all — it does not even need the
	// placeholder path to collide.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return ""
	}

	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}

	projectsDir := filepath.Join(configDir, "projects")

	// List all project hash directories
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}

	// Session filename format: {sessionID}.jsonl
	sessionFile := sessionID + ".jsonl"

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		filePath := filepath.Join(projectsDir, entry.Name(), sessionFile)
		if _, err := os.Stat(filePath); err == nil {
			return filePath
		}
	}

	return ""
}
