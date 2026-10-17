package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreToolUseUpgrade(t *testing.T) {
	stubHookBinary(t, t.TempDir())
	dir := t.TempDir()
	if _, err := InjectClaudeHooks(dir); err != nil {
		t.Fatal(err)
	}
	hooks, err := readClaudeHooksSection(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate an earlier async PreToolUse install alongside user catch-all and Bash hooks.
	hooks["PreToolUse"] = json.RawMessage(`[{"hooks":[{"type":"command","command":"user-guard","timeout":7},{"type":"command","command":"agent-deck hook-handler","async":true}]},{"matcher":"Bash","hooks":[{"type":"prompt","prompt":"check bash","timeout":9}]}]`)
	old, err := json.Marshal(map[string]any{"hooks": hooks, "userSetting": true})
	if err != nil {
		t.Fatal(err)
	}
	path := writeSettings(t, dir, string(old))
	if hooksAlreadyInstalled(hooks) {
		t.Fatal("old async PreToolUse must need upgrade")
	}
	result, err := HealClaudeHooks(dir, "9.9.9")
	if err != nil || !result.Healed {
		t.Fatalf("heal = %+v, %v", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeUser, _ := json.Marshal(withoutAgentDeckEntries(t, old))
	afterUser, _ := json.Marshal(withoutAgentDeckEntries(t, after))
	if !bytes.Equal(beforeUser, afterUser) {
		t.Fatalf("user settings changed: %s -> %s", beforeUser, afterUser)
	}
	hooks, err = readClaudeHooksSection(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := claudeHookEventConfig{Event: "PreToolUse", Env: PreToolHookSyncMarkerEnv + "=1"}
	if !eventHasAgentDeckHookMatchingConfig(hooks["PreToolUse"], cfg, true) {
		t.Fatal("missing catch-all ordered PreToolUse")
	}
	if _, ok := hooks["PostToolUse"]; ok {
		t.Fatal("PostToolUse must remain unsubscribed")
	}
	if changed, err := InjectClaudeHooks(dir); err != nil || changed {
		t.Fatalf("second install = %v, %v", changed, err)
	}
	again, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, again) {
		t.Fatalf("second install changed bytes: %v", err)
	}
}

func TestPreToolClaudeTurnWithoutPrompt(t *testing.T) {
	for _, afterStop := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-stop=%v", afterStop), func(t *testing.T) {
			inst, cleanup := startHookLagInstance(t, "pretool", auditConductorBusyPane)
			defer cleanup()
			dir := t.TempDir()
			if _, err := InjectClaudeHooks(dir); err != nil {
				t.Fatal(err)
			}
			hooks, err := readClaudeHooksSection(dir)
			if err != nil {
				t.Fatal(err)
			}
			fire := func(event, status string, age time.Duration) {
				t.Helper()
				// Only installed events fire, so removing the subscription reports the previous Stop again.
				if _, ok := hooks[event]; !ok {
					return
				}
				body := fmt.Sprintf(`{"status":%q,"session_id":"sess-610","event":%q,"ts":%d}`, status, event, time.Now().Add(-age).Unix())
				if err := os.WriteFile(filepath.Join(GetHooksDir(), inst.ID+".json"), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fire("UserPromptSubmit", "running", time.Second)
			if got, _ := cliPass(t, inst); got != StatusRunning {
				t.Fatalf("normal prompt = %s", got)
			}
			if afterStop {
				fire("Stop", "waiting", time.Second)
				if got, _ := cliPass(t, inst); got != StatusWaiting {
					t.Fatalf("previous Stop = %s", got)
				}
			}
			// No UserPromptSubmit follows the previous Stop. Test both sides of the
			// two-minute fast-path window with the same long-tool busy pane.
			for _, seconds := range []int{1, 30, 60, 119, 125, 160} {
				fire("PreToolUse", "running", time.Duration(seconds)*time.Second)
				if got, _ := cliPass(t, inst); got != StatusRunning {
					t.Fatalf("tool at %ds = %s, want running", seconds, got)
				}
			}
			fire("Stop", "waiting", time.Second)
			if got, _ := cliPass(t, inst); got != StatusWaiting {
				t.Fatalf("final Stop = %s, want waiting immediately", got)
			}
		})
	}
}
