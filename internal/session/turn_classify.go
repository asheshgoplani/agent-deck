package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Turn classification (issue #2469). The notify-daemon used to key a child's
// turn on the transcript FILE SIZE: any growth was a "new turn" and every new
// turn woke the parent with a text-less record. A child running background
// agents or background Bash gets a <task-notification> user record and an
// assistant turn for every completion, so the parent was woken dozens of times
// per hour for turns that carried nothing it had to act on.
//
// This file reads the just-finished turn from the transcript tail and reduces
// it to facts the producer can tier on: WHAT started the turn (trigger), WHAT
// the child said (bounded text + hash), and whether it carries a completion
// sentinel, an error, or a question. The tier rule is:
//
//	noise  — same text hash and same attention class as the last journaled
//	         turn of this child, no new sentinel (hook re-fires, waiting→idle
//	         flips, polls that saw nothing new). Never recorded in the inbox.
//	urgent — a completion sentinel, an error status, or an explicit question
//	         to the parent. Wakes the parent.
//	info   — any other new text, whoever started the turn (a background task,
//	         a system injection, an inbox prompt, a human, a send). Recorded
//	         with its text; never wakes on its own (rides the parent's next
//	         turn or the info digest).
//
// An unreadable transcript takes the legacy path (no text, urgent on a new
// signal), which is today's behaviour minus the duplicate re-fires; a readable
// transcript with an unknown trigger is info unless it carries a sentinel, an
// error or a question.

// Turn tiers and triggers carried on TransitionNotificationEvent and the
// per-child turn journal.
const (
	TurnTierUrgent = "urgent"
	TurnTierInfo   = "info"
	TurnTierNoise  = "noise"

	TurnTriggerHuman   = "human"   // a human typed (or an untagged parent send)
	TurnTriggerSend    = "send"    // a tagged agent-deck send ([agent-deck from:<id>])
	TurnTriggerTask    = "task"    // a background sub-agent / Bash task notification
	TurnTriggerSystem  = "system"  // isMeta / <system-reminder> / local command output
	TurnTriggerInbox   = "inbox"   // the child's own [INBOX]/Stop-hook/[HEARTBEAT] prompt
	TurnTriggerUnknown = "unknown" // no main-chain user record found in the tail window
)

// turnFlushRaceWindow bounds how long a transcript whose newest main-chain
// record is a user prompt counts as "reply not flushed yet" (issue #1186: the
// Stop hook can fire before the assistant record lands, typically by well
// under a second). Past it the turn is classified without text.
const turnFlushRaceWindow = 10 * time.Second

// turnScanTailLines bounds the backward walk. One turn of a busy child is a
// handful of records (user, assistant tool calls, tool results, final
// assistant text, trailing system/attachment records); 200 leaves margin for
// a long tool loop while TranscriptTailLines caps the bytes at 512 KB.
const turnScanTailLines = 200

// DefaultTurnTextBytes bounds the child text carried on a record when
// [inbox] max_text_bytes is unset. MaxTurnTextBytes is the hard ceiling.
const (
	DefaultTurnTextBytes = 600
	MaxTurnTextBytes     = 2048
)

// sendEnvelopePrefix tags a `session send` so the receiving child's reply turn
// can be routed back to the sender (PR5 of the comms redesign). The classifier
// recognises it today so a tagged send is never mistaken for background noise.
const sendEnvelopePrefix = "[agent-deck from:"

// SendEnvelope is the one-line tag `session send` puts above a message sent
// from inside an agent-deck session.
func SendEnvelope(senderID string) string {
	return sendEnvelopePrefix + strings.TrimSpace(senderID) + "]"
}

// HasSendEnvelope reports whether message already starts with an envelope
// (a forwarded or re-sent message), so it is never tagged twice.
func HasSendEnvelope(message string) bool {
	return strings.HasPrefix(strings.TrimSpace(message), sendEnvelopePrefix)
}

// TurnFacts is everything the producer needs to tier a child's finished turn.
type TurnFacts struct {
	// UUID is the transcript uuid of the assistant record that carries the
	// turn's final text; the stable per-turn identity.
	UUID string
	// Trigger is one of the TurnTrigger* constants.
	Trigger string
	// FromID is the sender parsed from a send envelope (Trigger == send).
	FromID string
	// Text is the child's final assistant text, trimmed, UNBOUNDED here; the
	// producer caps it with CapTurnText before it leaves the daemon.
	Text string
	// TextHash is the first 16 hex of sha256(Text), "" when Text is empty.
	TextHash string
	// Question reports a parent-facing question in Text.
	Question bool
	// Done is the completion sentinel when HasDone.
	Done    DoneSignal
	HasDone bool
	// TypedCommand reports that the turn was started by a slash command a
	// person typed: a non-meta command record without scheduled turnOrigin,
	// possibly followed by a meta expansion. Trigger stays system; the done-repeat check
	// (issue #2481) reads this to treat the turn as started by a person.
	TypedCommand bool
	// Pending means the Stop hook outran the transcript flush: the newest
	// main-chain record is a user record with no assistant reply yet.
	Pending bool
	// At is the assistant record's own timestamp (zero when the record has
	// none), so a consumer can tell which observed edge the facts belong to.
	At time.Time
}

// Signal is the per-turn identity the notifier dedups and fingerprints on:
// "turn:<uuid>" when the assistant record is known, else "text:<hash>", else
// "" (caller falls back to the legacy signal).
func (f TurnFacts) Signal() string {
	switch {
	case f.UUID != "":
		return "turn:" + f.UUID
	case f.TextHash != "":
		return "text:" + f.TextHash
	default:
		return ""
	}
}

// transcriptTurnRecord is the subset of a Claude transcript record the
// classifier reads. turnOrigin / origin.kind / isMeta are the fields Claude
// Code 2.1.x stamps on user records; the content prefixes are the fallback
// for builds that omit them.
type transcriptTurnRecord struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	Timestamp   json.RawMessage `json:"timestamp"` // decoded leniently: a non-string value never drops the record
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	TurnOrigin  string          `json:"turnOrigin"`
	Origin      struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// userRecordIsToolResult reports whether a user record only carries tool
// results (the harness echoing a tool's output back), which is not a turn
// trigger.
func userRecordIsToolResult(content json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil || len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

// ScanTranscriptTurn reads the just-finished main-chain turn from the
// transcript tail. A missing or unreadable file returns an error so callers
// fall back to the legacy signal.
func ScanTranscriptTurn(path string) (TurnFacts, error) {
	lines, err := TranscriptTailLines(path, turnScanTailLines)
	if err != nil {
		return TurnFacts{}, err
	}
	return classifyTranscriptTail(lines), nil
}

// classifyTranscriptTail is ScanTranscriptTurn on already-read lines (oldest
// first). Exposed for tests with fixture transcripts.
func classifyTranscriptTail(lines []string) TurnFacts {
	var facts TurnFacts
	foundAssistant := false
	for i := len(lines) - 1; i >= 0; i-- {
		var rec transcriptTurnRecord
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil || rec.IsSidechain {
			continue
		}
		switch rec.Type {
		case "assistant":
			if foundAssistant {
				continue
			}
			text := strings.TrimSpace(transcriptText(rec.Message.Content))
			if text == "" {
				continue // tool_use-only record; keep walking
			}
			foundAssistant = true
			facts.UUID = rec.UUID
			var ts string
			if json.Unmarshal(rec.Timestamp, &ts) == nil && ts != "" {
				facts.At, _ = time.Parse(time.RFC3339Nano, ts)
			}
			facts.Text = text
			facts.TextHash = turnTextHash(text)
			facts.Question = textAsksParent(text)
			facts.Done, facts.HasDone = ScanDoneSentinel(text)
		case "user":
			if userRecordIsToolResult(rec.Message.Content) {
				continue
			}
			if !foundAssistant {
				// The reply to this prompt has not flushed yet.
				facts.Pending = true
				return facts
			}
			facts.Trigger, facts.FromID = classifyTrigger(rec)
			facts.TypedCommand = transcriptTypedCommand(lines[:i+1])
			return facts
		}
	}
	if foundAssistant {
		facts.Trigger = TurnTriggerUnknown
	} else {
		facts.Pending = true
	}
	return facts
}

// transcriptTypedCommand finds the command record behind a prompt's meta
// expansion. Never cross an assistant record into an earlier turn.
func transcriptTypedCommand(lines []string) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		var rec transcriptTurnRecord
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil || rec.IsSidechain {
			continue
		}
		if rec.Type == "assistant" {
			return false
		}
		if rec.Type != "user" || userRecordIsToolResult(rec.Message.Content) {
			continue
		}
		if rec.TurnOrigin == "scheduled" {
			return false
		}
		if rec.IsMeta {
			continue
		}
		text := strings.TrimSpace(transcriptText(rec.Message.Content))
		return strings.HasPrefix(text, "<command-name>") || strings.HasPrefix(text, "<command-message>")
	}
	return false
}

