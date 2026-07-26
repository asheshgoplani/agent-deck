package session

import (
	"bytes"
	"encoding/json"
	"os"
)

// contextTailReadInitial is the first tail window read from a transcript when
// looking for the newest assistant usage record. Most transcripts have an
// assistant line within the last few KB, but a single tool-result or
// compaction record can be multi-megabyte, so the window grows (×4, capped)
// until an assistant entry is found or the whole file has been scanned.
const (
	contextTailReadInitial = 256 * 1024
	contextTailReadMax     = 8 * 1024 * 1024
)

// CurrentContextTokensForInstance returns the instance's current context size
// in tokens — the prompt size of the newest assistant turn in its Claude
// transcript (input + cache-read + cache-creation tokens), which is what the
// next request will roughly resend. ok is false when the instance has no
// resolvable transcript (non-Claude tool, no session id yet, file missing) or
// no assistant turn with usage has been written yet.
//
// Unlike ParseSessionJSONL this reads only the transcript tail, so it is cheap
// enough to run per child on every `session children` poll.
func CurrentContextTokensForInstance(inst *Instance) (int, bool) {
	path := ClaudeTranscriptPathForInstance(inst)
	if path == "" {
		return 0, false
	}
	return currentContextTokensFromTail(path)
}

// currentContextTokensFromTail scans the tail of a Claude JSONL transcript for
// the newest assistant entry carrying token usage and returns its prompt size.
func currentContextTokensFromTail(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return 0, false
	}
	size := info.Size()

	for window := int64(contextTailReadInitial); ; window *= 4 {
		if window > contextTailReadMax {
			window = contextTailReadMax
		}
		offset := size - window
		if offset < 0 {
			offset = 0
		}
		buf := make([]byte, size-offset)
		if _, err := f.ReadAt(buf, offset); err != nil {
			return 0, false
		}
		lines := bytes.Split(buf, []byte("\n"))
		// A mid-file offset almost certainly landed inside a line; drop the
		// partial first fragment so it can't half-parse.
		if offset > 0 && len(lines) > 0 {
			lines = lines[1:]
		}
		if tokens, ok := lastAssistantContextTokens(lines); ok {
			return tokens, true
		}
		if offset == 0 || window >= contextTailReadMax {
			return 0, false
		}
	}
}

// lastAssistantContextTokens returns the prompt size of the last assistant
// entry with usage among the given JSONL lines.
func lastAssistantContextTokens(lines [][]byte) (int, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var entry jsonlEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" {
			continue
		}
		u := entry.Message.Usage
		tokens := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		if tokens == 0 {
			continue
		}
		return tokens, true
	}
	return 0, false
}
