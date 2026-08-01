package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// refNow is the fixed clock these tests reason against, so freshness assertions
// do not depend on wall time.
var refNow = time.Date(2026, 7, 30, 21, 0, 0, 0, time.UTC)

func stampAt(offset time.Duration) string {
	return refNow.Add(offset).UTC().Format(time.RFC3339Nano)
}

// Records shaped exactly like the field evidence that motivated #1802: the quota
// rejection carries apiErrorStatus 429 AND error "rate_limit" together.
func recRateLimitAt(ts string) string {
	return fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"error":"rate_limit","timestamp":%q,"isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"You've hit your session limit · resets 8:50pm (UTC)"}]}}`, ts)
}

func recAssistantAt(ts, text string) string {
	return fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":false,"timestamp":%q,"isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, ts, text)
}

func recUserAt(ts string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"isSidechain":false,"message":{"role":"user","content":"[HEARTBEAT] Check sessions in your group"}}`, ts)
}

func recAPIErrorAt(ts string, status int, kind string) string {
	return fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":%d,"error":%q,"timestamp":%q,"isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"api error"}]}}`, status, kind, ts)
}

func recRateLimitNoStatusAt(ts string) string {
	return fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","timestamp":%q,"isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"rate limited"}]}}`, ts)
}

func recRateLimitNoTimestamp() string {
	return `{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"error":"rate_limit","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"You've hit your session limit"}]}}`
}

func recSidechainRateLimitAt(ts string) string {
	return fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"error":"rate_limit","timestamp":%q,"isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"You've hit your session limit"}]}}`, ts)
}

func writeUsageLimitTranscript(t *testing.T, records ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestLatestAssistantTurnIsRateLimited(t *testing.T) {
	recent := stampAt(-time.Minute)

	tests := []struct {
		name        string
		records     []string
		wantLimited bool
		wantOK      bool
	}{
		{
			name:        "recent quota rejection is the latest assistant turn",
			records:     []string{recUserAt(recent), recRateLimitAt(recent)},
			wantLimited: true,
			wantOK:      true,
		},
		{
			name:        "a later successful turn clears it with no expiry logic",
			records:     []string{recRateLimitAt(recent), recUserAt(recent), recAssistantAt(recent, "All clear.")},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "later user message alone does not clear it",
			records:     []string{recRateLimitAt(recent), recUserAt(recent)},
			wantLimited: true,
			wantOK:      true,
		},
		{
			// #1806 review: the verdict must not outlive the window it describes,
			// even when no further turn is ever submitted.
			name:        "a rejection older than the window is no longer believed",
			records:     []string{recUserAt(stampAt(-6 * time.Hour)), recRateLimitAt(stampAt(-6 * time.Hour))},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "a rejection with no timestamp is not treated as fresh",
			records:     []string{recUserAt(recent), recRateLimitNoTimestamp()},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "credential failure is a different condition",
			records:     []string{recUserAt(recent), recAPIErrorAt(recent, 401, "authentication_error")},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "overloaded model is not a quota rejection",
			records:     []string{recUserAt(recent), recAPIErrorAt(recent, 529, "overloaded_error")},
			wantLimited: false,
			wantOK:      true,
		},
		{
			// #1806 review: 429 and rate_limit are required together, so neither
			// alone nor a mismatched pair may read as plan exhaustion.
			name:        "429 with a different error kind is not plan exhaustion",
			records:     []string{recUserAt(recent), recAPIErrorAt(recent, 429, "overloaded_error")},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "rate_limit kind without the 429 status does not match",
			records:     []string{recUserAt(recent), recRateLimitNoStatusAt(recent)},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "subagent sidechain limit does not classify the parent",
			records:     []string{recRateLimitAt(recent), recAssistantAt(recent, "done"), recSidechainRateLimitAt(recent)},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "no assistant turn yet reports no verdict",
			records:     []string{recUserAt(recent)},
			wantLimited: false,
			wantOK:      false,
		},
		{
			// The scan walks backwards, so a malformed line must sit AFTER the
			// rejection to be visited at all — otherwise the case never exercises
			// the skip it claims to.
			name:        "a malformed line newer than the verdict is skipped, not fatal",
			records:     []string{recUserAt(recent), recRateLimitAt(recent), `{not json`},
			wantLimited: true,
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limited, ok := latestAssistantTurnIsRateLimited(writeUsageLimitTranscript(t, tt.records...), refNow)
			if limited != tt.wantLimited || ok != tt.wantOK {
				t.Fatalf("latestAssistantTurnIsRateLimited = (%v, %v), want (%v, %v)",
					limited, ok, tt.wantLimited, tt.wantOK)
			}
		})
	}
}

