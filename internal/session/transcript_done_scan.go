package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the transcript-tail scan behind completion-sentinel
// detection (issue #1186). It lives in internal/session because TWO callers
// need it: the Stop-hook handler (cmd/agent-deck) for the immediate scan, and
// the transition daemon for the flush-race rescan — Claude Code can fire the
// Stop hook BEFORE appending the turn's final assistant record, and the hook,
// synchronous since issue #1225, must not sleep waiting for the flush. The
// hook persists the transcript path into the hook status file instead, and
// the daemon's poll loop becomes the retry.

// transcriptContentMessage extracts the assistant message content blocks from
// a transcript line, for completion-sentinel detection.
type transcriptContentMessage struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// doneScanTailLines bounds the backward walk over transcript records when
// looking for the just-finished assistant turn. Post-assistant noise observed
// in the wild is 1-4 records (system/attachment); 25 leaves generous margin
// without rescanning history.
const doneScanTailLines = 25

// ValidateTranscriptPath cleans a Claude Code transcript path and applies the
// same traversal / containment guards as the hook cost path: no "..", and the
// path must live under one of the transcript roots (transcriptRoots). Both
// the hook handler (payload-supplied path) and the daemon (path re-read from
// a hook status file) gate on this before opening the file.
//
// Messaging audit P1-2: the only root used to be ~/.claude, but every
// agent-deck-launched Claude runs with CLAUDE_CONFIG_DIR pointing at a
// worker-scratch home or a named account slot and reports transcript_path
// under THAT directory, so the sentinel scan was skipped for every worker and
// no [DONE] ever became a finished event. The roots now also include
// $CLAUDE_CONFIG_DIR, every configured account slot, the global
// [claude].config_dir and the worker-scratch root.
//
// Containment is fail-closed and boundary-aware, in two stages. Lexically the
// path must equal a root or begin with root + separator (a raw HasPrefix would
// accept ~/.claude-spoof/x.jsonl). Then both the path (resolved on its deepest
// existing ancestor, so a symlinked parent with a not-yet-flushed leaf still
// resolves) and the roots are symlink-resolved and the REAL location must
// again sit under a real root: a symlink under a root that points outside
// every root is rejected. If no root can be established (home unresolvable)
// the path is rejected rather than falling through.
func ValidateTranscriptPath(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") {
		return "", false
	}
	roots := transcriptRoots()
	if len(roots) == 0 {
		return "", false
	}
	if !containedUnderAny(cleanPath, roots) {
		return "", false
	}
	realRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		realRoots = append(realRoots, resolveCanonical(r))
	}
	if !containedUnderAny(resolveProbeTarget(cleanPath), realRoots) {
		return "", false
	}
	return cleanPath, true
}

// transcriptRoots returns the absolute, cleaned directories a Claude Code
// transcript may legitimately live under: ~/.claude, $CLAUDE_CONFIG_DIR (the
// hook handler inherits it from the session), every [profiles.<name>.claude]
// config_dir account slot, the global [claude].config_dir, every
// [conductors.<name>.claude] and [groups."<path>".claude] config_dir (the
// same dirs resolveClaudeConfigDir can launch a session under; review round
// 2, P2-C), and the worker-scratch root (whose per-session homes symlink
// `projects` back into the owning config dir). Empty when the home directory
// cannot be resolved.
func transcriptRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	seen := map[string]bool{}
	var roots []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(ExpandPath(dir))
		if !filepath.IsAbs(dir) || seen[dir] {
			return
		}
		seen[dir] = true
		roots = append(roots, dir)
	}
	add(filepath.Join(home, ".claude"))
	add(os.Getenv("CLAUDE_CONFIG_DIR"))
	if cfg, _ := LoadUserConfig(); cfg != nil {
		for _, name := range ConfiguredAccountNames(cfg) {
			add(cfg.GetProfileClaudeConfigDir(name))
		}
		add(cfg.Claude.ConfigDir)
		for _, c := range cfg.Conductors {
			add(c.Claude.ConfigDir)
		}
		for _, g := range cfg.Groups {
			add(g.Claude.ConfigDir)
		}
	}
	add(workerScratchDirRoot())
	return roots
}

// ScanTranscriptTailForDone scans the transcript tail for a completion
// sentinel in the just-stopped MAIN-CHAIN assistant turn. The sentinel-bearing
// assistant record is NOT reliably the literal last line: Claude Code appends
// system / attachment records after the assistant turn, and sidechain
// (subagent) records interleave freely. Walk backwards over a bounded tail
// window, skipping sidechain traffic and non-turn records, until the first
// main-chain assistant or user record:
//
//   - assistant: this IS the turn that just stopped — scan it for the
//     sentinel. pending=false.
//   - user: the just-stopped turn's reply has not been appended yet (the Stop
//     hook outran the transcript flush). Scanning past it would re-read the
//     PREVIOUS turn's sentinel — the observed deterministic one-turn lag — so
//     report pending=true and let the caller retry once the record lands.
//   - window exhausted: nothing conclusive in the tail; also pending=true
//     (the flushed record, once appended, lands at the very end and the next
//     scan sees it immediately).
//
// A missing/unreadable file yields pending=false so callers never spin on a
// path that will not resolve.
func ScanTranscriptTailForDone(path string) (sig DoneSignal, found bool, pending bool) {
	lines, err := TranscriptTailLines(path, doneScanTailLines)
	if err != nil {
		return DoneSignal{}, false, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var msg transcriptContentMessage
		if err := json.Unmarshal([]byte(lines[i]), &msg); err != nil {
			continue
		}
		if msg.IsSidechain {
			continue
		}
		switch msg.Type {
		case "assistant":
			sig, found = ScanDoneSentinel(transcriptText(msg.Message.Content))
			return sig, found, false
		case "user":
			return DoneSignal{}, false, true
		}
	}
	return DoneSignal{}, false, true
}

// transcriptText flattens an assistant message's content into plain text.
// Claude transcripts encode content either as a string or as an array of
// typed blocks ({"type":"text","text":"..."}); only text blocks contribute.
func transcriptText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// TranscriptTailLines returns up to n trailing non-empty lines of the file,
// oldest first. It reads at most the trailing 512KB — transcript records are
// single JSONL lines comfortably under that; a line truncated by the byte cut
// is discarded rather than half-parsed.
func TranscriptTailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path) // #nosec G304 -- callers validate via ValidateTranscriptPath or supply test paths
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	if size == 0 {
		return nil, fmt.Errorf("empty file")
	}

	const maxTailBytes = int64(512 * 1024)
	offset := int64(0)
	if size > maxTailBytes {
		offset = size - maxTailBytes
	}
	buf := make([]byte, size-offset)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil, err
	}

	rawLines := strings.Split(strings.TrimRight(string(buf), "\n\r "), "\n")
	if offset > 0 && len(rawLines) > 0 {
		rawLines = rawLines[1:] // first line may be cut mid-record by the offset
	}
	start := 0
	if len(rawLines) > n {
		start = len(rawLines) - n
	}
	lines := make([]string, 0, n)
	for _, raw := range rawLines[start:] {
		if s := strings.TrimSpace(raw); s != "" {
			lines = append(lines, s)
		}
	}
	return lines, nil
}
