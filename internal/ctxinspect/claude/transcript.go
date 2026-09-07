package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Head-parse bounds. The panel runs from a keypress, so every read is capped:
// a corrupt or pathological transcript must cost a bounded amount of memory and
// time rather than whatever the file happens to contain.
const (
	// maxHeadLines bounds how far the parser looks for the head boundary. On a
	// real corpus the boundary is reached in well under a hundred lines; the cap
	// only fires on a transcript with no assistant turn at all.
	maxHeadLines = 512
	// maxHeadBytes bounds the cumulative read. A tool_result record in the
	// startup lookahead window can carry a megabyte of command output, and the
	// head is not worth an unbounded read.
	maxHeadBytes = 16 << 20
	// maxRecordBytes bounds a single JSONL record. Startup attachments run to a
	// few kilobytes, but a compact_boundary record can carry a whole preserved
	// segment, so the cap is generous and a record that exceeds it is recorded
	// as a warning rather than silently dropped.
	maxRecordBytes = 8 << 20
	// maxStartupLookaheadLines and maxStartupLookaheadTurns bound the search for
	// a startup injection block that lands *after* the transcript's first
	// assistant record. See [Head.consumeAssistant] for why the search exists
	// and how the two bounds were chosen.
	maxStartupLookaheadLines = 48
	maxStartupLookaheadTurns = 2
	// maxNestedMemoryFiles bounds how many captured memory files are retained
	// from the head.
	maxNestedMemoryFiles = 128
)

// Attachment type names Claude Code writes into the transcript.
const (
	attachSkillListing   = "skill_listing"
	attachAgentListing   = "agent_listing_delta"
	attachDeferredTools  = "deferred_tools_delta"
	attachMCPInstruction = "mcp_instructions_delta"
	attachNestedMemory   = "nested_memory"
	attachHookContext    = "hook_additional_context"
)

// SkillListing is the startup skill catalogue, exactly as injected.
type SkillListing struct {
	// Content is the catalogue text: one "- name: description" entry per skill.
	Content string
	// Names are the skills that loaded, in catalogue order.
	Names []string
	// Count is the harness's own count, kept separately so a disagreement with
	// len(Names) is visible rather than reconciled away.
	Count int
	// Initial reports whether the record was the session's first listing. A
	// non-initial delta describes a change, not the startup state.
	Initial bool
}

// AgentListing is the startup agent catalogue, exactly as injected.
type AgentListing struct {
	// Types are the agent identifiers, in catalogue order.
	Types []string
	// Lines are the injected "- name: description" entries, parallel to Types.
	Lines []string
	// Initial reports whether the record was the session's first listing.
	Initial bool
}

// DeferredTools are tools whose names are in the prefix while their schemas
// load only when the agent asks for them.
type DeferredTools struct {
	// Names are the deferred tool names. Only names are injected; the schemas
	// are not, which is why they are priced as names and not as schemas.
	Names []string
}

// MCPInstructions are the instruction blocks MCP servers contributed.
type MCPInstructions struct {
	// Names are the server names, in injection order.
	Names []string
	// Blocks are the verbatim instruction blocks, parallel to Names.
	Blocks []string
}

// FirstTurn is the provider-reported accounting for the session's first model
// request — the one measurement in a Claude report that nothing of ours
// produced.
type FirstTurn struct {
	// InputTokens, CacheCreation and CacheRead are the three components of the
	// request's prompt. Their sum is the whole prompt regardless of how much of
	// it was served from cache.
	InputTokens   int
	CacheCreation int
	CacheRead     int
	// Model is the model that served the turn.
	Model string
	// At is the record's timestamp, zero when it carried none.
	At time.Time
	// Source names the exact field path the figures were read from.
	Source string
}

// PromptTokens is the measured size of the first request's prompt.
func (t FirstTurn) PromptTokens() int { return t.InputTokens + t.CacheCreation + t.CacheRead }

