package session

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Comms Ledger producer side (docs/comms.md). A producer (Claude's
// hook-handler, codex-notify, and the daemon's own status edges) never
// writes the ledger: the daemon is its only writer (the #824
// duplicate-writer race). They spool one small JSON file per observed edge
// under
// <data>/runtime/comms/spool/<instance>/<ulid>.json (tmp + fsync + rename),
// and the notify daemon ingests the spool on its poll loop, classifies, and
// commits one ledger record per turn. The spool is a transit area, not a
// store: a file is removed as soon as its record is committed, and an entry
// a daemon never picked up (the switch turned off, a removed session) is
// pruned by this daemon after commsSpoolMaxAge; the per-instance file cap
// bounds the spool under a daemon old enough not to know it at all.

// Spool edges.
const (
	// CommsEdgeTurnEnd carries the harness's final assistant text for a turn.
	CommsEdgeTurnEnd = "turn_end"
	// CommsEdgePromptStart carries the prompt that started a turn, so the
	// daemon can derive the turn's trigger for harnesses without a readable
	// transcript (a send envelope, an inbox/heartbeat prompt, a human).
	CommsEdgePromptStart = "prompt_start"
	// CommsEdgeStatus is a status-only edge the daemon spools for a tool
	// with no text producer (From -> State with the output signal in TH).
	CommsEdgeStatus = "status"
)

// Spool caps. Text is capped well above the record ceiling (the daemon
// applies the configured record cap at commit) so the end of a long reply,
// where a completion sentinel or a question sits, is still in the spool;
// the full-text hash is carried separately. The prompt only needs its
// prefix for trigger classification.
const (
	commsSpoolTextBytes   = 16 << 10
	commsSpoolPromptBytes = 1024
	commsSpoolMaxAge      = 24 * time.Hour
	commsSpoolMaxFiles    = 512 // per instance; a daemon that never drains must not fill the disk
	commsSpoolIDBytes     = 256 // harness, event, session and turn ids
	commsSpoolCwdBytes    = 4096
	commsSpoolMaxBytes    = 64 << 10 // an entry over this is not ours: skipped and removed on read
)

// CommsSpoolEntry is one spooled edge. Field names match the ledger record
// where the meaning is the same.
type CommsSpoolEntry struct {
	Harness        string `json:"harness"`         // claude | codex | gemini | cursor | pi | hermes | opencode
	Event          string `json:"event"`           // raw hook event name
	Edge           string `json:"edge"`            // CommsEdge* constants
	Instance       string `json:"instance"`        // agent-deck session id
	From           string `json:"from,omitempty"`  // status edge: the status left
	State          string `json:"state,omitempty"` // status edge: the status entered
	SessionID      string `json:"session_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	Text           string `json:"text,omitempty"`   // assistant text (turn_end), capped
	TH             string `json:"th,omitempty"`     // sha256/16 of the FULL trimmed text, before the cap
	Prompt         string `json:"prompt,omitempty"` // user prompt prefix (either edge)
	TranscriptPath string `json:"transcript_path,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	TSignal        int64  `json:"t_signal"` // Unix ms the harness signal was received

	// path is where the entry sits on disk (set by ReadCommsSpool).
	path string
}

// ID is the entry's spool id (the ULID file name), "" before it is written.
// It is minted once by the producer, so it identifies this observation
// across daemon retries.
func (e CommsSpoolEntry) ID() string {
	if e.path == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(e.path), ".json")
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
	if e.Edge != CommsEdgeTurnEnd && e.Edge != CommsEdgePromptStart && e.Edge != CommsEdgeStatus {
		return errors.New("comms spool: unknown edge " + e.Edge)
	}
	if e.TSignal == 0 {
		e.TSignal = time.Now().UnixMilli()
	}
	full := strings.TrimSpace(e.Text)
	if full != "" && e.Edge == CommsEdgeTurnEnd {
		e.TH = turnTextHash(full) // the daemon matches this against the transcript turn
	}
	e.Text = capBytes(full, commsSpoolTextBytes)
	e.Prompt = comms.CapText(strings.TrimSpace(e.Prompt), commsSpoolPromptBytes)
	e.Harness = capBytes(e.Harness, commsSpoolIDBytes)
	e.Event = capBytes(e.Event, commsSpoolIDBytes)
	e.SessionID = capBytes(e.SessionID, commsSpoolIDBytes)
	e.TurnID = capBytes(e.TurnID, commsSpoolIDBytes)
	e.TranscriptPath = capBytes(e.TranscriptPath, commsSpoolCwdBytes)
	e.Cwd = capBytes(e.Cwd, commsSpoolCwdBytes)
	if e.Edge == CommsEdgeTurnEnd && e.Text == "" {
		// Nothing to carry: the status edge is already in the hook file. An
		// empty turn would only become a text-less record.
		return nil
	}
	dir := commsSpoolInstanceDir(e.Instance)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if countSpoolFiles(dir) >= commsSpoolMaxFiles {
		commsLog.Warn("comms_spool_full", slog.String("instance", e.Instance), slog.Int("cap", commsSpoolMaxFiles))
		return errors.New("comms spool: instance spool full; is the notify daemon running?")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return writeFileDurable(filepath.Join(dir, comms.NewID(time.UnixMilli(e.TSignal))+".json"), data, 0o600)
}

// capBytes truncates s to max bytes on a rune boundary, with no marker
// (identifiers, paths and spool text, whose full hash travels separately).
func capBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	keep := max
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep]
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
		data, ok := readSpoolFile(path)
		if !ok {
			continue
		}
		var e CommsSpoolEntry
		if json.Unmarshal(data, &e) != nil || e.Edge == "" {
			commsLog.Warn("comms_spool_entry_rejected", slog.String("path", path), slog.String("reason", "malformed"))
			_ = os.Remove(path)
			continue
		}
		e.path = path
		out = append(out, e)
	}
	return out, nil
}

