package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Head-parse bounds. The panel runs from a keypress, so every read is capped: a
// corrupt or pathological rollout must cost a bounded amount of memory and time
// rather than whatever the file happens to contain.
const (
	// maxPrefixRecords bounds how far the parser looks for the first model
	// turn. On a real corpus the boundary is reached within a dozen records.
	maxPrefixRecords = 96
	// maxScanRecords bounds the whole scan, including the tail search for the
	// first populated token_count. That record sits after the first assistant
	// message — Codex emits it once the turn's usage is known — so the scan
	// cannot stop at the prefix boundary the way the Claude parser does.
	maxScanRecords = 512
	// maxRecordBytes bounds a single JSONL record. A session_meta record
	// embeds the whole base prompt and a world_state record the whole merged
	// AGENTS.md, so the cap is generous; a record past it is recorded as a
	// warning rather than silently dropped.
	maxRecordBytes = 8 << 20
	// maxScanBytes bounds the cumulative read. Tool output records in the tail
	// are unbounded in principle, and the anchor is not worth an unbounded read.
	maxScanBytes = 64 << 20
	// maxPrefixParts bounds how many injected content parts are retained.
	maxPrefixParts = 256
)

// Rollout record and payload type discriminators, as Codex writes them.
const (
	recordSessionMeta  = "session_meta"
	recordResponseItem = "response_item"
	recordWorldState   = "world_state"
	recordTurnContext  = "turn_context"
	recordEventMsg     = "event_msg"

	payloadMessage       = "message"
	payloadReasoning     = "reasoning"
	payloadTokenCount    = "token_count"
	payloadUserMessage   = "user_message"
	payloadAgentMessage  = "agent_message"
	payloadAgentReason   = "agent_reasoning"
	roleDeveloper        = "developer"
	roleUser             = "user"
	roleAssistant        = "assistant"
	agentsMDPartPrefix   = "# AGENTS.md instructions"
	skillsInstructionTag = "skills_instructions"
	environmentTag       = "environment_context"
	agentsMDTag          = "agents_md"
)

// ContentPart is one block Codex injected into the prefix.
//
// Codex splits a prefix message's content array into one element per injected
// block, each opened by its own marker, so a part is the finest granularity at
// which the harness itself distinguishes content. Pricing at that granularity
// is what lets the report attribute a cost to "your skills catalogue" rather
// than to "a developer message".
type ContentPart struct {
	// Role is the message role the part arrived on, "developer" or "user".
	Role string
	// Tag is the marker the block opens with — "permissions",
	// "skills_instructions", "environment_context" — or [agentsMDTag] for the
	// AGENTS.md block, which opens with a heading rather than a tag. It is
	// empty for an untagged block.
	Tag string
	// Text is the block, verbatim.
	Text string
	// Message and Element locate the part in the rollout: which prefix message
	// it arrived on and which element of that message's content array it was.
	// They make a part's item id stable across runs.
	Message int
	Element int
}

// Chars is the part's length in characters.
func (p ContentPart) Chars() int { return len([]rune(p.Text)) }

// Recognised reports whether Codex marked this part as an injected block.
//
// An unrecognised user-role part is almost always the user's own typed message,
// which is part of the measured first request but is not fixed startup
// overhead. Counting it as overhead would inflate every report of a session
// whose first prompt was long, so unrecognised parts are excluded and named in
// a caveat instead.
func (p ContentPart) Recognised() bool { return p.Role == roleDeveloper || p.Tag != "" }