func TestLatestAssistantTurnIsRateLimited_MissingFile(t *testing.T) {
	limited, ok := latestAssistantTurnIsRateLimited(filepath.Join(t.TempDir(), "absent.jsonl"), refNow)
	if limited || ok {
		t.Fatalf("missing transcript = (%v, %v), want (false, false)", limited, ok)
	}
}

// #1806 review: a rejection pushed beyond the first tail window by later
// non-assistant traffic must still be found. A long-lived process keeps its memo,
// but a fresh CLI invocation starts with none and would otherwise report a
// limited session as healthy.
// #1806 cycle-5 audit: the previous version proved only fixture depth — it asserted
// the answer came out right, which an overlapping or whole-file reader also
// achieves. What the change actually claims is *non-overlapping, bounded* I/O that
// still reaches byte 0, so that is what this asserts, using the scan's observer
// seam to record every byte range read.
func TestLatestAssistantTurnIsRateLimited_ReadsBoundedNonOverlappingChunks(t *testing.T) {
	savedChunk := usageLimitScanChunkBytes
	usageLimitScanChunkBytes = 1024
	defer func() { usageLimitScanChunkBytes = savedChunk }()

	path := filepath.Join(t.TempDir(), "deep.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString(recRateLimitAt(stampAt(-time.Minute)) + "\n"); err != nil {
		t.Fatalf("write rejection: %v", err)
	}
	filler := recUserAt(stampAt(-time.Minute))
	var buried int64
	for buried < 200*1024 {
		n, err := f.WriteString(filler + "\n")
		if err != nil {
			t.Fatalf("write filler: %v", err)
		}
		buried += int64(n)
	}
	_ = f.Close()

	size, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	type rng struct{ start, end int64 }
	var reads []rng
	usageLimitScanObserver = func(start, end int64) { reads = append(reads, rng{start, end}) }
	defer func() { usageLimitScanObserver = nil }()

	limited, ok := latestAssistantTurnIsRateLimited(path, refNow)
	if !limited || !ok {
		t.Fatalf("deep walk = (%v, %v), want (true, true)", limited, ok)
	}

	if len(reads) < 100 {
		t.Fatalf("only %d chunk reads for a %d-byte file at %d-byte chunks; the walk is not chunking",
			len(reads), size.Size(), usageLimitScanChunkBytes)
	}

	// Descending, contiguous, non-overlapping, and terminating at byte 0.
	var total int64
	prevStart := size.Size()
	for n, r := range reads {
		if r.end != prevStart {
			t.Fatalf("read %d is [%d,%d) but the previous read started at %d — chunks must be contiguous and non-overlapping",
				n, r.start, r.end, prevStart)
		}
		if r.end-r.start > usageLimitScanChunkBytes {
			t.Fatalf("read %d spans %d bytes, over the %d-byte chunk bound", n, r.end-r.start, usageLimitScanChunkBytes)
		}
		total += r.end - r.start
		prevStart = r.start
	}
	if prevStart != 0 {
		t.Fatalf("walk stopped at byte %d instead of reaching 0 — a ceiling would look exactly like this", prevStart)
	}
	// Chunk scanning reads the file once. Line reads add the lines actually parsed,
	// which for this fixture is a small tail, so a generous multiple still catches
	// the old geometric re-reading (which cost >1.5x on this shape).
	if total != size.Size() {
		t.Fatalf("chunk reads covered %d bytes for a %d-byte file; expected exact single coverage", total, size.Size())
	}
}