// classifyTrigger maps the user record that started a turn to a trigger kind.
func classifyTrigger(rec transcriptTurnRecord) (trigger, fromID string) {
	text := strings.TrimSpace(transcriptText(rec.Message.Content))
	switch {
	case rec.TurnOrigin == "task_notification", rec.Origin.Kind == "task-notification",
		strings.HasPrefix(text, "<task-notification>"):
		return TurnTriggerTask, ""
	case strings.HasPrefix(text, sendEnvelopePrefix):
		rest := strings.TrimPrefix(text, sendEnvelopePrefix)
		if end := strings.IndexByte(rest, ']'); end > 0 {
			return TurnTriggerSend, strings.TrimSpace(rest[:end])
		}
		return TurnTriggerSend, ""
	case strings.HasPrefix(text, "[INBOX"), strings.HasPrefix(text, "Stop hook feedback:"),
		strings.HasPrefix(text, "[HEARTBEAT]"), strings.HasPrefix(text, "[agent-deck inbox]"):
		return TurnTriggerInbox, ""
	case rec.IsMeta, strings.HasPrefix(text, "<system-reminder>"), strings.HasPrefix(text, "<local-command"),
		strings.HasPrefix(text, "<command-name>"):
		return TurnTriggerSystem, ""
	default:
		return TurnTriggerHuman, ""
	}
}

