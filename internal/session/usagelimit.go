package session

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// Usage-limit detection (#1802).
//
// A Claude session whose plan usage window is exhausted is healthy in every way
// agent-deck observes from the outside: the pane is alive, the composer accepts
// input, and every send is typed AND submitted. What fails is one layer further
// in — the API rejects the turn and it completes in zero seconds. Field evidence
// (2026-07-30): nine periodic sends were delivered and bounced this way over
// 4h24m. Each `session send` exited 0 — correctly, delivery genuinely succeeded
// — the systemd unit logged success every time, and the coarse status stayed
// "idle", which is exactly the state periodic senders require in order to keep
// sending. Nothing in the stack reported a problem.
//
// The signal is taken from Claude's own transcript rather than from pane text.
// That is a deliberate choice, and the reason is worth recording because the
// pane looks like the obvious source: the rejection renders behind the "⎿"
// tool-result connector, wraps across visual lines on a narrow pane (agent-deck
// captures with `-p -e` and no `-J`, so soft wraps arrive as real newlines), and
// shares its vocabulary with ordinary output — a session running `grep` over
// this repository would print the same words. Every one of those defeats a text
// scanner. The transcript instead carries structured fields:
//
//	{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,
//	 "error":"rate_limit","timestamp":"2026-07-30T17:03:25.225Z", ...}
//
// So the verdict keys off `apiErrorStatus` / `error`, not off prose. Nothing
// here needs to know how Claude words the banner, or how wide the pane is.

// usageLimitRateLimitStatus is the HTTP status Claude records on a quota
// rejection. Paired with the `error` discriminator below rather than trusted
// alone, since 429 is a general rate-limit code.
const usageLimitRateLimitStatus = 429

// usageLimitErrorKind is the `error` discriminator Claude writes alongside a
// quota rejection ("rate_limit"). Distinct from a credential failure, which
// carries its own status (401) and kind — the two need opposite responses:
// re-authenticating cannot help a quota window, and waiting cannot help an
// expired token.
const usageLimitErrorKind = "rate_limit"

// usageLimitScanInterval throttles the transcript read. Substate is computed per
// session per status poll, so an unbounded read would put file I/O on a path
// that runs for every session in a fleet. The condition it detects lasts hours,
// so a few seconds of staleness costs nothing.
const usageLimitScanInterval = 5 * time.Second

// usageLimitTranscriptTailBytes bounds how much of the transcript is read.
// Transcripts reach tens of megabytes (a 62 MB one was in the field evidence),
// and only the most recent turn matters, so read a tail rather than the file.
// Large enough to contain several records even when one carries a file-history
// snapshot.
const usageLimitTranscriptTailBytes = 512 * 1024

// transcriptRecord is the subset of a transcript line this detector reads.
type transcriptRecord struct {
	Type              string `json:"type"`
	IsSidechain       bool   `json:"isSidechain"`
	IsAPIErrorMessage bool   `json:"isApiErrorMessage"`
	APIErrorStatus    int    `json:"apiErrorStatus"`
	Error             string `json:"error"`
	Timestamp         string `json:"timestamp"`
	Message           struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"message"`
}

// isRateLimitRejection reports whether this record is a quota rejection.
//
// Requires the api-error flag AND one of the two structured discriminators. The
// flag alone is not enough (a 401 or an overloaded-model error also sets it),
// and matching the rendered text is exactly what this detector exists to avoid.
func (r transcriptRecord) isRateLimitRejection() bool {
	if !r.IsAPIErrorMessage {
		return false
	}
	return r.APIErrorStatus == usageLimitRateLimitStatus ||
		strings.EqualFold(strings.TrimSpace(r.Error), usageLimitErrorKind)
}

// isAssistantTurn reports whether this record is a main-conversation assistant
// turn. Sidechain records are subagent traffic, not the session's own turn, so a
// subagent hitting a limit must not classify its parent.
func (r transcriptRecord) isAssistantTurn() bool {
	if r.IsSidechain {
		return false
	}
	role := strings.TrimSpace(r.Message.Role)
	if role == "" {
		role = strings.TrimSpace(r.Type)
	}
	return role == "assistant"
}

// latestAssistantTurnIsRateLimited reports whether the MOST RECENT assistant
// turn in the transcript at path is a quota rejection, plus whether a verdict
// could be formed at all.
//
// "Most recent assistant turn" is the whole expiry story, and it is why this
// shape was chosen over parsing the advertised reset time ("resets 8:50pm
// (UTC)"). A reset timestamp would need 12-hour parsing, a timezone, and
// day-rollover handling, and would still be a promise rather than an
// observation. Whereas the moment the session completes any real turn, the
// latest assistant record is no longer an error and the verdict clears itself.
// Nothing has to expire it, and it cannot get stuck.
//
// ok is false when no assistant turn was found in the tail (a brand-new session,
// or a tail entirely consumed by one oversized record). Callers must treat that
// as "unknown", never as "fine" — silence is what this whole detector exists to
// stop being mistaken for health.
func latestAssistantTurnIsRateLimited(path string) (limited bool, ok bool) {
	lines, err := readTranscriptTailLines(path, usageLimitTranscriptTailBytes)
	if err != nil {
		return false, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var rec transcriptRecord
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
			continue
		}
		if !rec.isAssistantTurn() {
			continue
		}
		return rec.isRateLimitRejection(), true
	}
	return false, false
}

// readTranscriptTailLines returns the complete JSONL lines in the last tailBytes
// of path.
//
// The first line of a mid-file read is dropped: a byte offset lands wherever it
// lands, so that line is a fragment and would fail to parse anyway — dropping it
// explicitly keeps the intent legible.
func readTranscriptTailLines(path string, tailBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	offset := int64(0)
	truncated := false
	if info.Size() > tailBytes {
		offset = info.Size() - tailBytes
		truncated = true
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// usageLimited reports whether this instance's latest assistant turn was a quota
// rejection, throttled to usageLimitScanInterval and mirrored in memory so the
// render path can read it without touching the filesystem.
//
// Claude-compatible tools only: the transcript shape is Claude's.
func (i *Instance) usageLimited() bool {
	if !IsClaudeCompatible(i.Tool) {
		return false
	}

	i.mu.RLock()
	last := i.lastUsageLimitScanAt
	cached := i.usageLimitedCached
	i.mu.RUnlock()

	if !last.IsZero() && time.Since(last) < usageLimitScanInterval {
		return cached
	}

	path := locateHandoffTranscript(i)
	if path == "" {
		return cached
	}

	limited, ok := latestAssistantTurnIsRateLimited(path)

	i.mu.Lock()
	i.lastUsageLimitScanAt = time.Now()
	if ok {
		// Only a formed verdict updates the mirror. An unreadable or
		// assistant-less tail leaves the previous answer standing rather than
		// silently reporting "not limited".
		i.usageLimitedCached = limited
	}
	cached = i.usageLimitedCached
	i.mu.Unlock()

	return cached
}

// UsageLimitedCached reports the last computed verdict without any filesystem
// access, for the TUI render hot path.
func (i *Instance) UsageLimitedCached() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.usageLimitedCached
}
