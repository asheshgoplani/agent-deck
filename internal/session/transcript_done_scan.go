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
// path must live under ~/.claude (where Claude Code keeps transcripts). Both
// the hook handler (payload-supplied path) and the daemon (path re-read from
// a hook status file) gate on this before opening the file.
//
// Containment is fail-closed and boundary-aware. A raw HasPrefix against
// ~/.claude wrongly accepts sibling directories (e.g. ~/.claude-spoof/x.jsonl,
// whose string prefix matches but which lives outside the transcript root), so
// the path must equal the root exactly OR begin with root + path separator. If
// the home directory cannot be resolved we cannot establish the containment
// root, so we REJECT rather than fall through (a missing root must never
// disable containment for a payload-supplied path).
func ValidateTranscriptPath(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	root := filepath.Join(home, ".claude")
	if cleanPath != root && !strings.HasPrefix(cleanPath, root+string(os.PathSeparator)) {
		return "", false
	}
	return cleanPath, true
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