// textAsksParent reports a parent-facing question: a NEED:/QUESTION:/ASK: line
// (markdown emphasis and bullet prefixes ignored), or one of the last two
// non-empty lines ending in "?" (closing punctuation and emphasis ignored, so
// "…?)" and "…?**" count, and a question followed by a one-line sign-off is
// still a question).
func textAsksParent(text string) bool {
	var tail []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		marker := strings.ToUpper(strings.TrimLeft(line, "-*>#•· \t_`"))
		if strings.HasPrefix(marker, "NEED:") || strings.HasPrefix(marker, "QUESTION:") || strings.HasPrefix(marker, "ASK:") {
			return true
		}
		tail = append(tail, line)
		if len(tail) > 2 {
			tail = tail[1:]
		}
	}
	for _, line := range tail {
		if strings.HasSuffix(strings.TrimRight(line, "*_`)]\"' "), "?") {
			return true
		}
	}
	return false
}

func turnTextHash(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

// CapTurnText truncates text to at most max bytes on a rune boundary, marking
// the clip. max <= 0 means DefaultTurnTextBytes; MaxTurnTextBytes is the hard
// ceiling so no record ever grows past the inbox line scanner's comfort zone.
func CapTurnText(text string, max int) string {
	return capTextBytes(text, clampTurnTextBytes(max))
}

// capTextBytes truncates text to at most max bytes on a rune boundary,
// marking the clip. No defaults or ceilings: callers apply their own.
func capTextBytes(text string, max int) string {
	if len(text) <= max {
		return text
	}
	const marker = "…"
	keep := max - len(marker)
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(text[keep]) {
		keep--
	}
	return text[:keep] + marker
}

// clampTurnTextBytes applies the default (for n <= 0) and the hard ceiling.
func clampTurnTextBytes(n int) int {
	if n <= 0 {
		return DefaultTurnTextBytes
	}
	if n > MaxTurnTextBytes {
		return MaxTurnTextBytes
	}
	return n
}

// attentionClass folds waiting and idle together: both mean "the child is at
// its prompt". error is its own class so a flip into error is never noise.
func attentionClass(status string) string {
	switch normalizeStatusString(status) {
	case string(StatusError):
		return "error"
	default:
		return "attention"
	}
}

// ClassifyTurnTier applies the tier rule. prev is the child's last journaled
// turn (nil when none).
//
// Noise is EITHER the same turn seen again (same assistant record uuid: hook
// re-fires, waiting→idle flips, polls that saw nothing new) OR a background
// turn whose text repeats the previous one (a child re-arming a monitor and
// printing the same line). A NEW turn a human or a send started is never
// noise, even when the child answers with the same words: the answer is news.
func ClassifyTurnTier(facts TurnFacts, status string, prev *TurnJournalEntry) string {
	if prev != nil && attentionClass(prev.Status) == attentionClass(status) &&
		(!facts.HasDone || (prev.DoneStatus == facts.Done.Status && prev.DoneSummary == facts.Done.Summary)) {
		sameTurn := facts.UUID != "" && prev.UUID == facts.UUID
		sameTextNoUUID := facts.UUID == "" && facts.TextHash != "" && prev.TextHash == facts.TextHash
		repeatedBackground := facts.TextHash != "" && prev.TextHash == facts.TextHash &&
			(facts.Trigger == TurnTriggerTask || facts.Trigger == TurnTriggerSystem || facts.Trigger == TurnTriggerInbox)
		if sameTurn || sameTextNoUUID || repeatedBackground {
			return TurnTierNoise
		}
	}
	// Urgent is exactly: a completion sentinel, an error status, or an
	// explicit question to the parent. Everything else is info, INCLUDING a
	// reply to something the parent or a human sent: a progress note or an
	// acknowledgement does not need the parent awake (conductor ruling,
	// 2026-10-03: four such replies cost a wake each). The parent reads info on
	// its next turn or in the digest; a sender that used --wait already has it.
	if facts.HasDone || normalizeStatusString(status) == string(StatusError) || facts.Question {
		return TurnTierUrgent
	}
	return TurnTierInfo
}

// turnFactsCache memoises the transcript scan per path on (size, mtime), so
// the daemon pays one stat per child per poll in steady state and one tail
// read per real turn. Shared by every daemon pass in the process. One tail
// read feeds both readers of the tail: the turn classifier (Facts) and the
// background-work scan the status merge uses (Background, issue #2473).
type turnFactsCache struct {
	mu      sync.Mutex
	entries map[string]turnFactsCacheEntry
}

type turnFactsCacheEntry struct {
	size  int64
	mtime time.Time
	facts TurnFacts
	err   error
	bg    transcriptBackgroundScan
}

var turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}

