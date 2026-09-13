package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Exercise the actual notify entrypoint, not synthetic DONE fields written by
// the test. Only the successful assistant completion payload can carry proof.
func TestIssue2091CodexNotifyPersistsExplicitDone(t *testing.T) {
	for _, tc := range []struct {
		name, event, assistant, input, want string
	}{
		{"explicit assistant done", "agent-turn-complete", "===AGENTDECK_DONE=== status=ok summary=finished", "", "ok"},
		{"ordinary turn", "agent-turn-complete", "What next?", "", ""},
		{"input is not assistant proof", "agent-turn-complete", "Working", "===AGENTDECK_DONE=== status=ok summary=spoof", ""},
		{"failed turn veto", "turn-failed", "===AGENTDECK_DONE=== status=ok summary=stale", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("AGENTDECK_INSTANCE_ID", "issue2091-notify")
			t.Setenv("AGENTDECK_HOOK_GENERATION", "")
			payload, err := json.Marshal(map[string]any{
				"type": tc.event, "thread-id": "thread2091", "turn-id": "turn2091",
				"last-assistant-message": tc.assistant, "input-messages": []string{tc.input},
			})
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Args
			os.Args = []string{"agent-deck", "codex-notify", string(payload)}
			t.Cleanup(func() { os.Args = previous })
			handleCodexNotify()
			data, err := os.ReadFile(filepath.Join(getHooksDir(), "issue2091-notify.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record hookStatusFile
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.DoneStatus != tc.want {
				t.Fatalf("actual notify DONE = %q, want %q", record.DoneStatus, tc.want)
			}
		})
	}
}

func TestIssue2091CodexProducerRejectsPriorLaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AGENTDECK_HOOK_GENERATION", "current")
	id := "issue2091-bound"
	if err := os.MkdirAll(getHooksDir(), 0700); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(getHooksDir(), id+".generation.json")
	if err := os.WriteFile(control, []byte(`{"generation":"current","next_sequence":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeCodexHookStatus(id, "waiting", "current-thread", "agent-turn-complete", "turn", "===AGENTDECK_DONE=== status=ok summary=done")
	path := filepath.Join(getHooksDir(), id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record hookStatusFile
	if err := json.Unmarshal(before, &record); err != nil {
		t.Fatal(err)
	}
	if record.HookGeneration != "current" || record.Sequence != 1 || record.DoneStatus != "ok" {
		t.Fatalf("producer did not bind evidence: %+v", record)
	}
	t.Setenv("AGENTDECK_HOOK_GENERATION", "old")
	writeCodexHookStatus(id, "waiting", "old-thread", "agent-turn-complete", "old-turn", "===AGENTDECK_DONE=== status=ok summary=old")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("old launch replaced current completion")
	}
	if got := session.ReadHookSessionAnchor(id); got != "current-thread" {
		t.Fatalf("old launch changed current anchor to %q", got)
	}
}

func TestIssue2091DelayedCodexDoneCannotReplaceNewTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AGENTDECK_HOOK_GENERATION", "")
	id := "issue2091-turn-order"
	writeCodexHookStatus(id, "running", "thread", "agent-turn-start", "A")
	writeCodexHookStatus(id, "waiting", "thread", "agent-turn-complete", "A", "===AGENTDECK_DONE=== status=ok summary=A")
	writeCodexHookStatus(id, "running", "thread", "agent-turn-start", "B")
	path := filepath.Join(getHooksDir(), id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeCodexHookStatus(id, "waiting", "thread", "agent-turn-complete", "A", "===AGENTDECK_DONE=== status=ok summary=late A")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("late A replaced running B")
	}
}

func TestIssue2091CodexFailureAndMissingTurnVeto(t *testing.T) {
	for _, terminal := range []string{"turn-failed", "turn-cancelled", "metadata-after-failure", "unknown-failure-after-failure", "first-unknown-failure", "duplicate-start-after-failure", "missing-turn"} {
		t.Run(terminal, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("AGENTDECK_HOOK_GENERATION", "")
			id := "issue2091-terminal-order"
			writeCodexHookStatus(id, "running", "thread", "agent-turn-start", "A")
			writeCodexHookStatus(id, "waiting", "thread", "agent-turn-complete", "A", "===AGENTDECK_DONE=== status=ok summary=A")
			if terminal != "missing-turn" {
				writeCodexHookStatus(id, "running", "thread", "agent-turn-start", "B")
				if terminal == "metadata-after-failure" || terminal == "unknown-failure-after-failure" || terminal == "first-unknown-failure" || terminal == "duplicate-start-after-failure" {
					if terminal != "first-unknown-failure" {
						writeCodexHookStatus(id, "waiting", "thread", "turn-failed", "B")
					}
					switch terminal {
					case "metadata-after-failure":
						writeCodexHookStatus(id, "waiting", "thread", "session.configured")
					case "unknown-failure-after-failure", "first-unknown-failure":
						writeCodexHookStatus(id, "waiting", "thread", "turn-failed", "")
					case "duplicate-start-after-failure":
						writeCodexHookStatus(id, "running", "thread", "agent-turn-start", "B")
					}
				} else {
					writeCodexHookStatus(id, "waiting", "thread", terminal, "B")
				}
			}
			turn := "A"
			if terminal == "metadata-after-failure" || terminal == "unknown-failure-after-failure" || terminal == "first-unknown-failure" || terminal == "duplicate-start-after-failure" {
				turn = "B"
			}
			if terminal == "missing-turn" {
				turn = ""
			}
			writeCodexHookStatus(id, "waiting", "thread", "agent-turn-complete", turn, "===AGENTDECK_DONE=== status=ok summary=late")
			data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var got hookStatusFile
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.DoneStatus != "" {
				t.Fatalf("ambiguous/contradicted completion became DONE: %+v", got)
			}
		})
	}
}

func TestIssue2091NewTurnRecoversFromUnidentifiedFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("AGENTDECK_HOOK_GENERATION", "")
	id := "issue2091-new-valid-turn"
	writeCodexHookStatus(id, "running", "thread", "agent-turn-start", "B")
	writeCodexHookStatus(id, "waiting", "thread", "turn-failed", "")
	writeCodexHookStatus(id, "running", "thread", "agent-turn-start", "C")
	writeCodexHookStatus(id, "waiting", "thread", "agent-turn-complete", "C", "===AGENTDECK_DONE=== status=ok summary=C")
	data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var got hookStatusFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.DoneStatus != "ok" || got.CodexCompletedGeneration != "thread:C" {
		t.Fatalf("new valid turn did not recover: %+v", got)
	}
}