// A record straddling a chunk boundary must be reassembled, not silently dropped
// or half-parsed. Chosen chunk size guarantees the rejection spans a boundary.
func TestLatestAssistantTurnIsRateLimited_RecordSpanningChunkBoundary(t *testing.T) {
	rec := recRateLimitAt(stampAt(-time.Minute))
	saved := usageLimitScanChunkBytes
	// Half the record length: the rejection cannot fit in one chunk.
	usageLimitScanChunkBytes = int64(len(rec) / 2)
	defer func() { usageLimitScanChunkBytes = saved }()

	path := filepath.Join(t.TempDir(), "split.jsonl")
	body := recUserAt(stampAt(-time.Minute)) + "\n" + rec + "\n" + recUserAt(stampAt(-time.Minute)) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	limited, ok := latestAssistantTurnIsRateLimited(path, refNow)
	if !limited || !ok {
		t.Fatalf("boundary-spanning record = (%v, %v), want (true, true)", limited, ok)
	}
}

// The pure publish rule, exhaustively — racing two real scans to exercise it is
// not something a test can do deterministically, which is why the rule is split
// out. #1806 cycle-4 audit noted usageLimitScanGen had no regression at all.
func TestUsageLimitPublishable(t *testing.T) {
	tests := []struct {
		name                  string
		curGen, myGen         uint64
		curSession, mySession string
		want                  bool
	}{
		{"current claim, same session", 7, 7, "s1", "s1", true},
		{"superseded by a newer claim", 8, 7, "s1", "s1", false},
		{"rebound while in flight", 7, 7, "s2", "s1", false},
		{"both moved", 9, 7, "s2", "s1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageLimitPublishable(tt.curGen, tt.myGen, tt.curSession, tt.mySession); got != tt.want {
				t.Fatalf("usageLimitPublishable = %v, want %v", got, tt.want)
			}
		})
	}
}

