package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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
// classified from that old rejection indefinitely.
//
// KNOWN LIMITATION, accepted deliberately: this bounds the staleness but does not
// track the ACTUAL reset. The banner carries one ("resets 8:50pm (UTC)") and this
// does not read it, so a rejection stamped 20:35 whose window reopened at 20:50 is
// still believed until 01:35 — up to ~5h of false usage-limit in the
// no-further-input case. The cost is bounded and is a mislabel rather than an
// action, since nothing consumes this substate yet; the alternative is parsing a
// human-facing 12-hour local-time string (timezone, day rollover, wording drift)
// to obtain a promise rather than an observation. Revisit when a consumer starts
// acting on the verdict, and prefer a revalidation signal over parsing the prose.
//
// The erring direction of the bound itself is also deliberate: where a plan uses a
// longer cap (a weekly one), this clears early and reports the session as
// unremarkable — the pre-existing behaviour, not a new false positive.
const usageLimitMaxAge = 5 * time.Hour

// usageLimitScanInterval throttles the transcript read. Substate is computed per
// session per status poll, so an unbounded read would put file I/O on a path
// that runs for every session in a fleet. The condition it detects lasts hours,
// so a few seconds of staleness costs nothing.
const usageLimitScanInterval = 5 * time.Second

// usageLimitScanChunkBytes is how much of the transcript is read at a time.
//
// The scan walks BACKWARD in non-overlapping chunks of this size until it finds
// the latest main-conversation assistant record or reaches the start of the file.
// Two properties matter and neither is optional:
//
//   - No ceiling. A fixed cap does not remove the cold-start false negative it
//     appears to bound, it relocates it: if the decisive rejection sits beyond the
//     cap with only non-assistant traffic after it, no verdict forms and a fresh
//     process reports the session healthy.
//   - No overlap, bounded memory. An earlier revision grew the window
//     geometrically and re-read what it had already seen — 512 KiB + 4 MiB + the
//     whole file for one verdict on a 5.8 MB transcript, with the final read
//     copied whole into memory. Chunking reads each byte at most once and holds
//     one chunk at a time, which matters because this runs on a status path.
//
// A var rather than a const so tests can shrink it and exercise a many-chunk walk
// without writing a multi-megabyte fixture.
var usageLimitScanChunkBytes int64 = 512 * 1024

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
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, false
	}

	chunk := usageLimitScanChunkBytes
	if chunk <= 0 {
		chunk = 512 * 1024
	}

	// carry holds the leftmost, still-incomplete line of the chunk processed
	// previously (which sits LATER in the file). Its head is at the end of the
	// chunk about to be read, so appending it there reassembles that line exactly
	// once — no byte is read twice and no record is split across the boundary.
	var carry []byte

	for end := info.Size(); end > 0; {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		buf := make([]byte, end-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return false, false
		}
		data := append(buf, carry...)

		lines := strings.Split(string(data), "\n")
		// Unless this chunk starts at byte 0, its first line began earlier in the
		// file and must not be parsed yet.
		firstComplete := 0
		if start > 0 {
			firstComplete = 1
			carry = []byte(lines[0])
		} else {
			carry = nil
		}

		for i := len(lines) - 1; i >= firstComplete; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var rec transcriptRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
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
		end = start
	}
	// The whole file was read and holds no main-conversation assistant turn.
	return false, false
}

// usageLimitPublishable reports whether a finished scan may write its result to
// the memo.
//
// Split out as a pure function because it is the rule that makes concurrent scans
// safe, and racing two real scans to exercise it is not something a test can do
// deterministically. A scan may publish only if it is still the current claim AND
// the instance is still bound to the session it read.
func usageLimitPublishable(currentGen, scanGen uint64, currentSessionID, scanSessionID string) bool {
	return currentGen == scanGen && currentSessionID == scanSessionID
}