// WorldState is the state snapshot Codex records alongside the prefix.
//
// It is an attribution key, not a source: its blobs are embedded inside the
// prefix content parts, so pricing both would double count. See the package
// documentation.
type WorldState struct {
	// Keys is the raw key set, retained so a report can name a key this build
	// has not been taught rather than silently ignoring it.
	Keys []string
	// AgentsMD is the merged AGENTS.md text, empty when not recorded.
	AgentsMD string
	// HostSkills is the skills catalogue body, empty when not recorded.
	HostSkills string
	// Environments is the environment snapshot, re-encoded as JSON.
	Environments string
	// EnvironmentsFilesystem is the workspace/permission string inside the
	// environment snapshot, which some releases inline into a developer block.
	EnvironmentsFilesystem string
	// PermissionsDigest is the permissions field when it is a digest rather
	// than text. A digest is a reference to content, not the content, so it is
	// never priced.
	PermissionsDigest string
}

// TurnContext is the per-turn configuration Codex records before the first
// turn.
type TurnContext struct {
	// Model is the model identifier that served the turn.
	Model string
	// CWD is the turn's working directory.
	CWD string
	// CollaborationMode is the active mode name, e.g. "default".
	CollaborationMode string
	// DeveloperInstructions is the mode's injected instruction text, when the
	// release records it.
	DeveloperInstructions string
}

// FirstTurn is the provider-reported accounting for the session's first model
// request — the one measurement in a Codex report that nothing of ours
// produced.
type FirstTurn struct {
	// InputTokens is the measured prompt size.
	InputTokens int
	// CachedInput and CacheWrite split the prompt by cache disposition. They
	// are reported as information; neither changes the prompt's size.
	CachedInput int
	CacheWrite  int
	// ContextWindow is the model's context window as the harness reported it.
	// Zero means the record carried none.
	ContextWindow int
	// TotalInputTokens is the session-cumulative figure from the same record.
	// It equals InputTokens exactly when the record describes the session's
	// first turn, which is the anchor's eligibility test.
	TotalInputTokens int
	// At is the record's timestamp, zero when it carried none.
	At time.Time
	// Source names the exact field path the figure was read from.
	Source string
}

// ColdStart reports whether the record describes the session's first turn.
func (t FirstTurn) ColdStart() bool { return t.TotalInputTokens == t.InputTokens }

// Head is everything the parser found in the rollout's head.
type Head struct {
	// SessionID is the rollout's own session identifier.
	SessionID string
	// CWD is the working directory the session was started in.
	CWD string
	// CLIVersion is the Codex release that wrote the rollout. It is reported
	// because the record format moves between releases.
	CLIVersion string
	// Originator names the front end, e.g. "codex-tui".
	Originator string
	// ThreadSource is "user" for a top-level thread, "subagent" for a spawned
	// one.
	ThreadSource string
	// BaseInstructions is the base system prompt, verbatim. Empty when the
	// release did not record one.
	BaseInstructions string
	// LegacyInstructions is the pre-0.10x session_meta.instructions string,
	// which older releases wrote in place of a base_instructions object.
	LegacyInstructions string
	// Prefix holds the injected content parts, in injection order, excluding
	// the user's own typed message.
	Prefix []ContentPart
	// Excluded holds the prefix-position parts that were not injected context —
	// in practice the typed first message. They are reported in a caveat so the
	// exclusion is visible rather than assumed.
	Excluded []ContentPart
	// TypedUserMessage is the first typed message, as the harness's own
	// user_message event recorded it. It is the authoritative way to tell the
	// typed prompt from an injected block.
	TypedUserMessage string
	// World is the world_state snapshot, or nil.
	World *WorldState
	// Turn is the turn_context snapshot, or nil.
	Turn *TurnContext
	// FirstTurn is the provider accounting for the first model request, or nil.
	FirstTurn *FirstTurn
	// ResumeSignals lists the reasons the first turn does not measure a cold
	// prefix. A non-empty list means no anchor may be claimed.
	ResumeSignals []string
	// LinesRead is how many records were consumed.
	LinesRead int
	// ReachedTurn reports whether the first model turn was found, which is what
	// bounds the prefix.
	ReachedTurn bool
	// Warnings records records that could not be decoded. They surface as
	// caveats: a record we failed to read is a gap in the inventory, and a gap
	// presented as an empty section would be a lie.
	Warnings []string
}

