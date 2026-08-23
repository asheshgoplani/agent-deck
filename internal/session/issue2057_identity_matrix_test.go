package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write2057Hook(t *testing.T, id string, fields map[string]any) {
	t.Helper()
	fields["status"] = "waiting"
	fields["ts"] = time.Now().Unix()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(GetHooksDir(), id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIssue2057_CrossHarnessIdentityAndDurableDrain(t *testing.T) {
	inboxTestHome(t)
	if err := os.MkdirAll(GetHooksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	parent := "parent-matrix"

	// Codex primary: upstream text is boundary-hashed, stable on repeat, distinct by turn.
	codex := &Instance{ID: "codex-primary", Tool: "codex"}
	write2057Hook(t, codex.ID, map[string]any{"event": "turn.completed", "codex_completed_generation": "thread:turn-1"})
	c1 := transitionEventOutputHash(codex)
	if c1 == "" || strings.Contains(c1, "thread:turn-1") || transitionEventOutputHash(codex) != c1 {
		t.Fatalf("unstable/raw primary signal %q", c1)
	}
	write2057Hook(t, codex.ID, map[string]any{"event": "turn.completed", "codex_completed_generation": "thread:turn-2"})
	c2 := transitionEventOutputHash(codex)

	// Codex fallback: completion-bound sequence ignores generic sequence churn/noise.
	fallback := &Instance{ID: "codex-fallback", Tool: "codex"}
	write2057Hook(t, fallback.ID, map[string]any{"event": "turn.completed", "sequence": 10, "codex_started_sequence": 4, "codex_completed_sequence": 4})
	f1 := transitionEventOutputHash(fallback)
	write2057Hook(t, fallback.ID, map[string]any{"event": "status.changed", "sequence": 99, "codex_started_sequence": 4, "codex_completed_sequence": 4})
	if got := transitionEventOutputHash(fallback); got != f1 {
		t.Fatalf("noise changed fallback %q -> %q", f1, got)
	}
	write2057Hook(t, fallback.ID, map[string]any{"event": "turn.completed", "codex_started_sequence": 5, "codex_completed_sequence": 5})
	f2 := transitionEventOutputHash(fallback)

	// Claude transcript identity is stable by append-only content boundary.
	transcript := filepath.Join(t.TempDir(), "claude.jsonl")
	if err := os.WriteFile(transcript, []byte("turn-one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claude := &Instance{ID: "claude", Tool: "claude", lastJSONLPath: transcript}
	cl1 := transitionEventOutputHash(claude)
	if transitionEventOutputHash(claude) != cl1 {
		t.Fatal("Claude same-turn signal changed")
	}
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("turn-two\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	cl2 := transitionEventOutputHash(claude)

	signals := []string{c1, c2, f1, f2, cl1, cl2}
	seen := map[string]bool{}
	for _, signal := range signals {
		if signal == "" || seen[signal] {
			t.Fatalf("cross-harness identity collision %q", signal)
		}
		seen[signal] = true
		ev := TransitionNotificationEvent{ChildSessionID: "shared-child", Profile: "personal", FromStatus: "running", ToStatus: "waiting", LastOutputHash: signal, Timestamp: time.Now()}
		if err := CommitToInbox(parent, ev); err != nil {
			t.Fatal(err)
		}
		if err := CommitToInbox(parent, ev); err != nil {
			t.Fatal(err)
		} // retry stays once
	}
	if got := readInboxLines(t, parent); len(got) != len(signals) {
		t.Fatalf("pending=%d want=%d", len(got), len(signals))
	}
	if _, err := DrainStagePhaseForCrashTest(parent); err != nil {
		t.Fatal(err)
	}
	drained, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(drained) != len(signals) {
		t.Fatalf("WAL recovery drained=%d want=%d", len(drained), len(signals))
	}
	if again, err := DrainInboxForParent(parent); err != nil || len(again) != 0 {
		t.Fatalf("repeat drain=%d err=%v", len(again), err)
	}
}

func TestIssue2057_FallbackAmbiguityFailsClosed(t *testing.T) {
	inboxTestHome(t)
	if err := os.MkdirAll(GetHooksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: "ambiguous", Tool: "codex"}
	for _, fields := range []map[string]any{
		{"event": "turn.completed", "sequence": 22},
		{"event": "turn.completed", "codex_started_sequence": 7, "codex_completed_sequence": 6},
	} {
		write2057Hook(t, inst.ID, fields)
		if got := transitionEventOutputHash(inst); got != "" {
			t.Fatalf("ambiguous fallback invented %q", got)
		}
	}
	if err := os.WriteFile(filepath.Join(GetHooksDir(), inst.ID+".json"), []byte(`{"status":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := transitionEventOutputHash(inst); got != "" {
		t.Fatalf("partial JSON invented %q", got)
	}
}