// Head is everything the parser found before the first assistant record.
type Head struct {
	// SessionID is the transcript's own session identifier.
	SessionID string
	// Skills, Agents, Deferred and MCP are the captured startup listings. Each
	// is nil when the session wrote no such record.
	Skills   *SkillListing
	Agents   *AgentListing
	Deferred *DeferredTools
	MCP      *MCPInstructions
	// NestedMemory maps an absolute path to the memory content Claude Code
	// recorded loading for it. It upgrades a reconstructed memory file to a
	// captured one.
	NestedMemory map[string]string
	// FirstTurn is the provider accounting for the first model request, or nil.
	FirstTurn *FirstTurn
	// FirstUserChars is the character count of the first typed user message.
	// It is part of the measured first-turn prompt but not part of the fixed
	// startup overhead, so it is reported rather than attributed.
	FirstUserChars int
	// HookContextChars and HookContextCount describe session-start hook output
	// injected into the prefix. Hooks are user-controlled but agent-deck has no
	// category for them in this version, so they are quantified in a caveat
	// instead of being silently folded into the residual unexplained.
	HookContextChars int
	HookContextCount int
	// ResumeSignals lists the reasons the first turn does not measure a cold
	// prefix. A non-empty list means no anchor may be claimed.
	ResumeSignals []string
	// StartupBlockObserved reports whether the parse actually saw Claude Code's
	// startup injection burst — any of the skill, agent, deferred-tool or MCP
	// listing records. It is the licence to describe something as absent: with
	// the burst on record, a catalogue that is not in it did not load; without
	// it, the same silence only means the parser never got to look.
	StartupBlockObserved bool
	// PreBlockTurns counts the model turns recorded before the startup block.
	// It is non-zero on the transcripts whose catalogues are injected after an
	// initial turn, and it is what makes the advanced anchor explainable rather
	// than merely different.
	//
	// Only a record that reports provider accounting counts. A <synthetic>
	// placeholder carries usage.input_tokens 0 and carried nothing into the
	// prompt, so counting it would put a number into the upper-bound caveat
	// ("also carried 2 earlier model turn(s)") that nothing in the request
	// corresponds to.
	PreBlockTurns int
	// UnusableTurns counts assistant records that were examined for an
	// accounting and could not supply one. It separates "no model turn is on
	// record" from "model turns are on record and none of them is readable",
	// which is the difference between an honest unknown and a fabricated one.
	UnusableTurns int
	// PostBlockSkipped counts assistant records recorded after the startup
	// block that carried no usable accounting and were stepped over rather than
	// taken as the boundary. It is the count behind "the measured turn is not
	// the first record after the injections", and it is zero on a healthy
	// transcript.
	PostBlockSkipped int
	// PostBlockUndecodable counts the subset of those whose message envelope
	// could not be decoded at all. A placeholder went into no request and
	// skipping it costs nothing; a record we simply could not read might have
	// been a real model turn, and the turn that follows it therefore carried its
	// output. That makes the total an upper bound on the fixed prefix, which is
	// a thing the report must say rather than a thing it may assume away.
	PostBlockUndecodable int
	// ModelSeen is the model named by the first assistant record that named one,
	// whether or not that record could supply an accounting. It is the fallback
	// for [FirstTurn.Model] on a session whose readable records are placeholders.
	ModelSeen string
	// LinesRead is how many records were consumed, inclusive of the assistant
	// record that stopped the parse. Asserted by test: the parser must not read
	// past the boundary.
	LinesRead int
	// ReachedAssistant reports whether the boundary record was actually found.
	ReachedAssistant bool
	// Warnings records records that could not be decoded. They are surfaced as
	// caveats: a record we failed to read is a gap in the inventory, and a gap
	// presented as an empty section would be a lie.
	Warnings []string
	// DecodeFailed holds the attachment types whose record was seen in the
	// startup burst and could not be decoded, keyed by the harness's own type
	// string. StartupBlockObserved says the burst happened; this says which
	// part of it we failed to read, which is the difference between "this
	// session has no MCP servers" and "this session's MCP block was
	// unreadable".
	DecodeFailed map[string]bool

	// firstTurnWarned records that an unusable assistant record has already
	// warned, so a run of usage-less pre-block records cannot flood the caveat
	// list. It is deliberately NOT a "we already looked" flag: whether a turn
	// was usable is tracked by FirstTurn itself, so a later real turn still
	// gets its chance. (Setting a single flag before the turn was known usable
	// is what made every session whose transcript opens with <synthetic>
	// zero-usage records report "this session has not completed a model turn
	// yet" while carrying hundreds of them.)
	firstTurnWarned bool
	// firstAssistantLine is the record number of the first assistant record,
	// which is where the bounded startup lookahead starts counting.
	firstAssistantLine int
	// lastTurnUsage and sawTurnUsage collapse the several records one model turn
	// writes into a single counted turn.
	lastTurnUsage [3]int
	sawTurnUsage  bool
}