// load returns the cache entry for path, reading the transcript tail only when
// the file changed since the last read.
func (c *turnFactsCache) load(path string) (turnFactsCacheEntry, error) {
	if path == "" {
		return turnFactsCacheEntry{}, os.ErrNotExist
	}
	info, err := os.Stat(path)
	if err != nil {
		return turnFactsCacheEntry{}, err
	}
	c.mu.Lock()
	entry, ok := c.entries[path]
	c.mu.Unlock()
	if ok && entry.size == info.Size() && entry.mtime.Equal(info.ModTime()) {
		return entry, nil
	}
	entry = turnFactsCacheEntry{size: info.Size(), mtime: info.ModTime()}
	lines, err := TranscriptTailLines(path, turnScanTailLines)
	if err != nil {
		entry.err = err
	} else {
		entry.facts = classifyTranscriptTail(lines)
		entry.bg = scanTranscriptBackground(lines)
	}
	c.mu.Lock()
	if len(c.entries) > 4096 {
		c.entries = map[string]turnFactsCacheEntry{}
	}
	c.entries[path] = entry
	c.mu.Unlock()
	return entry, nil
}

// Facts returns the classification of the transcript at path, re-scanning
// only when the file changed. A Pending result stays pending only within the
// flush-race window of the file's last write.
func (c *turnFactsCache) Facts(path string) (TurnFacts, error) {
	entry, err := c.load(path)
	if err != nil {
		return TurnFacts{}, err
	}
	if entry.err == nil && entry.facts.Pending {
		// A reply that never flushes (interrupted turn, a prompt with no
		// assistant text) must not park the child: past the flush-race window
		// the turn is treated as unclassifiable (legacy signal, trigger
		// unknown) rather than pending.
		if time.Since(entry.mtime) <= turnFlushRaceWindow {
			return entry.facts, nil
		}
		return TurnFacts{Trigger: TurnTriggerUnknown}, nil
	}
	return entry.facts, entry.err
}

// Background returns the background-work scan of the transcript at path
// (see scanTranscriptBackground), sharing Facts' tail read and cache.
func (c *turnFactsCache) Background(path string) (transcriptBackgroundScan, error) {
	entry, err := c.load(path)
	if err != nil {
		return transcriptBackgroundScan{}, err
	}
	return entry.bg, entry.err
}

// instanceTurnFacts classifies the instance's current turn from its Claude
// transcript. ok=false when the tool has no readable transcript (Codex,
// Gemini, pi, terminal tools, remote sessions): callers use the legacy signal
// and treat the turn as trigger=unknown.
func instanceTurnFacts(inst *Instance) (TurnFacts, bool) {
	if inst == nil {
		return TurnFacts{}, false
	}
	path := inst.GetJSONLPath()
	if path == "" {
		return TurnFacts{}, false
	}
	facts, err := turnFacts.Facts(path)
	if err != nil {
		return TurnFacts{}, false
	}
	return facts, true
}

// ClassifyTranscriptTailForReplay exposes the tail classifier for the
// measurement tool under tools/; not used by the product.
func ClassifyTranscriptTailForReplay(lines []string) TurnFacts { return classifyTranscriptTail(lines) }