// Anchorable reports whether the first turn measures a cold fixed prefix.
func (h *Head) Anchorable() bool {
	return h != nil && h.FirstTurn != nil && len(h.ResumeSignals) == 0
}

// PartsWithTag returns the injected parts carrying a tag.
func (h *Head) PartsWithTag(tag string) []ContentPart {
	var out []ContentPart
	for _, p := range h.Prefix {
		if p.Tag == tag {
			out = append(out, p)
		}
	}
	return out
}

// rawRecord is the envelope of one rollout line. The payload stays raw because
// its shape depends on the record type.
type rawRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// sessionMetaPayload is the head record. base_instructions is raw because it is
// an object in current releases and was a bare string in older ones.
type sessionMetaPayload struct {
	SessionID        string          `json:"session_id"`
	ID               string          `json:"id"`
	CWD              string          `json:"cwd"`
	Originator       string          `json:"originator"`
	CLIVersion       string          `json:"cli_version"`
	ThreadSource     string          `json:"thread_source"`
	BaseInstructions json.RawMessage `json:"base_instructions"`
	Instructions     string          `json:"instructions"`
}

// responseItemPayload is one item of the model request or response.
type responseItemPayload struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// eventMsgPayload is a UI-level event. info is raw because token_count writes
// null there on releases that predate the usage fields.
type eventMsgPayload struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Info    json.RawMessage `json:"info"`
}

// tokenCountInfo is the provider accounting attached to a token_count event.
type tokenCountInfo struct {
	TotalTokenUsage    *tokenUsage `json:"total_token_usage"`
	LastTokenUsage     *tokenUsage `json:"last_token_usage"`
	ModelContextWindow int         `json:"model_context_window"`
}

// tokenUsage is one usage figure set.
type tokenUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	TotalTokens           int `json:"total_tokens"`
}

// worldStatePayload wraps the state snapshot.
type worldStatePayload struct {
	Full  bool            `json:"full"`
	State json.RawMessage `json:"state"`
}

// turnContextPayload is the per-turn configuration. collaboration_mode is raw
// because a release that writes it as a bare string would otherwise fail the
// whole payload decode and cost us the model identifier too.
type turnContextPayload struct {
	Model                 string          `json:"model"`
	CWD                   string          `json:"cwd"`
	CollaborationMode     json.RawMessage `json:"collaboration_mode"`
	DeveloperInstructions string          `json:"developer_instructions"`
}

// ParseHead reads a Codex rollout's head and returns what the session injected
// before its first turn, plus the provider's measurement of that turn.
//
// The prefix scan stops at the first model turn. The scan then continues, with
// a cheap byte prefilter and no further decoding of irrelevant records, only as
// far as the first populated token_count record — which Codex writes after the
// first assistant message — and stops there.
//
// An error is returned only when the file cannot be opened or read. A record
// that fails to decode is recorded in [Head.Warnings] and reported as a caveat,
// because a silently skipped record would be a hole in the inventory.
func ParseHead(path string) (*Head, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the Codex rollout %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	head := &Head{}
	r := bufio.NewReaderSize(f, 64<<10)
	tokenCountMarker := []byte(payloadTokenCount)
	var scanned int64
	messageIndex := 0

	for head.LinesRead < maxScanRecords && scanned < maxScanBytes {
		line, truncated, rerr := readBoundedLine(r, maxRecordBytes)
		if len(line) == 0 && rerr != nil {
			if rerr == io.EOF {
				break
			}
			return nil, fmt.Errorf("reading the Codex rollout %q: %w", path, rerr)
		}
		head.LinesRead++
		scanned += int64(len(line))

		switch {
		case truncated:
			head.Warnings = append(head.Warnings,
				fmt.Sprintf("record %d exceeds the %d-byte read cap and was not decoded", head.LinesRead, maxRecordBytes))
		case head.ReachedTurn && !bytes.Contains(line, tokenCountMarker):
			// Past the prefix only the anchor is still wanted. Skipping the
			// decode of every tool-output record is what keeps the tail scan
			// affordable on a long conversation.
		default:
			if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
				head.consume(trimmed, &messageIndex)
			}
		}

		if head.ReachedTurn && head.FirstTurn != nil {
			break
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("reading the Codex rollout %q: %w", path, rerr)
		}
	}

	head.finish()
	return head, nil
}

