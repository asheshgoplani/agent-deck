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
// That is deliberate, and worth recording because the pane looks like the
// obvious source: the rejection renders behind the "⎿" tool-result connector,
// wraps across visual lines on a narrow pane (agent-deck captures with `-p -e`
// and no `-J`, so soft wraps arrive as real newlines), and shares its vocabulary
// with ordinary output — a session running `grep` over this repository prints the
// same words. Every one of those defeats a text scanner. The transcript instead
// carries structured fields:
//
//	{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,
//	 "error":"rate_limit","timestamp":"2026-07-30T17:03:25.225Z", ...}
//
// So the verdict keys off apiErrorStatus + error, not off prose.

// usageLimitRateLimitStatus and usageLimitErrorKind are BOTH required.
//
// 429 alone is a general rate-limit code that other API errors also carry, and
// mislabelling one of those as plan exhaustion would then persist for the whole
// freshness window. Every field sample carries the pair, so the pair is the
// contract; if a sample is ever observed carrying only one of them, relax this
// with that evidence in hand rather than pre-emptively.
const (
	usageLimitRateLimitStatus = 429
	usageLimitErrorKind       = "rate_limit"
)

// usageLimitMaxAge bounds how long a rejection is believed.
//
// The verdict is "the latest assistant turn was a quota rejection", which clears
// itself the moment a real turn completes — but only if one ever does. A session
// that receives no further input after the window reopens would otherwise stay
// classified from that old rejection indefinitely. Rather than parse the
// advertised reset text ("resets 8:50pm (UTC)"), which needs 12-hour parsing, a
// timezone and day-rollover handling and is still only a promise, this bounds
// belief by the length of the rolling window itself.
//
// The erring direction is deliberate: where a plan uses a longer cap (a weekly
// one), this clears early and reports the session as unremarkable — which is the
// pre-existing behaviour, not a new false positive.
const usageLimitMaxAge = 5 * time.Hour

// usageLimitScanInterval throttles the transcript read. Substate is computed per
// session per status poll, so an unbounded read would put file I/O on a path
// that runs for every session in a fleet. The condition it detects lasts hours,
// so a few seconds of staleness costs nothing.
const usageLimitScanInterval = 5 * time.Second

// usageLimitTailSteps are the tail sizes tried in order until a
// main-conversation assistant record is found.
//
// One fixed window is not enough. A single oversized record (a file-history
// snapshot, a compaction) can fill it, and later non-assistant traffic can push
// the decisive rejection out of it. When that happens a long-lived process keeps
// its memo, but a fresh CLI invocation starts with no memo and would report the
// session as healthy — the exact false negative this detector exists to prevent.
// So escalate rather than give up, and stop at a bound so a pathological
// transcript cannot turn a status poll into an unbounded read.
var usageLimitTailSteps = []int64{
	512 * 1024,
	8 * 1024 * 1024,
	64 * 1024 * 1024,
}

// transcriptRecord is the subset of a transcript line this detector reads.
type transcriptRecord struct {
	Type              string `json:"type"`
	IsSidechain       bool   `json:"isSidechain"`
	IsAPIErrorMessage bool   `json:"isApiErrorMessage"`
	APIErrorStatus    int    `json:"apiErrorStatus"`
	Error             string `json:"error"`
	Timestamp         string `json:"timestamp"`
	Message           struct {
		Role string `json:"role"`
	} `json:"message"`
}

// isRateLimitRejection reports whether this record is a plan-quota rejection.
// Requires the api-error flag AND both discriminators — see the constants above.
func (r transcriptRecord) isRateLimitRejection() bool {
	return r.IsAPIErrorMessage &&
		r.APIErrorStatus == usageLimitRateLimitStatus &&
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

// fresh reports whether the record's timestamp is within maxAge of now.
//
// An absent or unparseable timestamp is NOT treated as fresh: believing a
// rejection of unknown age is precisely what produces a stuck verdict. A
// timestamp in the future is likewise rejected rather than trusted.
func (r transcriptRecord) fresh(now time.Time, maxAge time.Duration) bool {
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Timestamp))
	if err != nil {
		return false
	}
	age := now.Sub(ts)
	return age >= 0 && age <= maxAge
}

