package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Per-child turn journal (issue #2469). Every finished turn the notify-daemon
// observes becomes exactly one compact line here, whatever its tier, so
// nothing a child did is ever lost even when it is deliberately not delivered
// to the parent (noise), and so a parent's delta can be rebuilt from the
// journal after an inbox overflow or dead letter. The journal is also what
// the tier rule compares against: the previous line's text hash and status
// decide whether the current turn is news.
//
// Layout: <data>/runtime/turn-journal/<child>.jsonl, append-only with fsync,
// trimmed to [inbox] journal_keep lines (default 256) by an atomic tail
// rewrite once it grows past the cap.

// TurnJournalEntry is one journaled turn. Field names are short on purpose:
// the file is read back on every classification.
type TurnJournalEntry struct {
	Seq         int64     `json:"seq"`
	TS          time.Time `json:"ts"`
	Child       string    `json:"child"`
	Profile     string    `json:"profile,omitempty"`
	Status      string    `json:"status"`
	Tier        string    `json:"tier"`
	Trigger     string    `json:"trigger,omitempty"`
	UUID        string    `json:"uuid,omitempty"`
	TextHash    string    `json:"th,omitempty"`
	Text        string    `json:"text,omitempty"`
	DoneStatus  string    `json:"done,omitempty"`
	DoneSummary string    `json:"summary,omitempty"`
	Question    bool      `json:"q,omitempty"`
	FromID      string    `json:"from_id,omitempty"`
}

// DefaultTurnJournalKeep bounds a child's journal when [inbox] journal_keep
// is unset.
const DefaultTurnJournalKeep = 256

// turnJournalMu serialises journal appends within the process. The daemon is
// single-threaded; this guards tests and any future second writer.
var turnJournalMu sync.Mutex

// runtimeDirOrTemp resolves <data>/runtime/<name>, falling back to a temp
// path so a data-path failure degrades the feature, never the daemon.
func runtimeDirOrTemp(name string) string {
	dir, err := runtimeDataPath(name)
	if err != nil {
		return tempAgentDeckPath("runtime", name)
	}
	return dir
}

// TurnJournalDir is the journal root.
func TurnJournalDir() string {
	return runtimeDirOrTemp("turn-journal")
}

// TurnJournalPath is the journal file for one child.
func TurnJournalPath(childID string) string {
	return filepath.Join(TurnJournalDir(), sanitizeInboxName(childID)+".jsonl")
}

// AppendTurnJournal appends one entry, assigning the next per-child seq, and
// returns the stored entry. The append is O_APPEND + fsync; the trim past the
// cap is an atomic rewrite.
func AppendTurnJournal(entry TurnJournalEntry, keep int) (TurnJournalEntry, error) {
	return writeTurnJournal(entry, keep, false)
}

// UpsertTurnJournal is AppendTurnJournal keeping one line per turn (issue
// #2481): an entry whose uuid matches the newest line is a re-observation of
// that turn (an info turn that escalated to urgent, a stale-signal flip) and
// replaces that line instead of adding a second one. It still takes the next
// seq, so a reader that already read the old line sees the change. Journals
// written before this (with repeat lines) stay readable as they are.
func UpsertTurnJournal(entry TurnJournalEntry, keep int) (TurnJournalEntry, error) {
	return writeTurnJournal(entry, keep, true)
}

func writeTurnJournal(entry TurnJournalEntry, keep int, replaceSameTurn bool) (TurnJournalEntry, error) {
	if strings.TrimSpace(entry.Child) == "" {
		return entry, errors.New("turn journal: empty child id")
	}
	if keep <= 0 {
		keep = DefaultTurnJournalKeep
	}
	turnJournalMu.Lock()
	defer turnJournalMu.Unlock()

	path := TurnJournalPath(entry.Child)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return entry, err
	}
	last, _ := lastTurnJournalEntryLocked(path)
	entry.Seq = 1
	if last != nil {
		entry.Seq = last.Seq + 1
	}
	if entry.TS.IsZero() {
		entry.TS = time.Now()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return entry, err
	}
	line = append(line, '\n')
	if replaceSameTurn && last != nil && entry.UUID != "" && last.UUID == entry.UUID {
		return entry, replaceLastTurnJournalLocked(path, line)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return entry, err
	}
	// A crash mid-append leaves a torn last line; starting this line on a
	// fresh row keeps the torn one skippable instead of fusing the two.
	if !fileEndsWithNewline(path) {
		line = append([]byte{'\n'}, line...)
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return entry, err
	}
	if err := fsyncFile(f); err != nil {
		_ = f.Close()
		return entry, err
	}
	if err := f.Close(); err != nil {
		return entry, err
	}
	return entry, trimTurnJournalLocked(path, keep)
}