// #1806 cycle-5 audit: the previous version seeded the newer verdict BEFORE the
// claim, so the old snapshot code returned true as well — a false green.
//
// My first replacement was ALSO blind: it called latestAssistantTurnIsRateLimited
// and then usageLimitedNow() directly, which proves only that usageLimitedNow reads
// current state and says nothing about whether usageLimited's exits route through
// it. This drives usageLimited() itself against a transcript the resolver really
// finds, and moves the memo mid-scan through the observer seam so the ordering is
// deterministic rather than raced.
func TestUsageLimited_EarlyExitReportsMemoPublishedDuringScan(t *testing.T) {
	const sid = "exit-order-session"
	project := t.TempDir()
	inst := NewInstanceWithTool("usage-limit-exit-order", project, "claude")
	inst.ClaudeSessionID = sid

	// Place the fixture exactly where the resolver looks.
	dir := filepath.Join(GetClaudeConfigDirForInstance(inst), "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	// A user record only: the scan reads it, finds no assistant turn, and takes the
	// post-claim "no verdict" exit — the exit under test.
	if err := os.WriteFile(path, []byte(recUserAt(stampAt(-time.Minute))+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := locateHandoffTranscript(inst); got != path {
		t.Skipf("resolver did not find the fixture (got %q, want %q); environment-dependent", got, path)
	}

	scanned := false
	usageLimitScanObserver = func(int64, int64) {
		scanned = true
		// Publish a newer verdict while this scan is in flight, exactly as a
		// concurrent scan would. Old code captured its snapshot before this point
		// and returned false.
		inst.mu.Lock()
		inst.usageLimitedCached = true
		inst.mu.Unlock()
	}
	defer func() { usageLimitScanObserver = nil }()

	got := inst.usageLimited()
	if !scanned {
		t.Fatal("premise broken: no chunk was read, so nothing was published mid-scan")
	}
	if !got {
		t.Fatal("early exit returned the snapshot taken at claim time, not the memo published during the scan")
	}
}

func TestUsageLimited_NonClaudeToolNeverScans(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-codex", t.TempDir(), "codex")
	inst.ClaudeSessionID = "abc123"
	if inst.usageLimited() {
		t.Fatal("usageLimited() = true for a codex session, want false")
	}
	if !inst.lastUsageLimitScanAt.IsZero() {
		t.Fatal("non-Claude tool should not even record a scan attempt")
	}
}

func TestUsageLimited_SkipsSSHInstances(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-ssh", t.TempDir(), "claude")
	inst.SSHHost = "devbox.example"
	inst.ClaudeSessionID = "abc123"

	if inst.usageLimited() {
		t.Fatal("usageLimited() = true for an SSH instance, want false")
	}
	if !inst.lastUsageLimitScanAt.IsZero() {
		t.Fatal("SSH instance should bail before any path work or scan stamp")
	}
}

// #1806 review: with an empty ClaudeSessionID the shared resolver deliberately
// falls back to the NEWEST conversation for the project, so an unbound instance
// would inherit a sibling session's rejection. It must not even look.
func TestUsageLimited_RequiresBoundSessionID(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-unbound", t.TempDir(), "claude")
	inst.ClaudeSessionID = ""

	if inst.usageLimited() {
		t.Fatal("usageLimited() = true for an unbound instance, want false")
	}
	if !inst.lastUsageLimitScanAt.IsZero() {
		t.Fatal("an unbound instance should bail before resolving any transcript")
	}
}

// The throttle must cover the "path never resolves" exit too: stamping only on
// success left that case resolving a transcript on every status poll.
func TestUsageLimited_ThrottlesWhenTranscriptUnresolvable(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-unresolvable", t.TempDir(), "claude")
	inst.ClaudeSessionID = "no-such-session-id"

	if inst.usageLimited() {
		t.Fatal("usageLimited() = true with no transcript, want false")
	}

	inst.mu.RLock()
	stamped := inst.lastUsageLimitScanAt
	inst.mu.RUnlock()
	if stamped.IsZero() {
		t.Fatal("scan attempt was not stamped, so the throttle never engages on this path")
	}

	inst.usageLimited()
	inst.mu.RLock()
	again := inst.lastUsageLimitScanAt
	inst.mu.RUnlock()
	if !again.Equal(stamped) {
		t.Fatalf("second call re-stamped (%v -> %v); throttle did not short-circuit", stamped, again)
	}
}

// #1806 review: the throttle check and stamp now share one critical section, so
// concurrent callers cannot both claim the window. Run under -race for the
// memory-safety half; this pins the observable contract — one stamp, one agreed
// answer.
func TestUsageLimited_ConcurrentCallersAgree(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-concurrent", t.TempDir(), "claude")
	inst.ClaudeSessionID = "no-such-session-id"

	const callers = 16
	var wg sync.WaitGroup
	results := make([]bool, callers)
	wg.Add(callers)
	for n := 0; n < callers; n++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = inst.usageLimited()
		}(n)
	}
	wg.Wait()

	for idx, got := range results {
		if got != results[0] {
			t.Fatalf("caller %d disagreed: %v vs %v", idx, got, results[0])
		}
	}
	inst.mu.RLock()
	stamped, gen := inst.lastUsageLimitScanAt, inst.usageLimitScanGen
	inst.mu.RUnlock()
	if stamped.IsZero() {
		t.Fatal("no scan was stamped despite concurrent callers")
	}
	// The generation IS the claim counter, so this is the assertion the earlier
	// version was missing: equal results and a non-zero timestamp left multiple
	// simultaneous claims completely invisible.
	if gen != 1 {
		t.Fatalf("usageLimitScanGen = %d after %d concurrent callers, want exactly 1 claim", gen, callers)
	}
}

// Pins the Substate wiring itself, deterministically and without touching the
// filesystem: seeding the throttle window makes usageLimited short-circuit on the
// memo, so this asserts the precedence branch rather than the detector.
//
// Without this, removing the branch from Substate would break no test — the rest
// of this file exercises usageLimited and the transcript walk, not the wiring.
func TestSubstate_ReportsUsageLimitWhenLimited(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-substate", t.TempDir(), "claude")
	inst.ClaudeSessionID = "bound-session-id"

	inst.mu.Lock()
	inst.lastUsageLimitScanAt = time.Now()
	inst.usageLimitedCached = true
	// The memo is keyed by the id it was formed for, so seed that too — without
	// it the identity check correctly discards the verdict as belonging to some
	// other session. (This test caught exactly that when the keying landed.)
	inst.usageLimitSessionID = inst.ClaudeSessionID
	inst.mu.Unlock()

	if got := inst.Substate(); got != SubstateUsageLimit {
		t.Fatalf("Substate() = %q with a live usage-limit verdict, want %q", got, SubstateUsageLimit)
	}

	inst.mu.Lock()
	inst.usageLimitedCached = false
	inst.mu.Unlock()

	if got := inst.Substate(); got == SubstateUsageLimit {
		t.Fatalf("Substate() = %q with no verdict, want anything else", got)
	}
}

// #1806 review (CodeRabbit): the empty-id gate alone cannot close the window,
// because locateHandoffTranscript re-reads ClaudeSessionID after the lock is
// released and falls back to the newest conversation for the project when it is
// empty. So the guarantee is made about the resolved PATH instead of the timing.
func TestTranscriptBelongsToSession(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		sessionID string
		want      bool
	}{
		{
			name:      "path for our own session",
			path:      "/home/u/.claude/projects/-home-u-proj/abc123.jsonl",
			sessionID: "abc123",
			want:      true,
		},
		{
			name:      "path for a sibling session is refused",
			path:      "/home/u/.claude/projects/-home-u-proj/other999.jsonl",
			sessionID: "abc123",
			want:      false,
		},
		{
			name:      "empty path",
			path:      "",
			sessionID: "abc123",
			want:      false,
		},
		{
			name:      "empty session id never matches",
			path:      "/home/u/.claude/projects/-home-u-proj/abc123.jsonl",
			sessionID: "",
			want:      false,
		},
		{
			name:      "prefix collision is not a match",
			path:      "/home/u/.claude/projects/-home-u-proj/abc1234.jsonl",
			sessionID: "abc123",
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transcriptBelongsToSession(tt.path, tt.sessionID); got != tt.want {
				t.Fatalf("transcriptBelongsToSession(%q, %q) = %v, want %v", tt.path, tt.sessionID, got, tt.want)
			}
		})
	}
}

