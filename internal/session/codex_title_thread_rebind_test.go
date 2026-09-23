package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Codex 0.155 runs an ephemeral thread-title generation thread next to the
// main thread. It fires agent-turn-complete with its own id but never writes a
// rollout. When its notify lands after the main thread's (seen live on
// 2026-09-23: main turn complete 03:14:11.585, title turn until 03:14:12), the
// binding moved to the title thread and every `session send` failed with
// "current rollout generation is unavailable".

// seedCodex155Rollout writes the head of a real Codex 0.155.1 rollout: a
// session_meta whose session_id and id are the thread id, then one completed
// turn.
func seedCodex155Rollout(t *testing.T, codexHome, sid, turnID string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "09", "23")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"timestamp":"2026-09-23T03:13:42.876Z","type":"session_meta","payload":{"session_id":"` + sid + `","id":"` + sid + `","cwd":"/tmp/sb-codex","originator":"codex-tui","cli_version":"0.155.1","source":"cli","thread_source":"user","history_mode":"paginated"}}`,
		`{"timestamp":"2026-09-23T03:14:09.177Z","type":"event_msg","payload":{"type":"task_started","turn_id":"` + turnID + `"}}`,
		`{"timestamp":"2026-09-23T03:14:09.180Z","type":"turn_context","payload":{"turn_id":"` + turnID + `","root_turn_id":"` + turnID + `"}}`,
		`{"timestamp":"2026-09-23T03:14:11.585Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turnID + `","last_agent_message":"pong"}}`,
	}
	path := filepath.Join(dir, "rollout-2026-09-23T05-13-42-"+sid+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexTurnComplete(inst *Instance, sid string) {
	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		SessionID: sid,
		Event:     "agent-turn-complete",
		UpdatedAt: time.Now(),
	})
}

func TestCodexHookRebind_TitleThreadAfterMainKeepsMain(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	mainSID := uniqueSID(t)
	titleSID := uniqueSID(t) // ephemeral: no rollout, ever
	seedCodex155Rollout(t, codexHome, mainSID, uniqueSID(t))

	codexTurnComplete(inst, mainSID)
	codexTurnComplete(inst, titleSID)

	if inst.CodexSessionID != mainSID {
		t.Fatalf("title-thread turn-complete usurped the binding: got %q, want %q", inst.CodexSessionID, mainSID)
	}
	if gen, err := inst.LatestCodexTurnGeneration(); err != nil || !strings.HasPrefix(gen, mainSID+":") {
		t.Fatalf("generation after title notify = %q, %v; want the main rollout's turn", gen, err)
	}
}

func TestCodexHookRebind_TitleThreadBeforeMainBindsMain(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	mainSID := uniqueSID(t)
	titleSID := uniqueSID(t)
	seedCodex155Rollout(t, codexHome, mainSID, uniqueSID(t))

	codexTurnComplete(inst, titleSID)
	if inst.CodexSessionID != "" {
		t.Fatalf("cold start bound the rollout-less title thread %q", inst.CodexSessionID)
	}
	codexTurnComplete(inst, mainSID)
	if inst.CodexSessionID != mainSID {
		t.Fatalf("main thread did not bind: got %q, want %q", inst.CodexSessionID, mainSID)
	}
}

func TestCodexHookEventEndsTurn(t *testing.T) {
	for event, want := range map[string]bool{
		"agent-turn-complete": true,
		"turn/completed":      true,
		"turn.failed":         true,
		"turn-aborted":        true,
		"turn/cancelled":      true,
		"turn-ended":          true,
		"thread.started":      false,
		"session/configured":  false,
		"agent-turn-start":    false,
		"turn/started":        false,
		"turn/pending":        false,
		"UserPromptSubmit":    false,
		"":                    false,
	} {
		if got := codexHookEventEndsTurn(event); got != want {
			t.Errorf("codexHookEventEndsTurn(%q) = %v, want %v", event, got, want)
		}
	}
}

func stubCodexPaneOpenPaths(t *testing.T, paths []string, err error) {
	t.Helper()
	restore := codexPaneOpenPaths
	t.Cleanup(func() { codexPaneOpenPaths = restore })
	codexPaneOpenPaths = func(*Instance) ([]string, error) { return paths, err }
}

func TestLiveCodexThreadID(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	if err := os.MkdirAll(filepath.Join(codexHome, "thread-writer-locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	a, b := uniqueSID(t), uniqueSID(t)
	lock := func(id string) string { return filepath.Join(codexHome, "thread-writer-locks", id+".lock") }
	rollout := func(id string) string {
		return filepath.Join(codexHome, "sessions", "2026", "09", "23", "rollout-2026-09-23T05-36-36-"+id+".jsonl")
	}
	cases := []struct {
		name  string
		paths []string
		err   error
		want  string
	}{
		{"fresh composer holds only its lock", []string{"/dev/null", lock(a)}, nil, a},
		{"idle thread holds lock and rollout", []string{lock(a), rollout(a)}, nil, a},
		{"after /new two threads are open", []string{lock(a), lock(b), rollout(a)}, nil, ""},
		{"lock outside this Codex home", []string{"/elsewhere/thread-writer-locks/" + a + ".lock"}, nil, ""},
		{"incomplete probe", []string{lock(a)}, errors.New("partial"), ""},
		{"no Codex files", []string{"/dev/null"}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubCodexPaneOpenPaths(t, tc.paths, tc.err)
			if got := inst.LiveCodexThreadID(); got != tc.want {
				t.Fatalf("LiveCodexThreadID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLatestCodexTurnGeneration_FreshThreadOwnedByLiveProcess(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	lockDir := filepath.Join(codexHome, "thread-writer-locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fresh, other := uniqueSID(t), uniqueSID(t)
	inst.CodexSessionID = fresh

	stubCodexPaneOpenPaths(t, []string{filepath.Join(lockDir, fresh+".lock")}, nil)
	if gen, err := inst.LatestCodexTurnGeneration(); err != nil || gen != "" {
		t.Fatalf("fresh live thread: generation = %q, %v; want empty, nil", gen, err)
	}

	stubCodexPaneOpenPaths(t, []string{filepath.Join(lockDir, other+".lock")}, nil)
	if _, err := inst.LatestCodexTurnGeneration(); err == nil {
		t.Fatal("a rollout-less identity the live process does not own must stay unavailable")
	}

	stubCodexPaneOpenPaths(t, nil, nil)
	if _, err := inst.LatestCodexTurnGeneration(); err == nil {
		t.Fatal("a rollout-less identity with no live process must stay unavailable")
	}
}
