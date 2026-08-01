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

// usageLimitScanChunkBytes is how much of the transcript is scanned for line
// boundaries at a time.
//
// The scan walks BACKWARD in non-overlapping chunks until it finds the latest
// main-conversation assistant record or reaches the start of the file. Two
// properties matter and neither is optional:
//
//   - No ceiling. A fixed cap does not remove the cold-start false negative it
//     appears to bound, it relocates it: if the decisive rejection sits beyond the
//     cap with only non-assistant traffic after it, no verdict forms and a fresh
//     process reports the session healthy.
//   - Bounded, non-overlapping I/O. Each chunk is read once to find newlines, and
//     each candidate line is then read once by its own bounded ReadAt. This runs on
//     a status path, so neither whole-file reads nor repeated recopying of a long
//     line is acceptable.
//
// A var rather than a const so tests can shrink it and exercise a many-chunk walk
// without writing a multi-megabyte fixture.
const defaultUsageLimitScanChunkBytes = 512 * 1024

var usageLimitScanChunkBytes int64 = defaultUsageLimitScanChunkBytes

// usageLimitMaxLineBytes bounds the size of a line this detector will read.
//
// Claude /compact writes multi-megabyte single-line records. Those are never the
// assistant turn being looked for, and pulling one into memory on a status path is
// exactly the cost the chunked walk exists to avoid — so a line longer than this is
// skipped on its measured length, without being read at all.
const usageLimitMaxLineBytes = 1 << 20 // 1 MiB

// usageLimitScanRead describes one read the scan performed. kind is "chunk" for a
// newline-scanning window and "line" for a candidate record read.
//
// Recording BOTH matters: chunk ranges alone cannot distinguish this walker from
// the carry-based one it replaced, whose chunk windows were also descending,
// contiguous and single-coverage — its defect was repeated copying of a growing
// line, which chunk instrumentation cannot see. Line reads are where that shows up.
type usageLimitScanRead struct {
	Kind       string
	Start, End int64
	// Bytes is how many bytes this event actually moved into memory: End-Start for
	// "chunk" and "line", and 0 for "line-skipped" since the whole point of the skip
	// is that nothing was read.
	//
	// Reporting WORK rather than only ranges is what lets a test see a walker that
	// re-copies the same line once per chunk. Ranges cannot: a copy-heavy
	// implementation can produce byte-identical chunk ranges, and one that never
	// reads candidate lines at all emits no line events, so a bound on line bytes
	// is vacuously satisfied. A test must therefore assert the premise that line
	// work was observed at all.
	Bytes int64
}