// LastTurnJournalEntry returns the newest journaled turn for a child, or nil.
func LastTurnJournalEntry(childID string) *TurnJournalEntry {
	turnJournalMu.Lock()
	defer turnJournalMu.Unlock()
	last, _ := lastTurnJournalEntryLocked(TurnJournalPath(childID))
	return last
}

// ReadTurnJournal returns the child's journaled turns with Seq > sinceSeq,
// oldest first. sinceSeq 0 returns everything retained.
func ReadTurnJournal(childID string, sinceSeq int64) ([]TurnJournalEntry, error) {
	turnJournalMu.Lock()
	defer turnJournalMu.Unlock()
	entries, err := readTurnJournalLocked(TurnJournalPath(childID))
	if err != nil {
		return nil, err
	}
	out := entries[:0]
	for _, e := range entries {
		if e.Seq > sinceSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

// RemoveTurnJournal deletes a child's journal (session removal sweep).
func RemoveTurnJournal(childID string) {
	turnJournalMu.Lock()
	defer turnJournalMu.Unlock()
	_ = os.Remove(TurnJournalPath(childID))
}

func readTurnJournalLocked(path string) ([]TurnJournalEntry, error) {
	f, err := os.Open(path) // #nosec G304 -- path derived from a sanitized child id under the data dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []TurnJournalEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxInboxLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e TurnJournalEntry
		if json.Unmarshal(line, &e) != nil {
			continue // a torn line is skipped, never fatal
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func lastTurnJournalEntryLocked(path string) (*TurnJournalEntry, error) {
	lines, err := TranscriptTailLines(path, 1)
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	var e TurnJournalEntry
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &e); err != nil {
		// The newest line is torn; fall back to a full read for the last good one.
		entries, rerr := readTurnJournalLocked(path)
		if rerr != nil || len(entries) == 0 {
			return nil, rerr
		}
		return &entries[len(entries)-1], nil
	}
	return &e, nil
}

// replaceLastTurnJournalLocked atomically rewrites the journal with its newest
// parseable line replaced by line (torn lines are dropped on the way).
func replaceLastTurnJournalLocked(path string, line []byte) error {
	entries, err := readTurnJournalLocked(path)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range entries[:max(len(entries)-1, 0)] {
		old, err := json.Marshal(e)
		if err != nil {
			continue
		}
		buf.Write(old)
		buf.WriteByte('\n')
	}
	buf.Write(line)
	return writeFileDurable(path, buf.Bytes(), 0o600)
}

// trimTurnJournalLocked keeps the last keep lines. Cheap in steady state: a
// file that cannot hold more than keep lines is left alone after one stat.
func trimTurnJournalLocked(path string, keep int) error {
	const minLineBytes = 64
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() <= int64(keep)*minLineBytes {
		return nil
	}
	entries, err := readTurnJournalLocked(path)
	if err != nil {
		return err
	}
	if len(entries) <= keep {
		return nil
	}
	var buf bytes.Buffer
	for _, e := range entries[len(entries)-keep:] {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return writeFileDurable(path, buf.Bytes(), 0o600)
}

// InboxConfig is the [inbox] section of config.toml: the tiering knobs for
// what reaches a parent and when. Every field is optional; zero means the
// default documented on the accessor. [conductors.<name>.inbox] overrides it
// for one conductor.
type InboxConfig struct {
	// WakeOn lists the tiers that wake an idle parent immediately. Default
	// ["urgent"]. ["urgent","info"] restores a wake per recorded turn.
	WakeOn []string `toml:"wake_on,omitempty"`
	// MaxTextBytes caps the child text carried on a record (default 600,
	// hard max 2048).
	MaxTextBytes int `toml:"max_text_bytes,omitzero"`
	// InfoDigestMinutes is how long info may wait for a turn the parent takes
	// anyway before a digest wakes an idle parent (default 15; 0 = never
	// wake for info).
	InfoDigestMinutes *int `toml:"info_digest_minutes,omitempty"`
	// QuestionWakes treats a trailing "?" / NEED: / QUESTION: line as urgent
	// (default true).
	QuestionWakes *bool `toml:"question_wakes,omitempty"`
	// JournalKeep bounds each child's turn journal (default 256 lines).
	JournalKeep int `toml:"journal_keep,omitzero"`
}

// merged returns c with any non-zero field of o applied on top.
func (c InboxConfig) merged(o *InboxConfig) InboxConfig {
	if o == nil {
		return c
	}
	if len(o.WakeOn) > 0 {
		c.WakeOn = o.WakeOn
	}
	if o.MaxTextBytes > 0 {
		c.MaxTextBytes = o.MaxTextBytes
	}
	if o.InfoDigestMinutes != nil {
		c.InfoDigestMinutes = o.InfoDigestMinutes
	}
	if o.QuestionWakes != nil {
		c.QuestionWakes = o.QuestionWakes
	}
	if o.JournalKeep > 0 {
		c.JournalKeep = o.JournalKeep
	}
	return c
}

// WakesFor reports whether a record of the given tier wakes an idle parent.
// An empty record tier (a record from an older producer) counts as urgent so
// old children keep waking the parent as before.
func (c InboxConfig) WakesFor(tier string) bool {
	if tier == "" {
		return true
	}
	wake := c.WakeOn
	if len(wake) == 0 {
		wake = []string{TurnTierUrgent}
	}
	for _, w := range wake {
		if strings.EqualFold(strings.TrimSpace(w), tier) {
			return true
		}
	}
	return false
}

// GetMaxTextBytes returns the text cap with the default and ceiling applied.
func (c InboxConfig) GetMaxTextBytes() int {
	return clampTurnTextBytes(c.MaxTextBytes)
}

// GetInfoDigestMinutes returns the digest window (default 15).
func (c InboxConfig) GetInfoDigestMinutes() int {
	if c.InfoDigestMinutes == nil {
		return 15
	}
	if *c.InfoDigestMinutes < 0 {
		return 0
	}
	return *c.InfoDigestMinutes
}

// GetQuestionWakes returns whether questions are urgent (default true).
func (c InboxConfig) GetQuestionWakes() bool {
	return c.QuestionWakes == nil || *c.QuestionWakes
}

// GetJournalKeep returns the journal cap (default 256).
func (c InboxConfig) GetJournalKeep() int {
	if c.JournalKeep <= 0 {
		return DefaultTurnJournalKeep
	}
	return c.JournalKeep
}

// inboxConfigOverride is a test seam: when set it replaces the loaded config.
var inboxConfigOverride *InboxConfig

// ResolveInboxConfig returns the [inbox] settings for a parent, with the
// conductor's [conductors.<name>.inbox] override applied when the parent title
// is conductor-<name>. A missing config yields the defaults.
func ResolveInboxConfig(parentTitle string) InboxConfig {
	if inboxConfigOverride != nil {
		return *inboxConfigOverride
	}
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		return InboxConfig{}
	}
	out := cfg.Inbox
	title := strings.ToLower(strings.TrimSpace(parentTitle))
	if name, ok := strings.CutPrefix(title, "conductor-"); ok && cfg.Conductors != nil {
		if c, ok := cfg.Conductors[name]; ok {
			out = out.merged(c.Inbox)
		}
	}
	return out
}

// fileEndsWithNewline reports whether path is empty, missing, or ends in \n.
func fileEndsWithNewline(path string) bool {
	f, err := os.Open(path) // #nosec G304 -- sanitized id under the data dir
	if err != nil {
		return true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return true
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], info.Size()-1); err != nil {
		return true
	}
	return b[0] == '\n'
}