// readSpoolFile reads one spool entry without following a symlink and
// without reading past the size bound. A symlink, a directory or an
// oversized file is not ours: it is removed and logged.
func readSpoolFile(path string) ([]byte, bool) {
	// O_NONBLOCK: a FIFO planted in the spool must not block the daemon.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			commsLog.Warn("comms_spool_entry_rejected", slog.String("path", path), slog.String("reason", "symlink"))
			_ = os.Remove(path)
		}
		return nil, false
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	if !info.Mode().IsRegular() {
		commsLog.Warn("comms_spool_entry_rejected", slog.String("path", path), slog.String("reason", "not a regular file"))
		_ = os.Remove(path)
		return nil, false
	}
	if info.Size() > commsSpoolMaxBytes {
		commsLog.Warn("comms_spool_entry_rejected", slog.String("path", path), slog.String("reason", "oversized"), slog.Int64("bytes", info.Size()))
		_ = os.Remove(path)
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, commsSpoolMaxBytes+1))
	if err != nil {
		return nil, false
	}
	return data, true
}

// RemoveCommsSpoolEntry deletes one ingested entry.
func RemoveCommsSpoolEntry(e CommsSpoolEntry) {
	if e.path != "" {
		_ = os.Remove(e.path)
	}
}

// QuarantineCommsSpoolEntry moves an entry whose identity conflicts with a
// committed record to <spool>/conflict/<instance>/ for a human to look at.
func QuarantineCommsSpoolEntry(e CommsSpoolEntry) {
	if e.path == "" {
		return
	}
	dir := filepath.Join(CommsSpoolDir(), "conflict", sanitizeInboxName(e.Instance))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.Rename(e.path, filepath.Join(dir, filepath.Base(e.path)))
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
// instance directories. Called only while the ledger is OFF (nothing will
// ever consume the spool then); every expiry is logged. With the ledger on
// an entry stays until it commits and the per-instance cap bounds an
// outage.
func PruneCommsSpool(now time.Time) {
	root := CommsSpoolDir()
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "conflict" {
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
				commsLog.Warn("comms_spool_expired", slog.String("instance", d.Name()), slog.String("entry", f.Name()))
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
