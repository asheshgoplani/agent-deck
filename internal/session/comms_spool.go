package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Comms Ledger producer side (docs/comms.md). A hook process (Claude Stop,
// Gemini AfterAgent, Cursor afterAgentResponse, Hermes post_llm_call, the pi
// extension, codex-notify) and the OpenCode SSE watcher never write the
// ledger: the daemon is its only writer (the #824 duplicate-writer race). They
// spool one small JSON file per observed edge under
// <data>/runtime/comms/spool/<instance>/<ulid>.json (tmp + fsync + rename),
// and the notify daemon ingests the spool on its poll loop, classifies, and
// commits one ledger record per turn. The spool is a transit area, not a
// store: a file is removed as soon as its record is committed, and an entry
// a daemon never picked up (an old daemon that predates the ledger, a
// removed session) is pruned after commsSpoolMaxAge.

// Spool edges.
const (
	// CommsEdgeTurnEnd carries the harness's final assistant text for a turn.
	CommsEdgeTurnEnd = "turn_end"
	// CommsEdgePromptStart carries the prompt that started a turn, so the
	// daemon can derive the turn's trigger for harnesses without a readable
	// transcript (a send envelope, an inbox/heartbeat prompt, a human).
	CommsEdgePromptStart = "prompt_start"
)

// Spool caps. Text is capped at the record ceiling here (the daemon applies
// the configured record cap); the prompt only needs its prefix for trigger
// classification.
const (
	commsSpoolTextBytes   = comms.MaxTextBytes
	commsSpoolPromptBytes = 1024
	commsSpoolMaxAge      = 24 * time.Hour
	commsSpoolMaxFiles    = 512 // per instance; a daemon that never drains must not fill the disk
)

// CommsSpoolEntry is one spooled edge. Field names match the ledger record
// where the meaning is the same.
type CommsSpoolEntry struct {
	Harness        string `json:"harness"`  // claude | codex | gemini | cursor | pi | hermes | opencode
	Event          string `json:"event"`    // raw hook event name
	Edge           string `json:"edge"`     // CommsEdge* constants
	Instance       string `json:"instance"` // agent-deck session id
	SessionID      string `json:"session_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	Text           string `json:"text,omitempty"`   // assistant text (turn_end)
	Prompt         string `json:"prompt,omitempty"` // user prompt prefix (either edge)
	TranscriptPath string `json:"transcript_path,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	TSignal        int64  `json:"t_signal"` // Unix ms the harness signal was received

	// path is where the entry sits on disk (set by ReadCommsSpool).
	path string
}

// commsLedgerOverride is a test seam: when set it replaces the config read.
// Atomic because a watcher goroutine may read it while a test's cleanup
// restores it.
var commsLedgerOverride atomic.Pointer[bool]

// CommsLedgerEnabled reports [comms] ledger = true. Every producer checks it
// before spooling, so an operator with the switch off pays nothing.
func CommsLedgerEnabled() bool {
	if v := commsLedgerOverride.Load(); v != nil {
		return *v
	}
	cfg, _ := LoadUserConfig()
	return cfg != nil && cfg.Comms.Ledger
}

// SetCommsLedgerForTest forces the switch for the current test.
func SetCommsLedgerForTest(on bool) func() {
	prev := commsLedgerOverride.Swap(&on)
	return func() { commsLedgerOverride.Store(prev) }
}

// CommsSpoolDir is the spool root: <data>/runtime/comms/spool.
func CommsSpoolDir() string {
	return runtimeDirOrTemp(filepath.Join("comms", "spool"))
}

func commsSpoolInstanceDir(instanceID string) string {
	return filepath.Join(CommsSpoolDir(), sanitizeInboxName(instanceID))
}

// WriteCommsSpool spools one edge for the daemon. It is the only disk write
// a producer makes for the ledger. The file lands under the instance's spool
// directory as <ulid>.json via tmp + fsync + rename, so the daemon never
// reads a torn entry and the name orders entries by signal time.
func WriteCommsSpool(e CommsSpoolEntry) error {
	e.Instance = strings.TrimSpace(e.Instance)
	if e.Instance == "" {
		return errors.New("comms spool: empty instance id")
	}
	if e.Edge != CommsEdgeTurnEnd && e.Edge != CommsEdgePromptStart {
		return errors.New("comms spool: unknown edge " + e.Edge)
	}
	if e.TSignal == 0 {
		e.TSignal = time.Now().UnixMilli()
	}
	e.Text = comms.CapText(strings.TrimSpace(e.Text), commsSpoolTextBytes)
	e.Prompt = comms.CapText(strings.TrimSpace(e.Prompt), commsSpoolPromptBytes)
	if e.Edge == CommsEdgeTurnEnd && e.Text == "" && e.Harness != "opencode" {
		// Nothing to carry: the status edge is already in the hook file. An
		// empty turn would only become a text-less record.
		return nil
	}
	dir := commsSpoolInstanceDir(e.Instance)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if countSpoolFiles(dir) >= commsSpoolMaxFiles {
		return errors.New("comms spool: instance spool full; is the notify daemon running?")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return writeFileDurable(filepath.Join(dir, comms.NewID(time.UnixMilli(e.TSignal))+".json"), data, 0o600)
}

func countSpoolFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// ReadCommsSpool returns the instance's spooled entries oldest first. A torn
// or foreign file is skipped. Entries carry their path so the daemon can
// remove each one after committing it.
func ReadCommsSpool(instanceID string) ([]CommsSpoolEntry, error) {
	dir := commsSpoolInstanceDir(instanceID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]CommsSpoolEntry, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path) // #nosec G304 -- sanitized id under the data dir
		if err != nil {
			continue
		}
		var e CommsSpoolEntry
		if json.Unmarshal(data, &e) != nil || e.Edge == "" {
			continue
		}
		e.path = path
		out = append(out, e)
	}
	return out, nil
}