// consume folds one decoded record into the head.
func (h *Head) consume(line string, messageIndex *int) {
	var rec rawRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d is not valid JSON: %v", h.LinesRead, err))
		return
	}

	switch rec.Type {
	case recordSessionMeta:
		h.readSessionMeta(rec)
	case recordWorldState:
		if !h.ReachedTurn {
			h.readWorldState(rec)
		}
	case recordTurnContext:
		if h.Turn == nil {
			h.readTurnContext(rec)
		}
	case recordResponseItem:
		h.readResponseItem(rec, messageIndex)
	case recordEventMsg:
		h.readEventMsg(rec)
	}
}

// readSessionMeta reads the rollout's identity and its base prompt.
func (h *Head) readSessionMeta(rec rawRecord) {
	var p sessionMetaPayload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: the session_meta payload could not be decoded: %v", h.LinesRead, err))
		return
	}
	if h.SessionID == "" {
		h.SessionID = firstNonEmpty(p.SessionID, p.ID)
	}
	h.CWD = firstNonEmpty(h.CWD, p.CWD)
	h.CLIVersion = firstNonEmpty(h.CLIVersion, p.CLIVersion)
	h.Originator = firstNonEmpty(h.Originator, p.Originator)
	h.ThreadSource = firstNonEmpty(h.ThreadSource, p.ThreadSource)
	h.LegacyInstructions = firstNonEmpty(h.LegacyInstructions, p.Instructions)
	if text, ok := decodeTextField(p.BaseInstructions); ok {
		h.BaseInstructions = firstNonEmpty(h.BaseInstructions, text)
	} else if len(p.BaseInstructions) > 0 && !isJSONNull(p.BaseInstructions) {
		h.Warnings = append(h.Warnings,
			fmt.Sprintf("record %d: base_instructions is present in a shape this build does not read, so the base system prompt is reported as absent rather than guessed", h.LinesRead))
	}
}

// readWorldState reads the state snapshot used to attribute content within the
// prefix parts.
func (h *Head) readWorldState(rec rawRecord) {
	var p worldStatePayload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: the world_state payload could not be decoded: %v", h.LinesRead, err))
		return
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(p.State, &state); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: the world_state snapshot could not be decoded: %v", h.LinesRead, err))
		return
	}

	ws := &WorldState{Keys: sortedKeys(state)}
	if raw, ok := state["agents_md"]; ok {
		ws.AgentsMD, _ = decodeTextField(raw)
	}
	if raw, ok := state["host_skills"]; ok {
		ws.HostSkills, _ = decodeBodyField(raw)
	}
	if raw, ok := state["environments"]; ok && !isJSONNull(raw) {
		ws.Environments = string(raw)
		var env struct {
			Filesystem string `json:"filesystem"`
		}
		if err := json.Unmarshal(raw, &env); err == nil {
			ws.EnvironmentsFilesystem = env.Filesystem
		}
	}
	if raw, ok := state["permissions"]; ok {
		var digest string
		if err := json.Unmarshal(raw, &digest); err == nil {
			ws.PermissionsDigest = digest
		}
	}
	h.World = ws
}

