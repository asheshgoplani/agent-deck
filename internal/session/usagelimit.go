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

// usageLimitFirstTailBytes is the first window read, sized for the common case:
// a real transcript has assistant records throughout, so this answers in one read.
const usageLimitFirstTailBytes = 512 * 1024

// usageLimitTailGrowth multiplies the window when a read found no
// main-conversation assistant record.
//
// There is deliberately NO ceiling. A fixed one does not remove the cold-start
// failure it appears to bound, it just moves it further out: if the decisive
// rejection sits beyond the cap and everything after it is non-assistant traffic,
// every step returns no verdict, and a fresh process then starts with no memo and
// reports the session as healthy — the false negative this detector exists to
// prevent. Growth instead terminates on reading the whole file, so the answer is
// always eventually found. The throttle bounds how often this can happen, and the
// pathological case (no assistant record in tens of megabytes) is not what a real
// transcript looks like.
const usageLimitTailGrowth = 8

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
	for tail := int64(usageLimitFirstTailBytes); ; tail *= usageLimitTailGrowth {
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
			// The whole file has been read and holds no assistant turn at all.
			return false, false
		}
	}
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

// transcriptBelongsToSession reports whether a resolved transcript path is the
// one for sessionID.
//
// Every branch of the resolver builds "<config>/projects/<encoded>/<sid>.jsonl",
// so the basename is the session identity — which makes this a cheap way to
// refuse a path that belongs to some other conversation, whatever produced it.
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
	cached := i.usageLimitedCached
	i.mu.Unlock()

	path := locateHandoffTranscript(i)
	if path == "" {
		return cached
	}
	// Verify the answer belongs to the id we gated on. Holding the lock across the
	// check is not enough on its own: the resolver re-reads ClaudeSessionID itself
	// (migrate_locate.go) after the lock is released, so a concurrent rebind could
	// still steer it onto the newest-conversation fallback. Checking the resolved
	// path makes the guarantee structural instead of dependent on timing.
	if !transcriptBelongsToSession(path, sessionID) {
		return cached
	}

	limited, ok := latestAssistantTurnIsRateLimited(path, time.Now())
	if !ok {
		// No formed verdict (unreadable, or no assistant turn in the file): leave
		// the previous answer standing rather than silently reporting "not
		// limited".
		return cached
	}

	i.mu.Lock()
	// Publish only if this scan is still the current one AND the instance is still
	// bound to the id it was formed for. Either check failing means a newer claim
	// or a rebind happened while the read was in flight, and this result is stale.
	if i.usageLimitScanGen == gen && i.usageLimitSessionID == sessionID {
		i.usageLimitedCached = limited
	} else {
		limited = i.usageLimitedCached
	}
	i.mu.Unlock()

	return limited
}
