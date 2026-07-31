package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Records shaped exactly like the field evidence that motivated #1802. The
// quota rejection carries apiErrorStatus 429 and error "rate_limit"; a
// credential failure carries 401; a normal turn carries neither.
const (
	recRateLimit = `{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"error":"rate_limit","timestamp":"2026-07-30T17:03:25.225Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"You've hit your session limit · resets 8:50pm (UTC)"}]}}`
	recAuth401   = `{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":401,"error":"authentication_error","timestamp":"2026-07-30T17:03:25.225Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"Invalid API key · Please run /login"}]}}`
	recOverload  = `{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":529,"error":"overloaded_error","timestamp":"2026-07-30T17:03:25.225Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"API Error: 529 overloaded"}]}}`
	recNormal    = `{"type":"assistant","isApiErrorMessage":false,"timestamp":"2026-07-30T21:06:27.000Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"All clear."}]}}`
	recUser      = `{"type":"user","timestamp":"2026-07-30T21:05:44.669Z","isSidechain":false,"message":{"role":"user","content":"[HEARTBEAT] Check sessions in your group"}}`
	// A subagent hitting its own limit must not classify the parent session.
	recSidechainRateLimit = `{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"error":"rate_limit","timestamp":"2026-07-30T17:03:25.225Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"You've hit your session limit · resets 8:50pm (UTC)"}]}}`
)

func writeUsageLimitTranscript(t *testing.T, records ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := strings.Join(records, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestLatestAssistantTurnIsRateLimited(t *testing.T) {
	tests := []struct {
		name        string
		records     []string
		wantLimited bool
		wantOK      bool
	}{
		{
			name:        "quota rejection is the latest assistant turn",
			records:     []string{recUser, recRateLimit},
			wantLimited: true,
			wantOK:      true,
		},
		{
			name: "a later successful turn clears it without any expiry logic",
			// This is the whole reason the verdict is "latest turn" rather than a
			// parsed reset time: recovery needs no clock.
			records:     []string{recRateLimit, recUser, recNormal},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "later user message alone does not clear it",
			records:     []string{recRateLimit, recUser},
			wantLimited: true,
			wantOK:      true,
		},
		{
			name:        "credential failure is a different condition",
			records:     []string{recUser, recAuth401},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "overloaded model is not a quota rejection",
			records:     []string{recUser, recOverload},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "subagent sidechain limit does not classify the parent",
			records:     []string{recRateLimit, recNormal, recSidechainRateLimit},
			wantLimited: false,
			wantOK:      true,
		},
		{
			name:        "no assistant turn yet reports no verdict",
			records:     []string{recUser},
			wantLimited: false,
			wantOK:      false,
		},
		{
			name:        "malformed lines are skipped, not fatal",
			records:     []string{`{not json`, recUser, recRateLimit},
			wantLimited: true,
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limited, ok := latestAssistantTurnIsRateLimited(writeUsageLimitTranscript(t, tt.records...))
			if limited != tt.wantLimited || ok != tt.wantOK {
				t.Fatalf("latestAssistantTurnIsRateLimited = (%v, %v), want (%v, %v)",
					limited, ok, tt.wantLimited, tt.wantOK)
			}
		})
	}
}

func TestLatestAssistantTurnIsRateLimited_MissingFile(t *testing.T) {
	limited, ok := latestAssistantTurnIsRateLimited(filepath.Join(t.TempDir(), "absent.jsonl"))
	if limited || ok {
		t.Fatalf("missing transcript = (%v, %v), want (false, false)", limited, ok)
	}
}

// The tail read must find the newest turn in a transcript far larger than the
// window — field transcripts reach tens of megabytes, and reading them whole on
// a status poll is what the tail exists to avoid.
func TestLatestAssistantTurnIsRateLimited_LargeTranscriptTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Comfortably exceed usageLimitTranscriptTailBytes with filler turns.
	filler := fmt.Sprintf(`{"type":"assistant","isApiErrorMessage":false,"isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}`, strings.Repeat("x", 512))
	for written := 0; written < 2*usageLimitTranscriptTailBytes; written += len(filler) + 1 {
		if _, err := f.WriteString(filler + "\n"); err != nil {
			t.Fatalf("write filler: %v", err)
		}
	}
	if _, err := f.WriteString(recRateLimit + "\n"); err != nil {
		t.Fatalf("write tail record: %v", err)
	}
	_ = f.Close()

	limited, ok := latestAssistantTurnIsRateLimited(path)
	if !limited || !ok {
		t.Fatalf("large transcript = (%v, %v), want (true, true)", limited, ok)
	}
}