// readTurnContext reads the per-turn configuration.
func (h *Head) readTurnContext(rec rawRecord) {
	var p turnContextPayload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: the turn_context payload could not be decoded: %v", h.LinesRead, err))
		return
	}
	tc := &TurnContext{Model: p.Model, CWD: p.CWD, DeveloperInstructions: p.DeveloperInstructions}
	if len(p.CollaborationMode) > 0 && !isJSONNull(p.CollaborationMode) {
		var mode struct {
			Mode     string `json:"mode"`
			Settings struct {
				DeveloperInstructions string `json:"developer_instructions"`
			} `json:"settings"`
		}
		if err := json.Unmarshal(p.CollaborationMode, &mode); err == nil {
			tc.CollaborationMode = mode.Mode
			tc.DeveloperInstructions = firstNonEmpty(tc.DeveloperInstructions, mode.Settings.DeveloperInstructions)
		} else if err := json.Unmarshal(p.CollaborationMode, &tc.CollaborationMode); err != nil {
			h.Warnings = append(h.Warnings,
				fmt.Sprintf("record %d: turn_context.collaboration_mode is in a shape this build does not read", h.LinesRead))
		}
	}
	h.Turn = tc
}

// readResponseItem collects the prefix content parts and detects the boundary.
func (h *Head) readResponseItem(rec rawRecord, messageIndex *int) {
	var p responseItemPayload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: the response_item payload could not be decoded: %v", h.LinesRead, err))
		return
	}
	if h.ReachedTurn {
		return
	}
	// Reasoning and an assistant message both mean the model has begun to
	// answer, so everything before them — and nothing after — is the prefix.
	if p.Type == payloadReasoning || p.Role == roleAssistant {
		h.ReachedTurn = true
		return
	}
	if p.Type != payloadMessage {
		return
	}
	if h.LinesRead > maxPrefixRecords {
		return
	}

	idx := *messageIndex
	*messageIndex++
	for element, c := range p.Content {
		if len(h.Prefix)+len(h.Excluded) >= maxPrefixParts {
			h.Warnings = append(h.Warnings,
				fmt.Sprintf("the prefix carries more than %d injected blocks; the remainder is not reported", maxPrefixParts))
			return
		}
		text := c.Text
		if strings.TrimSpace(text) == "" {
			continue
		}
		part := ContentPart{Role: p.Role, Tag: partTag(text), Text: text, Message: idx, Element: element}
		h.Prefix = append(h.Prefix, part)
	}
}

// readEventMsg reads the typed-message marker and the provider accounting.
func (h *Head) readEventMsg(rec rawRecord) {
	var p eventMsgPayload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: the event_msg payload could not be decoded: %v", h.LinesRead, err))
		return
	}
	switch p.Type {
	case payloadUserMessage:
		if h.TypedUserMessage == "" {
			h.TypedUserMessage = p.Message
		}
	case payloadAgentMessage, payloadAgentReason:
		h.ReachedTurn = true
	case payloadTokenCount:
		h.readTokenCount(rec, p)
	}
}

// readTokenCount reads the first populated accounting record.
//
// A record whose info is null carries no usage at all — older releases emit
// one per turn purely to refresh rate-limit state — so it is skipped rather
// than treated as a measurement of zero.
func (h *Head) readTokenCount(rec rawRecord, p eventMsgPayload) {
	if h.FirstTurn != nil || len(p.Info) == 0 || isJSONNull(p.Info) {
		return
	}
	var info tokenCountInfo
	if err := json.Unmarshal(p.Info, &info); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: the token_count info could not be decoded: %v", h.LinesRead, err))
		return
	}
	last := info.LastTokenUsage
	if last == nil {
		last = info.TotalTokenUsage
	}
	if last == nil {
		return
	}
	turn := &FirstTurn{
		InputTokens:   last.InputTokens,
		CachedInput:   last.CachedInputTokens,
		CacheWrite:    last.CacheWriteInputTokens,
		ContextWindow: info.ModelContextWindow,
		Source:        "event_msg token_count: info.last_token_usage.input_tokens",
	}
	turn.TotalInputTokens = turn.InputTokens
	if info.TotalTokenUsage != nil {
		turn.TotalInputTokens = info.TotalTokenUsage.InputTokens
	}
	if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		turn.At = ts
	}
	h.FirstTurn = turn
	if !turn.ColdStart() {
		h.addResumeSignal(fmt.Sprintf(
			"the first recorded accounting reports %d cumulative prompt tokens against %d for the turn, so this rollout's first turn is not the session's first turn",
			turn.TotalInputTokens, turn.InputTokens))
	}
}

