package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #3012 step 1: the hook file keeps a monotonic turn_seq per instance,
// bumped on UserPromptSubmit and Stop under the status lock, carried through
// every other write and independent of [health].

func readTurnSeq(t *testing.T, id string) uint64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var raw struct {
		TurnSeq *uint64 `json:"turn_seq"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal hook file: %v", err)
	}
	if raw.TurnSeq == nil {
		return 0
	}
	return *raw.TurnSeq
}

type turnSeqStep struct {
	status, event string
	want          uint64
}

// A turn, then the edges a /clear, a compaction or a restart write: none of
// them may lose the count, and only a prompt or a Stop moves it.
var turnSeqScript = []turnSeqStep{
	{"waiting", "SessionStart", 0},
	{"running", "UserPromptSubmit", 1},
	{"running", "PreToolUse", 1},
	{"waiting", "PostToolUse", 1},
	{"waiting", "Stop", 2},
	{"waiting", "SessionStart", 2}, // /clear
	{"waiting", "PermissionRequest", 2},
	{"dead", "SessionEnd", 2},
	{"waiting", "SessionStart", 2}, // a new process
	{"running", "UserPromptSubmit", 3},
	{"waiting", "Stop", 4},
}

func runTurnSeqScript(t *testing.T, id string) {
	t.Helper()
	for i, s := range turnSeqScript {
		writeHookStatusReceivedAt(id, s.status, "sess-3012", s.event, "", doneScanResult{}, time.Now())
		if got := readTurnSeq(t, id); got != s.want {
			t.Fatalf("step %d (%s): turn_seq = %d, want %d", i, s.event, got, s.want)
		}
	}
}

func TestIssue3012_TurnSeqCountsPromptsAndStops(t *testing.T) {
	hookEventsHome(t)
	runTurnSeqScript(t, "inst-3012")
}

// The event history goes away with [health] enabled = false; turn_seq must not.
func TestIssue3012_TurnSeqWithHealthDisabled(t *testing.T) {
	hookEventsHome(t)
	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[health]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	if hookEventHistoryEnabled() {
		t.Fatalf("precondition: history must be off")
	}
	runTurnSeqScript(t, "inst-3012-off")
}

// A hook file written before turn_seq existed reads as 0 and counts on from there.
func TestIssue3012_LegacyHookFileStartsAtZero(t *testing.T) {
	hookEventsHome(t)
	const id = "inst-3012-legacy"
	if err := os.MkdirAll(getHooksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"status":"waiting","session_id":"sess-old","event":"Stop","ts":1700000000}`
	if err := os.WriteFile(filepath.Join(getHooksDir(), id+".json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readTurnSeq(t, id); got != 0 {
		t.Fatalf("legacy file turn_seq = %d, want 0", got)
	}
	writeHookStatus(id, "waiting", "sess-old", "SessionStart", "")
	if got := readTurnSeq(t, id); got != 0 {
		t.Fatalf("after SessionStart turn_seq = %d, want 0", got)
	}
	writeHookStatus(id, "running", "sess-old", "UserPromptSubmit", "")
	if got := readTurnSeq(t, id); got != 1 {
		t.Fatalf("after UserPromptSubmit turn_seq = %d, want 1", got)
	}
}

// Overlapping hook processes (a Stop racing a prompt, subagents) must never
// lose an increment: the read and the write share the status lock, whether or
// not the write carries a receive time.
func TestIssue3012_TurnSeqUnderConcurrentWriters(t *testing.T) {
	hookEventsHome(t)
	const id = "inst-3012-race"
	const writers, each = 8, 10
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				event := "Stop"
				if (w+i)%2 == 0 {
					event = "UserPromptSubmit"
				}
				if w%2 == 0 {
					writeHookStatusReceivedAt(id, "waiting", "sess-race", event, "", doneScanResult{}, time.Now())
				} else {
					writeHookStatus(id, "waiting", "sess-race", event, "")
				}
				// Non-turn edges in between must not clobber a newer count.
				writeHookStatus(id, "waiting", "sess-race", "Notification", "")
			}
		}(w)
	}
	wg.Wait()
	if got := readTurnSeq(t, id); got != writers*each {
		t.Fatalf("turn_seq = %d after %d concurrent turn edges, want %d", got, writers*each, writers*each)
	}
}

// The Codex notify writer rebuilds the file from its own read; it must carry
// turn_seq forward too.
func TestIssue3012_CodexNotifyCarriesTurnSeq(t *testing.T) {
	hookEventsHome(t)
	const id = "inst-3012-codex"
	writeHookStatus(id, "running", "sess-c", "UserPromptSubmit", "")
	writeHookStatus(id, "waiting", "sess-c", "Stop", "")
	writeCodexHookStatus(id, "waiting", "sess-c", "agent-turn-complete", "turn-1")
	if got := readTurnSeq(t, id); got != 2 {
		t.Fatalf("turn_seq after a codex notify write = %d, want 2", got)
	}
}