// latestAssistantTurnIsRateLimited reports whether the MOST RECENT
// main-conversation assistant turn at path is a quota rejection recent enough to
// still be believed, plus whether a verdict could be formed at all.
//
// ok is false when no assistant turn was found within the escalation bound.
// Callers must treat that as "unknown", never as "fine" — silence being mistaken
// for health is the failure this whole detector exists to stop.
func latestAssistantTurnIsRateLimited(path string, now time.Time) (limited bool, ok bool) {
	for _, tail := range usageLimitTailSteps {
		lines, complete, err := readTranscriptTailLines(path, tail)
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
			if !rec.isRateLimitRejection() {
				return false, true
			}
			// A rejection older than the window is no longer evidence about now.
			return rec.fresh(now, usageLimitMaxAge), true
		}
		if complete {
			// The whole file was read and holds no assistant turn at all.
			return false, false
		}
	}
	return false, false
}

// readTranscriptTailLines returns the complete JSONL lines in the last tailBytes
// of path, and whether that read covered the whole file.
//
// The first line of a mid-file read is dropped: a byte offset lands wherever it
// lands, so that line is a fragment. Dropping it explicitly keeps the intent
// legible and stops a truncated record from being parsed as a whole one.
func readTranscriptTailLines(path string, tailBytes int64) (lines []string, complete bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}

	offset := int64(0)
	complete = true
	if info.Size() > tailBytes {
		offset = info.Size() - tailBytes
		complete = false
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, false, err
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false, err
	}

	split := strings.Split(string(data), "\n")
	if !complete && len(split) > 0 {
		split = split[1:]
	}
	out := make([]string, 0, len(split))
	for _, l := range split {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out, complete, nil
}

// usageLimited reports whether this instance's latest assistant turn was a
// recent quota rejection, throttled to usageLimitScanInterval.
func (i *Instance) usageLimited() bool {
	// Claude-compatible tools only: the transcript shape is Claude's.
	if !IsClaudeCompatible(i.Tool) {
		return false
	}
	// The transcript is a local file. An SSH-backed session keeps its
	// conversation on the remote host and stores a remote ProjectPath, so there
	// is no local path to resolve — bail before doing any path work rather than
	// resolving a meaningless local path and failing to open it forever.
	if i.IsSSH() {
		return false
	}
	// A bound session id is required, not merely helpful. With an empty id
	// LocateConversationConfigDir deliberately falls back to the NEWEST
	// conversation for the project across every config dir, so an unbound
	// instance would inherit whichever sibling session wrote last and be marked
	// usage-limited on another session's rejection.
	i.mu.RLock()
	sessionID := strings.TrimSpace(i.ClaudeSessionID)
	i.mu.RUnlock()
	if sessionID == "" {
		return false
	}

	// Check and stamp in ONE critical section. Split across an RLock read and a
	// later Lock, two callers whose window had expired could both pass the check,
	// both scan, and publish their results in either order.
	i.mu.Lock()
	if !i.lastUsageLimitScanAt.IsZero() && time.Since(i.lastUsageLimitScanAt) < usageLimitScanInterval {
		cached := i.usageLimitedCached
		i.mu.Unlock()
		return cached
	}
	// Claim the window before releasing the lock so a concurrent caller sees it
	// taken. The I/O below deliberately runs unlocked.
	i.lastUsageLimitScanAt = time.Now()
	cached := i.usageLimitedCached
	i.mu.Unlock()

	path := locateHandoffTranscript(i)
	if path == "" {
		return cached
	}

	limited, ok := latestAssistantTurnIsRateLimited(path, time.Now())
	if !ok {
		// No formed verdict (unreadable, or no assistant turn within the
		// escalation bound): leave the previous answer standing rather than
		// silently reporting "not limited".
		return cached
	}

	i.mu.Lock()
	i.usageLimitedCached = limited
	i.mu.Unlock()

	return limited
}