// #1806 cycle-3 review: the memo had no session identity, so an Instance rebound
// from session A to session B returned A's verdict for B — indefinitely when B
// never forms one of its own. The memo is now keyed by the id it was formed for.
func TestUsageLimited_RebindDoesNotInheritVerdict(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-rebind", t.TempDir(), "claude")
	inst.ClaudeSessionID = "session-A"

	// Seed a live verdict for A, exactly as a completed scan would.
	inst.mu.Lock()
	inst.usageLimitSessionID = "session-A"
	inst.usageLimitedCached = true
	inst.lastUsageLimitScanAt = time.Now()
	inst.mu.Unlock()

	if !inst.usageLimited() {
		t.Fatal("premise broken: A's own verdict should be returned for A")
	}

	// Normal rebind onto the same Instance.
	inst.ClaudeSessionID = "session-B"

	if inst.usageLimited() {
		t.Fatal("B inherited A's usage-limit verdict")
	}

	inst.mu.RLock()
	gotID, gotCached := inst.usageLimitSessionID, inst.usageLimitedCached
	inst.mu.RUnlock()
	if gotID != "session-B" {
		t.Fatalf("memo identity = %q after rebind, want %q", gotID, "session-B")
	}
	if gotCached {
		t.Fatal("stale verdict survived the rebind")
	}
}

// The rebind must also drop the throttle claim, otherwise B would be answered
// from A's window instead of being scanned.
func TestUsageLimited_RebindClearsThrottleClaim(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-rebind-throttle", t.TempDir(), "claude")
	inst.ClaudeSessionID = "session-A"

	inst.mu.Lock()
	inst.usageLimitSessionID = "session-A"
	inst.usageLimitedCached = true
	claimed := time.Now()
	inst.lastUsageLimitScanAt = claimed
	inst.mu.Unlock()

	inst.ClaudeSessionID = "session-B"
	_ = inst.usageLimited()

	inst.mu.RLock()
	stamped := inst.lastUsageLimitScanAt
	inst.mu.RUnlock()
	if stamped.Equal(claimed) {
		t.Fatal("rebind kept A's throttle claim, so B is answered from A's window")
	}
}