// A tail that starts mid-record must not be parsed as a whole line.
func TestReadTranscriptTailLines_DropsPartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := strings.Repeat("A", 400) + "\n" + recRateLimit + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Tail smaller than the file forces a mid-file offset.
	lines, err := readTranscriptTailLines(path, 200)
	if err != nil {
		t.Fatalf("readTranscriptTailLines: %v", err)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "AAA") {
			t.Fatalf("partial first line was kept: %q", l[:20])
		}
	}
}

func TestUsageLimited_NonClaudeToolNeverScans(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-codex", t.TempDir(), "codex")
	if inst.usageLimited() {
		t.Fatal("usageLimited() = true for a codex session, want false")
	}
	if !inst.lastUsageLimitScanAt.IsZero() {
		t.Fatal("non-Claude tool should not even record a scan attempt")
	}
}

// Regression for the review findings on #1806.
func TestUsageLimited_SkipsSSHInstances(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-ssh", t.TempDir(), "claude")
	inst.SSHHost = "devbox.example"
	inst.ClaudeSessionID = "abc123"

	if inst.usageLimited() {
		t.Fatal("usageLimited() = true for an SSH instance, want false")
	}
	if !inst.lastUsageLimitScanAt.IsZero() {
		t.Fatal("SSH instance should bail before doing any path work or stamping a scan")
	}
}

// The throttle must cover the "path never resolves" exit too. Stamping only on
// success left that case calling locateHandoffTranscript — user-config load plus
// several stats — on every status poll, which is precisely the cost the throttle
// exists to bound.
func TestUsageLimited_ThrottlesWhenTranscriptUnresolvable(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-unresolvable", t.TempDir(), "claude")
	// No ClaudeSessionID: nothing to resolve a transcript from.
	inst.ClaudeSessionID = ""

	if inst.usageLimited() {
		t.Fatal("usageLimited() = true with no transcript, want false")
	}

	inst.mu.RLock()
	stamped := inst.lastUsageLimitScanAt
	inst.mu.RUnlock()
	if stamped.IsZero() {
		t.Fatal("scan attempt was not stamped, so the throttle never engages on this path")
	}

	// Second call inside the window must not re-stamp (i.e. it short-circuits).
	inst.usageLimited()
	inst.mu.RLock()
	again := inst.lastUsageLimitScanAt
	inst.mu.RUnlock()
	if !again.Equal(stamped) {
		t.Fatalf("second call re-stamped (%v -> %v); throttle did not short-circuit", stamped, again)
	}
}

// The mirror is what CachedSubstate reads, so it must stay consistent with the
// substate it drives and must not require filesystem access.
func TestCachedSubstate_UsesUsageLimitMirror(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-mirror", t.TempDir(), "claude")

	if got := inst.CachedSubstate(); got == SubstateUsageLimit {
		t.Fatalf("CachedSubstate = %q before any scan, want anything else", got)
	}

	inst.mu.Lock()
	inst.usageLimitedCached = true
	inst.mu.Unlock()

	if got := inst.CachedSubstate(); got != SubstateUsageLimit {
		t.Fatalf("CachedSubstate = %q with the mirror set, want %q", got, SubstateUsageLimit)
	}
}
