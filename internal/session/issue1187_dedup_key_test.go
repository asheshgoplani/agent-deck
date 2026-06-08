package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression tests for issue #1187 — agent-deck [EVENT] notifications re-fire
// 10-40x for the same child because the issue-#1142 output-hash dedup key was
// CLOCK-derived, not CONTENT-derived.
//
// Root cause: transitionEventOutputHash returned fmt.Sprintf("act:%d",
// inst.GetLastActivityTime().UnixNano()). GetLastActivityTime resolves to the
// tmux stateTracker.lastChangeTime, which is re-stamped to time.Now() on every
// detected window_activity tick (tmux.go:2956/3102/3303). A live Claude pane
// sitting at the prompt is NOT visually static — it animates the footer, token
// counter, cursor, and hint lines, so window_activity bumps every poll. The
// "stable pane signal" therefore moved on every poll, layer-2 dedup
// (transition_notifier.go isDuplicate) could never match, and the same
// transition re-emitted for hours.
//
// Fix: derive the dedup key from session CONTENT (the transcript size). A
// Claude JSONL transcript is append-only and grows ONLY when a real message is
// written (user prompt, assistant turn, tool call). Animated pane chrome never
// touches the transcript, so the key is identical across idle polls and
// changes only on a genuine new turn.

// setupClaudeTranscript builds a real Claude-style JSONL transcript on disk so
// transitionEventOutputHash exercises its true production path (GetJSONLPath →
// os.Stat) without a live tmux pane. It returns the instance and the transcript
// file path so tests can append to simulate a genuine new turn.
func setupClaudeTranscript(t *testing.T, initialLine string) (*Instance, string) {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	projectPath := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	const sessionID = "sess-1187-aaaa-bbbb-cccc"
	projectDir := claudeProjectDirForTest(t, configDir, projectPath)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir projectDir: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(transcript, []byte(initialLine), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	inst := &Instance{
		ID:              "child-1187",
		Tool:            "claude",
		ClaudeSessionID: sessionID,
		ProjectPath:     projectPath,
		CreatedAt:       time.Unix(1747800000, 0).UTC(),
	}
	// Sanity: the production path must actually resolve the transcript we wrote,
	// otherwise the test would silently exercise the empty-hash fallback.
	if got := inst.GetJSONLPath(); got != transcript {
		t.Fatalf("GetJSONLPath = %q, want %q", got, transcript)
	}
	return inst, transcript
}

func appendTurn(t *testing.T, transcript, line string) {
	t.Helper()
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
}

// Test 1 (the RED discriminator): a genuine new turn — the transcript grows —
// MUST change the dedup key. The pre-fix clock-derived key did not read the
// transcript at all (it returned act:<CreatedAt-ns> with no live tmux pane), so
// appending a real message left the key unchanged → the engine treated the new
// turn as a duplicate.
func TestIssue1187_NewTurnChangesDedupKey(t *testing.T) {
	inst, transcript := setupClaudeTranscript(t, `{"type":"user","content":"first"}`+"\n")

	before := transitionEventOutputHash(inst)
	if before == "" {
		t.Fatal("issue #1187: expected a non-empty dedup key for a Claude session with a transcript")
	}

	appendTurn(t, transcript, `{"type":"assistant","content":"a genuine new turn"}`+"\n")

	after := transitionEventOutputHash(inst)
	if after == before {
		t.Fatalf("issue #1187: dedup key must change after a genuine new turn (transcript grew); got %q both before and after — the key is not content-derived", before)
	}
}

// Test 2: an idle pane whose transcript is unchanged must return the SAME key
// across many polls. This is the stability the flood needed: with a stable key,
// layer-2 dedup matches and suppresses re-fires. (Pre-fix, the clock moved on
// every window_activity tick from animated chrome, so this never held for a
// live pane.)
func TestIssue1187_IdlePaneKeyStableAcrossPolls(t *testing.T) {
	inst, _ := setupClaudeTranscript(t, `{"type":"assistant","content":"done"}`+"\n")

	first := transitionEventOutputHash(inst)
	if first == "" {
		t.Fatal("issue #1187: expected a non-empty dedup key")
	}
	for i := range 20 {
		if got := transitionEventOutputHash(inst); got != first {
			t.Fatalf("issue #1187: dedup key changed on idle poll %d (%q != %q) — animated chrome must not move the key", i, got, first)
		}
	}
}

// Test 3 (user-visible flood, integration via the dedup boundary): simulate 20
// poll ticks of an animating-but-idle pane. Each tick re-derives LastOutputHash
// the way the daemon does (transitionEventOutputHash) against an UNCHANGED
// transcript, then runs it through isDuplicate/markNotified. Exactly ONE
// [EVENT] must reach the parent, not 20.
func TestIssue1187_AnimatingIdlePane_EmitsOnce(t *testing.T) {
	n := setupNotifierForDedupTest(t)
	inst, _ := setupClaudeTranscript(t, `{"type":"assistant","content":"waiting at prompt"}`+"\n")

	base := time.Unix(1747800000, 0).UTC()
	events := make([]TransitionNotificationEvent, 20)
	for i := range 20 {
		events[i] = TransitionNotificationEvent{
			ChildSessionID: inst.ID,
			Profile:        "_test-1187",
			FromStatus:     "running",
			ToStatus:       "waiting",
			LastOutputHash: transitionEventOutputHash(inst),
			// 30s apart — well outside the 90s legacy window after the first
			// couple, so only a content-stable layer-2 key can suppress them.
			Timestamp: base.Add(time.Duration(i) * 30 * time.Second),
		}
	}

	if got := countEmitted(n, events); got != 1 {
		t.Fatalf("issue #1187: 20 animating-idle polls must collapse to 1 [EVENT], got %d", got)
	}
}

// Test 4 (regression: distinct turns must each notify): two genuine turns, with
// a real transcript write between them, must each produce an [EVENT]. The fix
// must not over-dedup away real progress.
func TestIssue1187_DistinctTurnsEachNotify(t *testing.T) {
	n := setupNotifierForDedupTest(t)
	inst, transcript := setupClaudeTranscript(t, `{"type":"assistant","content":"turn one"}`+"\n")

	base := time.Unix(1747800000, 0).UTC()

	ev1 := TransitionNotificationEvent{
		ChildSessionID: inst.ID,
		Profile:        "_test-1187",
		FromStatus:     "running",
		ToStatus:       "waiting",
		LastOutputHash: transitionEventOutputHash(inst),
		Timestamp:      base,
	}

	// Genuine second turn: user prompt + assistant response written.
	appendTurn(t, transcript, `{"type":"user","content":"second prompt"}`+"\n")
	appendTurn(t, transcript, `{"type":"assistant","content":"turn two"}`+"\n")

	ev2 := TransitionNotificationEvent{
		ChildSessionID: inst.ID,
		Profile:        "_test-1187",
		FromStatus:     "running",
		ToStatus:       "waiting",
		LastOutputHash: transitionEventOutputHash(inst),
		Timestamp:      base.Add(5 * time.Minute),
	}

	if got := countEmitted(n, []TransitionNotificationEvent{ev1, ev2}); got != 2 {
		t.Fatalf("issue #1187: two genuinely distinct turns must each notify, got %d", got)
	}
}

// Test 5 (running↔waiting flap with no progress): a child that flaps
// running→waiting repeatedly while doing NO new work (transcript unchanged)
// must not manufacture fresh edges. The content-stable key makes layer-2 catch
// the flap that defeated the legacy from+to window.
func TestIssue1187_RunningWaitingFlapNoProgress_EmitsOnce(t *testing.T) {
	n := setupNotifierForDedupTest(t)
	inst, _ := setupClaudeTranscript(t, `{"type":"assistant","content":"idle"}`+"\n")

	base := time.Unix(1747800000, 0).UTC()
	hash := transitionEventOutputHash(inst)

	// Five running→waiting edges spaced 2 minutes apart (outside the 90s
	// window), all with the same content hash because nothing was written.
	events := make([]TransitionNotificationEvent, 5)
	for i := range 5 {
		events[i] = TransitionNotificationEvent{
			ChildSessionID: inst.ID,
			Profile:        "_test-1187",
			FromStatus:     "running",
			ToStatus:       "waiting",
			LastOutputHash: hash,
			Timestamp:      base.Add(time.Duration(i) * 2 * time.Minute),
		}
	}

	if got := countEmitted(n, events); got != 1 {
		t.Fatalf("issue #1187: running↔waiting flap with no new work must emit once, got %d", got)
	}
}

// Boundary: a non-Claude session (no transcript) yields an empty key and falls
// back to the legacy 90s short-window dedup — same as before the fix. This
// pins that the change does not silently break dedup for tools without a JSONL
// transcript.
func TestIssue1187_NoTranscript_EmptyKeyFallsBackToLegacy(t *testing.T) {
	inst := &Instance{ID: "shell-child", Tool: "shell"}
	if got := transitionEventOutputHash(inst); got != "" {
		t.Fatalf("issue #1187: non-Claude session must yield empty key (legacy fallback), got %q", got)
	}
}