// RemoveCommsSpoolEntry deletes one ingested entry.
func RemoveCommsSpoolEntry(e CommsSpoolEntry) {
	if e.path != "" {
		_ = os.Remove(e.path)
	}
}

// ListCommsSpoolInstances returns the instance ids with a spool directory.
func ListCommsSpoolInstances() []string {
	entries, err := os.ReadDir(CommsSpoolDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// PruneCommsSpool removes entries older than commsSpoolMaxAge and empty
// instance directories. It bounds the spool when no daemon drains it: an
// older daemon that predates the ledger, or a child whose session was
// removed before its last edge was ingested.
func PruneCommsSpool(now time.Time) {
	root := CommsSpoolDir()
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		remaining := 0
		for _, f := range files {
			info, err := f.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) > commsSpoolMaxAge {
				_ = os.Remove(filepath.Join(dir, f.Name()))
				continue
			}
			remaining++
		}
		if remaining == 0 {
			_ = os.Remove(dir) // fails harmlessly while a producer is writing
		}
	}
}

// commsPromptTrigger maps the prompt that started a turn to a trigger kind,
// for harnesses whose transcript the daemon cannot read. The prefixes are
// the ones the Claude classifier recognises, so a tagged send, an inbox or
// heartbeat prompt and a human prompt tier the same way on every harness.
// An empty prompt (no prompt-start edge seen) is unknown, which tiers toward
// urgent: louder, not lossy.
func commsPromptTrigger(prompt string) (trigger, fromID string) {
	text := strings.TrimSpace(prompt)
	switch {
	case text == "":
		return TurnTriggerUnknown, ""
	case strings.HasPrefix(text, sendEnvelopePrefix):
		rest := strings.TrimPrefix(text, sendEnvelopePrefix)
		if end := strings.IndexByte(rest, ']'); end > 0 {
			return TurnTriggerSend, strings.TrimSpace(rest[:end])
		}
		return TurnTriggerSend, ""
	case strings.HasPrefix(text, "[INBOX"), strings.HasPrefix(text, "[HEARTBEAT]"),
		strings.HasPrefix(text, "[agent-deck inbox]"), strings.HasPrefix(text, "[agent-deck msg]"):
		return TurnTriggerInbox, ""
	case strings.HasPrefix(text, "<task-notification>"):
		return TurnTriggerTask, ""
	case strings.HasPrefix(text, "<system-reminder>"), strings.HasPrefix(text, "<local-command"),
		strings.HasPrefix(text, "<command-name>"):
		return TurnTriggerSystem, ""
	default:
		return TurnTriggerHuman, ""
	}
}