// Anchorable reports whether the first turn measures a cold fixed prefix.
func (h *Head) Anchorable() bool {
	return h != nil && h.FirstTurn != nil && len(h.ResumeSignals) == 0
}

// rawRecord is the shape of one transcript line. Only the fields the inspector
// needs are declared; everything else is ignored, and the two polymorphic
// fields are kept raw because their type varies by record kind — a `file`
// attachment's content is an object where a skill_listing's is a string, so a
// single typed struct would fail to decode perfectly valid records.
type rawRecord struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	SessionID        string          `json:"sessionId"`
	Timestamp        string          `json:"timestamp"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	PromptSource     string          `json:"promptSource"`
	Attachment       json.RawMessage `json:"attachment"`
	Message          json.RawMessage `json:"message"`
}

// attachmentKind peeks at an attachment's discriminator.
type attachmentKind struct {
	Type string `json:"type"`
}

// skillListingRecord is the skill_listing attachment.
type skillListingRecord struct {
	Content    string   `json:"content"`
	Names      []string `json:"names"`
	SkillCount int      `json:"skillCount"`
	IsInitial  bool     `json:"isInitial"`
}

// agentListingRecord is the agent_listing_delta attachment.
type agentListingRecord struct {
	AddedTypes []string `json:"addedTypes"`
	AddedLines []string `json:"addedLines"`
	IsInitial  bool     `json:"isInitial"`
}

// deferredToolsRecord is the deferred_tools_delta attachment.
type deferredToolsRecord struct {
	AddedNames []string `json:"addedNames"`
}

// mcpInstructionsRecord is the mcp_instructions_delta attachment.
type mcpInstructionsRecord struct {
	AddedNames  []string `json:"addedNames"`
	AddedBlocks []string `json:"addedBlocks"`
}

// nestedMemoryRecord is the nested_memory attachment.
type nestedMemoryRecord struct {
	Path    string `json:"path"`
	Content struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"content"`
}

// hookContextRecord is the hook_additional_context attachment.
type hookContextRecord struct {
	Content  []string `json:"content"`
	HookName string   `json:"hookName"`
}

// messageRecord is the assistant/user message envelope.
type messageRecord struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *usageRecord    `json:"usage"`
}

// usageRecord is the provider accounting. Recent Claude Code versions zero the
// top-level fields and report per-request figures under iterations, so both
// shapes are read and the iteration is preferred when present — it describes
// the single request whose prompt we are trying to measure.
type usageRecord struct {
	InputTokens   int              `json:"input_tokens"`
	CacheCreation int              `json:"cache_creation_input_tokens"`
	CacheRead     int              `json:"cache_read_input_tokens"`
	Iterations    []usageIteration `json:"iterations"`
}