// finish separates the typed message from the injected prefix and records what
// the scan could not complete.
func (h *Head) finish() {
	typed := strings.TrimSpace(h.TypedUserMessage)
	kept := h.Prefix[:0]
	for _, part := range h.Prefix {
		injected := part.Recognised()
		if injected && typed != "" && part.Role == roleUser && strings.TrimSpace(part.Text) == typed {
			// The harness's own user_message event wins over the marker: a
			// typed message that happens to open with a tag is still typed.
			injected = false
		}
		if !injected {
			h.Excluded = append(h.Excluded, part)
			continue
		}
		kept = append(kept, part)
	}
	h.Prefix = kept

	if !h.ReachedTurn && h.LinesRead >= maxScanRecords {
		h.Warnings = append(h.Warnings,
			fmt.Sprintf("no model turn in the first %d records: the injected prefix below may be incomplete", maxScanRecords))
	}
	if h.FirstTurn == nil && h.ReachedTurn {
		h.Warnings = append(h.Warnings,
			"the rollout records a model turn but no populated token_count record was found within the scan budget, so there is no provider-measured total")
	}
}

// addResumeSignal records a reason the first turn cannot serve as an anchor,
// without duplicating a reason already recorded.
func (h *Head) addResumeSignal(reason string) {
	for _, existing := range h.ResumeSignals {
		if existing == reason {
			return
		}
	}
	h.ResumeSignals = append(h.ResumeSignals, reason)
}

// partTag returns the marker an injected block opens with.
//
// Codex opens each block with either an XML-style tag on the first line or, for
// the merged AGENTS.md block, a markdown heading. Anything else is untagged,
// which for a user-role part means it is the typed message.
func partTag(text string) string {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if strings.HasPrefix(trimmed, agentsMDPartPrefix) {
		return agentsMDTag
	}
	if !strings.HasPrefix(trimmed, "<") {
		return ""
	}
	for i := 1; i < len(trimmed); i++ {
		c := trimmed[i]
		if c == '>' || c == ' ' {
			if i == 1 {
				return ""
			}
			return trimmed[1:i]
		}
		isLower := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if !isLower && !isDigit && c != '_' {
			return ""
		}
	}
	return ""
}

// decodeTextField reads a field that is either {"text": "..."} or a bare
// string, which is how base_instructions and agents_md differ across releases.
func decodeTextField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, s != ""
	}
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Text, obj.Text != ""
	}
	return "", false
}

// decodeBodyField reads a field that is either {"body": "..."} or a bare
// string, which is the shape host_skills uses.
func decodeBodyField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, s != ""
	}
	var obj struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Body, obj.Body != ""
	}
	return "", false
}

// isJSONNull reports whether a raw message is the JSON null literal.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// sortedKeys returns a map's keys in a stable order, so a report comparing two
// runs cannot differ by map iteration order alone.
func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// readBoundedLine reads one newline-terminated record, retaining at most max
// bytes and reporting whether the record was longer.
//
// bufio.Scanner cannot be used here: a record over its buffer aborts the whole
// scan, and Codex routinely writes multi-hundred-kilobyte records. An oversized
// record must cost us that record and nothing more. The returned error is
// io.EOF on the final record.
func readBoundedLine(r *bufio.Reader, max int) (line []byte, truncated bool, err error) {
	var buf []byte
	for {
		chunk, rerr := r.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			if room := max - len(buf); room > 0 {
				buf = append(buf, chunk[:room]...)
			}
			truncated = true
		} else {
			buf = append(buf, chunk...)
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		return buf, truncated, rerr
	}
}
