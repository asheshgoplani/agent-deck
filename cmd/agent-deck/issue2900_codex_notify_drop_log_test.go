package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// TestHandleCodexNotify_DropLogsReason pins issue #2900: each notify payload
// handleCodexNotify drops (an unbacked turn end, a subagent thread) leaves
// exactly one debug line naming the reason, thread, turn and event, and never
// the turn's text. A payload that is kept leaves none.
func TestHandleCodexNotify_DropLogsReason(t *testing.T) {
	const secret = "SECRET-ASSISTANT-TEXT-2900"
	cases := []struct {
		name       string
		thread     string
		subagent   bool   // seed a thread_source=subagent rollout for thread
		mainBacked bool   // seed a user rollout for thread
		wantReason string // "" = kept, no drop line
	}{
		{name: "unbacked turn end", thread: "helper-thread-2900", wantReason: "unbacked_turn_end"},
		{name: "subagent thread", thread: "subagent-thread-2900", subagent: true, wantReason: "subagent_thread"},
		{name: "main thread kept", thread: "main-thread-2900", mainBacked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTDECK_DEBUG", "")
			logDir := initTestLogging(t)
			tmpHome := t.TempDir()
			t.Setenv("HOME", tmpHome)
			t.Setenv("CODEX_HOME", "")
			t.Setenv("AGENTDECK_INSTANCE_ID", "inst-2900")
			t.Setenv("CODEX_SESSION_ID", "")
			dir := filepath.Join(tmpHome, ".codex", "sessions", "2026", "10", "25")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.mainBacked {
				seedCodexNotifyRollout(t, tmpHome, tc.thread)
			}
			if tc.subagent {
				meta := `{"type":"session_meta","payload":{"id":"` + tc.thread +
					`","parent_thread_id":"parent-2900","thread_source":"subagent"}}` + "\n"
				if err := os.WriteFile(filepath.Join(dir, "rollout-2026-10-25T10-00-00-"+tc.thread+".jsonl"), []byte(meta), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			origArgs := os.Args
			t.Cleanup(func() { os.Args = origArgs })
			os.Args = []string{"agent-deck", "codex-notify",
				`{"type":"agent-turn-complete","thread-id":"` + tc.thread + `","turn-id":"turn-2900","last-assistant-message":"` + secret + `"}`}
			handleCodexNotify()

			logging.Shutdown()
			body := readLogTolerant(t, logDir)
			if strings.Contains(body, secret) {
				t.Fatalf("drop log leaked the turn text:\n%s", body)
			}
			var drops []map[string]any
			for _, line := range strings.Split(body, "\n") {
				if !strings.Contains(line, `"codex_notify_dropped"`) {
					continue
				}
				var rec map[string]any
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("unparseable log line %q: %v", line, err)
				}
				drops = append(drops, rec)
			}
			if tc.wantReason == "" {
				if len(drops) != 0 {
					t.Fatalf("kept payload logged a drop: %v", drops)
				}
				return
			}
			if len(drops) != 1 {
				t.Fatalf("got %d codex_notify_dropped lines, want 1:\n%s", len(drops), body)
			}
			rec := drops[0]
			want := map[string]string{
				"level": "DEBUG", "reason": tc.wantReason, "thread_id": tc.thread,
				"turn_id": "turn-2900", "event": "agent-turn-complete", "instance": "inst-2900",
			}
			for k, v := range want {
				if got, _ := rec[k].(string); got != v {
					t.Errorf("%s = %q, want %q (record %v)", k, got, v, rec)
				}
			}
		})
	}
}

// TestWriteCodexHookStatus_WaitsOutWriterLockContention pins issue #2900: a
// turn end whose writer finds the per-instance lock held (an overlapping
// notify process, a slow disk) gave up after 100 ms and the turn end never
// reached the hook status. It now waits for the lock, within a bound.
func TestWriteCodexHookStatus_WaitsOutWriterLockContention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOOK_GENERATION", "")
	if err := os.MkdirAll(getHooksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	const instanceID = "inst-2900-lock"
	lock, err := os.OpenFile(filepath.Join(getHooksDir(), instanceID+".codex-writer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer closeChecked(lock)
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(400 * time.Millisecond)
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}()

	writeCodexHookStatus(instanceID, "waiting", "thread-2900", "agent-turn-complete", "turn-2900")
	<-released

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("turn end lost under writer-lock contention: %v", err)
	}
	var got hookStatusFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "waiting" || got.CodexCompletedGeneration != "thread-2900:turn-2900" {
		t.Fatalf("status after contention = %#v", got)
	}
}