// usageIteration is one request inside a multi-iteration assistant turn.
type usageIteration struct {
	InputTokens   int `json:"input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
}

// ParseHead reads a Claude Code transcript up to and including the model turn
// that first carried the session's startup injections, and returns what the
// session injected before that turn.
//
// It stops at that boundary: no record after it is read, which is what keeps the
// cost independent of conversation length. An error is returned only when the
// file cannot be opened or read; a record that fails to decode is recorded in
// [Head.Warnings] and reported as a caveat, because a silently skipped record
// would be a hole in the inventory.
func ParseHead(path string) (*Head, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the Claude transcript %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	head := &Head{NestedMemory: make(map[string]string)}
	r := bufio.NewReaderSize(f, 64<<10)
	var scanned int64

	for head.LinesRead < maxHeadLines && scanned < maxHeadBytes {
		line, truncated, rerr := readBoundedLine(r, maxRecordBytes)
		if len(line) == 0 && rerr != nil {
			if rerr == io.EOF {
				break
			}
			return nil, fmt.Errorf("reading the Claude transcript %q: %w", path, rerr)
		}
		head.LinesRead++
		scanned += int64(len(line))

		if truncated {
			head.Warnings = append(head.Warnings,
				fmt.Sprintf("record %d exceeds the %d-byte read cap and was not decoded", head.LinesRead, maxRecordBytes))
		} else if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			if stop := head.consume(trimmed); stop {
				head.ReachedAssistant = true
				break
			}
		}

		if head.lookaheadExhausted() {
			break
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("reading the Claude transcript %q: %w", path, rerr)
		}
	}

	head.finish(scanned)
	return head, nil
}

// lookaheadExhausted reports whether the bounded search for a startup block that
// follows the transcript's first assistant record has run out of budget.
func (h *Head) lookaheadExhausted() bool {
	if h.StartupBlockObserved || h.firstAssistantLine == 0 {
		return false
	}
	return h.LinesRead-h.firstAssistantLine >= maxStartupLookaheadLines ||
		h.PreBlockTurns > maxStartupLookaheadTurns
}

// finish records what the scan could not complete. A head that never reached its
// boundary is not an empty head: it is an unknown one, and the difference has to
// reach the report.
func (h *Head) finish(scanned int64) {
	if h.ReachedAssistant {
		return
	}
	if h.PostBlockSkipped > 0 {
		// The block is on record and assistant records follow it; they simply
		// carry no accounting agent-deck can read. [anchorMissingReason] already
		// says exactly that from UnusableTurns, and the pre-block sentence below
		// would say the opposite — that the injections came after the only turn.
		h.Warnings = append(h.Warnings, fmt.Sprintf(
			"the startup injections are on record and the %d assistant record(s) after them carry no usable accounting, so no measured total could be read for this session",
			h.PostBlockSkipped))
		return
	}
	if h.firstAssistantLine > 0 {
		// A model turn is on record, but no turn that carried the startup block
		// is. The recorded turn's request cannot have contained injections the
		// transcript does not yet show, so its accounting does not measure a
		// cold startup prefix and must not be published as one. This is the
		// Claude counterpart of the Codex cumulative-vs-turn cold-start guard.
		if h.StartupBlockObserved {
			h.addResumeSignal("the startup injections are recorded after the only model turn in the transcript, so that turn's accounting measures a request that did not carry them")
			return
		}
		h.addResumeSignal("the session's startup injections are not on record before the first model turn, so that turn's accounting measures a request that did not carry them")
		h.Warnings = append(h.Warnings,
			fmt.Sprintf("no startup injection block within %d records or %d model turns of the first assistant record: the startup inventory below is what was observed, not what loaded",
				maxStartupLookaheadLines, maxStartupLookaheadTurns))
		return
	}
	switch {
	case h.LinesRead >= maxHeadLines:
		h.Warnings = append(h.Warnings,
			fmt.Sprintf("no assistant turn in the first %d records: the startup inventory may be incomplete", maxHeadLines))
	case scanned >= maxHeadBytes:
		h.Warnings = append(h.Warnings,
			fmt.Sprintf("no assistant turn within the %d-byte read budget: the startup inventory may be incomplete", maxHeadBytes))
	}
}

// consume folds one decoded record into the head and reports whether it is the
// boundary record that ends the parse.
func (h *Head) consume(line string) (stop bool) {
	var rec rawRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d is not valid JSON: %v", h.LinesRead, err))
		return false
	}
	if rec.SessionID != "" && h.SessionID == "" {
		h.SessionID = rec.SessionID
	}

	switch rec.Type {
	case "assistant":
		return h.consumeAssistant(rec)
	case "summary":
		h.addResumeSignal("the transcript opens with a conversation summary, so the first turn replays earlier content")
	case "system":
		if rec.Subtype == "compact_boundary" {
			h.addResumeSignal("a compaction boundary precedes the first turn, so the first turn measures a compacted conversation and not a cold prefix")
		}
	case "user":
		if rec.IsCompactSummary {
			h.addResumeSignal("the first user message is a compaction summary, so the first turn replays earlier content")
		}
		h.readFirstUser(rec)
	case "attachment":
		h.readAttachment(rec)
	}
	return false
}

// consumeAssistant folds an assistant record into the head and reports whether
// it is the boundary record that ends the parse.
//
// The boundary is the first model turn recorded *after* the startup injection
// block, not simply the first model turn. Claude Code writes the skill, agent,
// deferred-tool and MCP catalogues asynchronously, and on a real corpus they land
// after an initial turn in 76 of 225 transcripts. Stopping at that earlier turn
// costs both halves of the report: the catalogues are never seen (so every skill
// on disk is described as certainly not loaded, at the strongest provenance the
// model has), and the anchor is read from a request that did not carry them (so
// the cold prefix is understated while still labelled provider-measured).
//
// The search past the first turn is bounded rather than open-ended, because a
// catalogue re-injected deep into a conversation is not startup state and the
// turn after it would fold the whole conversation into the fixed prefix. Both
// bounds are set from the corpus: every genuine late injection arrives within 20
// records and 1 model turn of the first assistant record, while the mid-session
// re-injections that must be excluded arrive 263 records and 52 turns in. The
// caps sit at 48 records and 2 turns — roughly double the observed need and an
// order of magnitude short of the case they exclude. When they expire,
// [Head.finish] withholds the anchor rather than publishing an uncertified one.
//
// A record that cannot supply an accounting is not the boundary. It is the same
// rule [Head.readFirstTurn] already applies before the block — a <synthetic>
// placeholder must not consume the only attempt — and it has to hold here too:
// Claude Code writes those placeholders on any session that was interrupted or
// hit an auth failure, and they land after the startup burst as readily as
// before it. Stopping on one ended the parse on the first record behind the
// block and left the real turns unread, so four live sessions lost the model
// name they had recorded 215 times and were told no measured total existed.
// The scan therefore steps over unusable records and keeps looking; the outer
// record and byte budgets bound it, exactly as they do when no assistant record
// is found at all.
func (h *Head) consumeAssistant(rec rawRecord) (stop bool) {
	if !h.StartupBlockObserved {
		if h.firstAssistantLine == 0 {
			h.firstAssistantLine = h.LinesRead
		}
		h.countTurn(rec)
		h.readFirstTurn(rec, false)
		return false
	}

	// The startup block is on record, so this request carried it: this is the
	// boundary and the turn the anchor must come from.
	preBlock := h.FirstTurn
	decodable := h.readFirstTurn(rec, true)
	if h.FirstTurn != nil {
		return true
	}
	if preBlock != nil {
		// The boundary record carries no usable accounting. The earlier turn is
		// kept for the model name it reports, but its total measured a request
		// without the catalogues and must never stand in for the cold prefix.
		h.FirstTurn = preBlock
		h.addResumeSignal(fmt.Sprintf(
			"the startup injections were recorded after the first model turn and the turn that carried them reports no usable accounting, so the only recorded total (%d tokens) measures a request that predates them",
			preBlock.PromptTokens()))
		return true
	}

	// Nothing usable here and nothing usable before it: there is no accounting
	// to fall back on, so this record cannot be the boundary. Keep reading.
	h.PostBlockSkipped++
	if !decodable {
		// A record we could not decode may have been a real model turn. Skipping
		// it means the next turn's request also carried whatever that one
		// produced, so the total that eventually lands covers more than the
		// fixed prefix. A placeholder carries no such risk — it went into no
		// request — which is why only the undecodable ones are counted here.
		h.PostBlockUndecodable++
	}
	return false
}

// countTurn advances the pre-block model-turn counter.
//
// Claude Code writes several assistant records for one model turn — thinking,
// text and tool_use blocks land separately carrying identical usage — so a turn
// is counted only when the accounting changes. On the corpus this collapses a
// genuine late injection to exactly one pre-block turn, while a transcript whose
// catalogue is re-injected mid-conversation shows dozens.
//
// A record that reports no tokens at all is not counted. The count is published
// as "the measured request also carried N earlier model turn(s)", and the
// <synthetic> placeholders Claude Code writes ahead of the first real turn on an
// interrupted session carry usage.input_tokens 0: they went into no request, so
// counting them would put a confident number in that sentence that describes
// nothing.
func (h *Head) countTurn(rec rawRecord) {
	var usage [3]int
	if len(rec.Message) > 0 {
		var msg messageRecord
		if err := json.Unmarshal(rec.Message, &msg); err == nil && msg.Usage != nil {
			usage = usageTriple(msg.Usage)
		}
	}
	if usage == ([3]int{}) {
		return
	}
	if h.sawTurnUsage && usage == h.lastTurnUsage {
		return
	}
	h.lastTurnUsage = usage
	h.sawTurnUsage = true
	h.PreBlockTurns++
}

// usageTriple reduces a usage record to the accounting that identifies a turn,
// preferring the per-request iteration for the same reason [Head.readFirstTurn]
// does: it describes the single request whose prompt is being measured.
func usageTriple(u *usageRecord) [3]int {
	if len(u.Iterations) > 0 {
		it := u.Iterations[0]
		return [3]int{it.InputTokens, it.CacheCreation, it.CacheRead}
	}
	return [3]int{u.InputTokens, u.CacheCreation, u.CacheRead}
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

// readFirstUser records the size of the session's first typed prompt. Only a
// genuinely typed message counts: slash-command envelopes and meta records are
// harness bookkeeping, not something the user can trim.
func (h *Head) readFirstUser(rec rawRecord) {
	if h.FirstUserChars > 0 || rec.IsMeta || len(rec.Message) == 0 {
		return
	}
	var msg messageRecord
	if err := json.Unmarshal(rec.Message, &msg); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: user message could not be decoded: %v", h.LinesRead, err))
		return
	}
	text := messageText(msg.Content)
	if strings.HasPrefix(strings.TrimSpace(text), "<command-") || strings.HasPrefix(strings.TrimSpace(text), "<local-command") {
		return
	}
	h.FirstUserChars = len([]rune(text))
}

// readFirstTurn extracts the provider accounting from an assistant record.
//
// final marks the boundary record — the first turn that carried the startup
// injections. Its accounting replaces any provisional one read from a turn that
// predates them; a provisional reading is otherwise kept.
//
// The guard is on FirstTurn, not on "did we look before". A record that turns
// out to be unusable — no envelope, no usage, a zero prompt total — must not
// consume the only attempt, because real transcripts open with records exactly
// like that: Claude Code writes two <synthetic> assistant records carrying
// usage.input_tokens 0 ahead of the first real turn on any session that was
// interrupted or hit an auth failure. Burning the attempt on those left the
// report claiming the session "has not completed a model turn yet" on
// transcripts holding hundreds of them — an unknown that shipped with a
// fabricated reason, which is worse than an unknown that admits it.
//
// Only the first unusable record warns, so a run of them cannot flood the
// caveat list.
//
// decodable reports whether the record's own structure could be read, which is a
// different question from whether it carried an accounting. A placeholder is
// perfectly decodable and simply reports nothing; a record whose envelope failed
// to parse might have been a real model turn. Only the caller stepping over
// records needs the distinction, and only it acts on it.
func (h *Head) readFirstTurn(rec rawRecord, final bool) (decodable bool) {
	if h.FirstTurn != nil && !final {
		return true
	}
	if final {
		h.FirstTurn = nil
	}
	// unusable records that this assistant record could not supply an
	// accounting, and warns about the first such record only. The count is what
	// lets the report say WHY there is no measured total: "no model turn is on
	// record" and "seven assistant records are on record and none of them
	// carries provider accounting" are different facts, and only one of them is
	// true of a transcript that opens with <synthetic> placeholders.
	unusable := func(format string, args ...any) {
		h.UnusableTurns++
		if h.firstTurnWarned {
			return
		}
		h.firstTurnWarned = true
		h.Warnings = append(h.Warnings, fmt.Sprintf(format, args...))
	}
	if len(rec.Message) == 0 {
		unusable("record %d: an assistant turn carries no message envelope, so it cannot supply a measured total", h.LinesRead)
		return true
	}
	var msg messageRecord
	if err := json.Unmarshal(rec.Message, &msg); err != nil {
		unusable("record %d: an assistant message could not be decoded: %v", h.LinesRead, err)
		return false
	}
	if h.ModelSeen == "" && msg.Model != "" && !strings.HasPrefix(msg.Model, "<") {
		// The model name is not an accounting claim, so it survives a record
		// that cannot supply one. Reading it here is what keeps a session whose
		// only readable records are placeholders from reporting an unknown model
		// and being handed the remedy that unknown implies.
		//
		// The placeholders name themselves — Claude Code writes model
		// "<synthetic>" — and that is a harness token, not a model. Recording it
		// would replace one wrong answer with a more confident wrong answer.
		h.ModelSeen = msg.Model
	}
	if msg.Usage == nil {
		unusable("record %d: an assistant turn reports no usage, so it cannot supply a measured total", h.LinesRead)
		return true
	}

	turn := &FirstTurn{
		Model:         msg.Model,
		InputTokens:   msg.Usage.InputTokens,
		CacheCreation: msg.Usage.CacheCreation,
		CacheRead:     msg.Usage.CacheRead,
		Source:        "message.usage.input_tokens + cache_creation_input_tokens + cache_read_input_tokens",
	}
	if len(msg.Usage.Iterations) > 0 {
		it := msg.Usage.Iterations[0]
		turn.InputTokens = it.InputTokens
		turn.CacheCreation = it.CacheCreation
		turn.CacheRead = it.CacheRead
		turn.Source = "message.usage.iterations[0].input_tokens + cache_creation_input_tokens + cache_read_input_tokens"
	}
	if turn.PromptTokens() <= 0 {
		unusable("record %d: an assistant turn reports a zero prompt total, which cannot be a real prompt; the reader keeps looking for one that can", h.LinesRead)
		return true
	}
	if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		turn.At = ts.UTC()
	}
	h.FirstTurn = turn
	return true
}

// readAttachment folds one attachment record into the head.
func (h *Head) readAttachment(rec rawRecord) {
	if len(rec.Attachment) == 0 {
		return
	}
	var kind attachmentKind
	if err := json.Unmarshal(rec.Attachment, &kind); err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("record %d: attachment discriminator could not be read: %v", h.LinesRead, err))
		return
	}

	switch kind.Type {
	case attachSkillListing, attachAgentListing, attachDeferredTools, attachMCPInstruction:
		// Recorded on sight, before the decode: the burst was witnessed even if
		// one of its records turns out to be unreadable, and that is what the
		// boundary rule and the report's absence claims turn on.
		h.StartupBlockObserved = true
	}

	decode := func(dst any) bool {
		if err := json.Unmarshal(rec.Attachment, dst); err != nil {
			h.Warnings = append(h.Warnings,
				fmt.Sprintf("record %d: %s attachment could not be decoded (%v): this part of the startup inventory is missing, not empty", h.LinesRead, kind.Type, err))
			// Witnessing the burst and reading one of its records are different
			// facts, and only the second one licenses "this session recorded no
			// X". Remember which kind failed so the report can say the harder,
			// truer thing instead.
			if h.DecodeFailed == nil {
				h.DecodeFailed = make(map[string]bool, 1)
			}
			h.DecodeFailed[kind.Type] = true
			return false
		}
		return true
	}

	switch kind.Type {
	case attachSkillListing:
		var a skillListingRecord
		if !decode(&a) {
			return
		}
		// A later delta describes a change to the listing, not the startup
		// state; only the initial record answers "what loaded at startup".
		if h.Skills != nil && !a.IsInitial {
			return
		}
		h.Skills = &SkillListing{Content: a.Content, Names: a.Names, Count: a.SkillCount, Initial: a.IsInitial}

	case attachAgentListing:
		var a agentListingRecord
		if !decode(&a) {
			return
		}
		if h.Agents != nil && !a.IsInitial {
			return
		}
		h.Agents = &AgentListing{Types: a.AddedTypes, Lines: a.AddedLines, Initial: a.IsInitial}

	case attachDeferredTools:
		var a deferredToolsRecord
		if !decode(&a) {
			return
		}
		if h.Deferred == nil {
			h.Deferred = &DeferredTools{}
		}
		h.Deferred.Names = appendUnique(h.Deferred.Names, a.AddedNames)

	case attachMCPInstruction:
		var a mcpInstructionsRecord
		if !decode(&a) {
			return
		}
		if h.MCP == nil {
			h.MCP = &MCPInstructions{}
		}
		for i, name := range a.AddedNames {
			block := ""
			if i < len(a.AddedBlocks) {
				block = a.AddedBlocks[i]
			}
			h.MCP.Names = append(h.MCP.Names, name)
			h.MCP.Blocks = append(h.MCP.Blocks, block)
		}

	case attachNestedMemory:
		var a nestedMemoryRecord
		if !decode(&a) {
			return
		}
		path := a.Path
		if path == "" {
			path = a.Content.Path
		}
		if path != "" && a.Content.Content != "" && len(h.NestedMemory) < maxNestedMemoryFiles {
			h.NestedMemory[path] = a.Content.Content
		}

	case attachHookContext:
		var a hookContextRecord
		if !decode(&a) {
			return
		}
		for _, c := range a.Content {
			h.HookContextChars += len([]rune(c))
		}
		h.HookContextCount++
	}
}

// messageText flattens a message's content, which is either a plain string or
// an array of typed blocks.
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// appendUnique appends the names not already present, preserving order.
func appendUnique(dst []string, add []string) []string {
	seen := make(map[string]bool, len(dst))
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range add {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		dst = append(dst, s)
	}
	return dst
}

// readBoundedLine reads one newline-terminated record, retaining at most max
// bytes and reporting whether the record was longer.
//
// bufio.Scanner cannot be used here: a record over its buffer aborts the whole
// scan, and a single oversized compact_boundary record must cost us that record
// and nothing more. The returned error is io.EOF on the final record.
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