// usageLimitedNow reads the memo under the lock.
//
// Every post-claim exit goes through this rather than returning the snapshot taken
// at claim time. Returning the snapshot let a slow scan report a value that a
// newer scan had already superseded: the memo stayed correct, but callers observed
// completion order inverted.
func (i *Instance) usageLimitedNow() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.usageLimitedCached
}

// transcriptBelongsToSession reports whether a resolved transcript path is the
// one for sessionID.
//
// Every branch of the resolver builds "<config>/projects/<encoded>/<sid>.jsonl",
// so the basename is the session identity — which makes this a cheap way to refuse
// a path belonging to some other conversation, whatever produced it. Locking the
// id check alone cannot do that, because the resolver re-reads ClaudeSessionID
// itself after the lock is released.
func transcriptBelongsToSession(path, sessionID string) bool {
	if path == "" || sessionID == "" {
		return false
	}
	return filepath.Base(path) == sessionID+".jsonl"
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

	// Read the session id, check the throttle and claim the window in ONE critical
	// section. Split apart, two callers whose window had expired could both pass
	// the check, both scan, and publish their results in either order — and the id
	// could change between its own read and the claim.
	//
	// A bound session id is required, not merely helpful: with an empty id
	// LocateConversationConfigDir deliberately falls back to the NEWEST
	// conversation for the project across every config dir, so an unbound instance
	// would inherit whichever sibling session wrote last.
	i.mu.Lock()
	sessionID := strings.TrimSpace(i.ClaudeSessionID)
	if sessionID == "" {
		i.mu.Unlock()
		return false
	}
	// The memo carries the id it was formed for. A session normally rebinds
	// (A→B) on the same Instance, and an identity-less memo would hand B the
	// verdict formed for A — indefinitely, if B never produces an assistant turn
	// of its own. A mismatch is therefore not a cache miss to be papered over but
	// a reason to discard both the verdict and the throttle claim.
	if i.usageLimitSessionID != sessionID {
		i.usageLimitSessionID = sessionID
		i.usageLimitedCached = false
		i.lastUsageLimitScanAt = time.Time{}
	}
	if !i.lastUsageLimitScanAt.IsZero() && time.Since(i.lastUsageLimitScanAt) < usageLimitScanInterval {
		cached := i.usageLimitedCached
		i.mu.Unlock()
		return cached
	}
	// Claim the window before releasing the lock so a concurrent caller sees it
	// taken, and take a generation stamp. The stamp records scan START, so a scan
	// slower than the interval can overlap the next claim; the generation is what
	// stops the older one publishing over the newer result.
	i.lastUsageLimitScanAt = time.Now()
	i.usageLimitScanGen++
	gen := i.usageLimitScanGen
	i.mu.Unlock()

	path := locateHandoffTranscript(i)
	if path == "" {
		return i.usageLimitedNow()
	}
	// Verify the answer belongs to the id we gated on. Holding the lock across the
	// check is not enough on its own: the resolver re-reads ClaudeSessionID itself
	// (migrate_locate.go) after the lock is released, so a concurrent rebind could
	// still steer it onto the newest-conversation fallback. Checking the resolved
	// path makes the guarantee structural instead of dependent on timing.
	if !transcriptBelongsToSession(path, sessionID) {
		return i.usageLimitedNow()
	}

	limited, ok := latestAssistantTurnIsRateLimited(path, time.Now())
	if !ok {
		// No formed verdict (unreadable, or no assistant turn in the file): leave
		// the previous answer standing rather than silently reporting "not
		// limited".
		return i.usageLimitedNow()
	}

	i.mu.Lock()
	if usageLimitPublishable(i.usageLimitScanGen, gen, i.usageLimitSessionID, sessionID) {
		i.usageLimitedCached = limited
	} else {
		// A newer claim or a rebind happened while this read was in flight, so this
		// result is stale — report what the memo now holds instead.
		limited = i.usageLimitedCached
	}
	i.mu.Unlock()

	return limited
}