// usageLimitScanObserver is a TEST SEAM: when non-nil it receives every read the
// scan performs. It is what lets a test assert non-overlap, bounded I/O and the
// oversized-line skip rather than merely assert that the answer came out right.
// Always nil in production.
var usageLimitScanObserver func(usageLimitScanRead)

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

	if info.Size() == 0 {
		// Nothing to read, and nothing to allocate: an empty transcript used to cost
		// a full chunk buffer on every eligible scan.
		return false, false
	}

	chunk := usageLimitScanChunkBytes
	if chunk <= 0 {
		chunk = defaultUsageLimitScanChunkBytes
	}
	// Size the buffer to the file when it is smaller than a chunk. A just-created
	// 200-byte transcript should not allocate 512 KiB per scan.
	bufSize := chunk
	if info.Size() < bufSize {
		bufSize = info.Size()
	}
	buf := make([]byte, bufSize)

	// lineEnd is the exclusive end of the line currently being delimited. Walking
	// backwards, a '\n' at offset i closes the line [i+1, lineEnd) and opens the
	// next one ending at i.
	//
	// Each line is then read exactly once, by its own bounded ReadAt. An earlier
	// revision instead prepended a growing "carry" to every chunk, which recopied
	// a multi-chunk line once per chunk — quadratic in line length, and this repo
	// documents /compact producing multi-megabyte single-line records.
	lineEnd := info.Size()

	consider := func(start, end int64) (bool, bool, bool) {
		if end <= start {
			return false, false, false
		}
		if end-start > usageLimitMaxLineBytes {
			if usageLimitScanObserver != nil {
				usageLimitScanObserver(usageLimitScanRead{Kind: "line-skipped", Start: start, End: end, Bytes: 0})
			}
			// A line this large is a compaction/file-history snapshot, not a turn
			// this detector can act on. Skipped WITHOUT reading it: the whole point
			// of the bound is not to pull megabytes onto a status path.
			return false, false, false
		}
		if usageLimitScanObserver != nil {
			usageLimitScanObserver(usageLimitScanRead{Kind: "line", Start: start, End: end, Bytes: end - start})
		}
		line := make([]byte, end-start)
		if _, err := f.ReadAt(line, start); err != nil && err != io.EOF {
			return false, false, false
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			return false, false, false
		}
		var rec transcriptRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			return false, false, false
		}
		if !rec.isAssistantTurn() {
			return false, false, false
		}
		if !rec.isRateLimitRejection() {
			return false, true, true
		}
		// A rejection older than the window is no longer evidence about now.
		return rec.fresh(now, usageLimitMaxAge), true, true
	}

	for end := info.Size(); end > 0; {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		n := end - start
		if usageLimitScanObserver != nil {
			usageLimitScanObserver(usageLimitScanRead{Kind: "chunk", Start: start, End: end, Bytes: end - start})
		}
		if _, err := f.ReadAt(buf[:n], start); err != nil && err != io.EOF {
			return false, false
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			if lim, o, decided := consider(start+i+1, lineEnd); decided {
				return lim, o
			}
			lineEnd = start + i
		}
		end = start
	}
	// The first line of the file has no preceding newline to close it.
	if lim, o, decided := consider(0, lineEnd); decided {
		return lim, o
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
func usageLimitPublishable(currentGen, scanGen uint64, liveSessionID, scanSessionID string) bool {
	return currentGen == scanGen && liveSessionID == scanSessionID
}

// usageLimitVerdictFor reads the memo under the lock, and only when all three
// identities agree: the LIVE ClaudeSessionID, the memo key, and the session this
// scan was gated on.
//
// ONE invariant, stated once, because getting it partially right produced the same
// class of bug three revisions running:
//
//	live == memoKey == scanID  →  the memo describes this caller's session
//
// Any pair alone is insufficient, and both failures were found in review rather
// than by me:
//
//   - scanID vs memoKey passes during an A→B rebind — both still read "A", since
//     the key only moves when a mismatch is observed at claim time — and hands A's
//     verdict to B.
//   - scanID vs live passes in an A→B→A sequence while the memo still holds B's
//     verdict, because transcript I/O runs outside i.mu and B can claim and publish
//     in between.
//
// A mismatch on any leg means "no verdict for this caller", never "not limited".
func (i *Instance) usageLimitVerdictFor(scanSessionID string) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.usageLimitVerdictForLocked(scanSessionID)
}

// usageLimitVerdictForLocked is usageLimitVerdictFor for callers already holding
// i.mu, so the publish path can apply the identical invariant instead of an
// open-coded approximation of it.
func (i *Instance) usageLimitVerdictForLocked(scanSessionID string) bool {
	if strings.TrimSpace(i.ClaudeSessionID) != scanSessionID {
		return false
	}
	if i.usageLimitSessionID != scanSessionID {
		return false
	}
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
		return i.usageLimitVerdictFor(sessionID)
	}
	// Verify the answer belongs to the id we gated on. Holding the lock across the
	// check is not enough on its own: the resolver re-reads ClaudeSessionID itself
	// (migrate_locate.go) after the lock is released, so a concurrent rebind could
	// still steer it onto the newest-conversation fallback. Checking the resolved
	// path makes the guarantee structural instead of dependent on timing.
	if !transcriptBelongsToSession(path, sessionID) {
		return i.usageLimitVerdictFor(sessionID)
	}

	limited, ok := latestAssistantTurnIsRateLimited(path, time.Now())
	if !ok {
		// No formed verdict (unreadable, or no assistant turn in the file): leave
		// the previous answer standing rather than silently reporting "not
		// limited".
		return i.usageLimitVerdictFor(sessionID)
	}

	i.mu.Lock()
	// Order matters here, and getting it wrong once already produced this exact bug
	// twice: check the LIVE ClaudeSessionID against the scan's id FIRST.
	//
	// usageLimitSessionID is the memo KEY and only moves when a mismatch is observed
	// at claim time, so during an in-flight A→B rebind it still reads "A". Comparing
	// against it — anywhere, on the write path or the read path — hands A's verdict
	// to B. The live field is the only thing that knows about the rebind.
	live := strings.TrimSpace(i.ClaudeSessionID)
	switch {
	case live != sessionID:
		// Rebound while this read was in flight. There is no verdict for the new
		// session, and inheriting one is the bug.
		limited = false
	case usageLimitPublishable(i.usageLimitScanGen, gen, live, sessionID):
		i.usageLimitedCached = limited
	default:
		// A newer claim for the same live session landed while this read was in
		// flight, so this result is stale. Report the memo through the SAME
		// three-way invariant: reading usageLimitedCached directly here was
		// identity-blind under A→B→A, where live and scan both read "A" while the
		// memo holds B's verdict.
		limited = i.usageLimitVerdictForLocked(sessionID)
	}
	i.mu.Unlock()

	return limited
}
