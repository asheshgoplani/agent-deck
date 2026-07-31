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

// withTailSteps runs fn with a pinned escalation ladder, so a test can assert
// what a single window can and cannot decide.
func withTailSteps(steps []int64, fn func()) {
	saved := usageLimitTailSteps
	usageLimitTailSteps = steps
	defer func() { usageLimitTailSteps = saved }()
	fn()
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
func TestLatestAssistantTurnIsRateLimited_RecoversAfterTailEviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evicted.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString(recRateLimitAt(stampAt(-time.Minute)) + "\n"); err != nil {
		t.Fatalf("write rejection: %v", err)
	}
	// Bury it under more than one tail step of user records. No assistant turn
	// among them, so the first step can form no verdict.
	filler := recUserAt(stampAt(-time.Minute))
	for written := int64(0); written < usageLimitTailSteps[0]+64*1024; written += int64(len(filler)) + 1 {
		if _, err := f.WriteString(filler + "\n"); err != nil {
			t.Fatalf("write filler: %v", err)
		}
	}
	_ = f.Close()

	// Premise: one window alone must be unable to answer.
	withTailSteps(usageLimitTailSteps[:1], func() {
		if _, ok := latestAssistantTurnIsRateLimited(path, refNow); ok {
			t.Fatal("premise broken: a single tail step formed a verdict, so eviction is not exercised")
		}
	})

	limited, ok := latestAssistantTurnIsRateLimited(path, refNow)
	if !limited || !ok {
		t.Fatalf("after eviction = (%v, %v), want (true, true) via tail escalation", limited, ok)
	}
}

// A tail offset landing inside a record must drop that partial line and keep the
// complete ones. Sized from the fixture rather than hardcoded, so the offset
// really lands inside the filler line.
func TestReadTranscriptTailLines_DropsPartialFirstLine(t *testing.T) {
	rec := recRateLimitAt(stampAt(-time.Minute))
	filler := strings.Repeat("A", 400)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(filler+"\n"+rec+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Covers the whole record plus part of the filler line above it.
	lines, complete, err := readTranscriptTailLines(path, int64(len(rec)+100))
	if err != nil {
		t.Fatalf("readTranscriptTailLines: %v", err)
	}
	if complete {
		t.Fatal("premise broken: the read covered the whole file, so no partial line exists")
	}
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want exactly the one complete record: %#v", len(lines), lines)
	}
	if lines[0] != rec {
		t.Fatalf("kept line is not the intact record:\n got %q\nwant %q", lines[0], rec)
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
	stamped := inst.lastUsageLimitScanAt
	inst.mu.RUnlock()
	if stamped.IsZero() {
		t.Fatal("no scan was stamped despite concurrent callers")
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
