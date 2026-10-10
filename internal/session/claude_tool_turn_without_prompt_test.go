package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Claude turn that starts itself (task notification, inbox injection, a
// turn continued after a blocked Stop) fires no UserPromptSubmit. Before the
// PreToolUse subscription the hook file still said "waiting" from the
// previous turn's Stop, and the two-minute hook fast path reported waiting
// for a session that was busy running a tool. The live-spinner override from
// #2507 only covers frames whose spinner sits directly above the composer;
// these frames keep a busy tool turn with other rows in between.

const toolTurnComposer = "\n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"

// Claude renders the open task list under the spinner during a long tool.
const toolTurnTodoPane = "⏺ Bash(go test ./internal/session/...)\n" +
	"  ⎿  Running…\n" +
	"\n" +
	"✻ Running the test suite… (48s · ↓ 1.2k tokens)\n" +
	"  ⎿  ☒ Read the failing test\n" +
	"     ☐ Run the test suite\n" +
	"     ☐ Fix the regression\n" +
	toolTurnComposer

// The spinner text is the task's active form, which may hold paths, digits
// or punctuation.
const toolTurnActiveFormPane = "⏺ Bash(sleep 30 && make test)\n" +
	"  ⎿  Running…\n" +
	"\n" +
	"✶ Running make test for v2… (31s · ↓ 512 tokens)\n" +
	toolTurnComposer

// The frame shape #2507 already covers; kept as a control.
const toolTurnLiveSpinnerPane = "⏺ Bash(sleep 30)\n" +
	"  ⎿  Running…\n" +
	"\n" +
	"✻ Sautéing… (31s · ↓ 512 tokens)\n" +
	toolTurnComposer

func TestClaudeToolTurnWithoutPromptReportsRunning(t *testing.T) {
	panes := map[string]string{
		"todo-list":    toolTurnTodoPane,
		"active-form":  toolTurnActiveFormPane,
		"live-spinner": toolTurnLiveSpinnerPane,
	}
	for name, pane := range panes {
		t.Run(name, func(t *testing.T) {
			stubHookBinary(t, t.TempDir())
			inst, cleanup := startHookLagInstance(t, "tool-turn-"+name, pane)
			defer cleanup()
			configDir := t.TempDir()
			if _, err := InjectClaudeHooks(configDir); err != nil {
				t.Fatal(err)
			}
			hooks, err := readClaudeHooksSection(configDir)
			if err != nil {
				t.Fatal(err)
			}
			write := func(event, status string, age time.Duration) {
				t.Helper()
				body := fmt.Sprintf(`{"status":%q,"session_id":"sess-610","event":%q,"ts":%d}`,
					status, event, time.Now().Add(-age).Unix())
				if err := os.WriteFile(filepath.Join(GetHooksDir(), inst.ID+".json"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("UserPromptSubmit", "running", 3*time.Second)
			write("Stop", "waiting", 2*time.Second)
			// The next turn starts without UserPromptSubmit. Only an installed
			// PreToolUse subscription can publish its tool; without one the
			// previous Stop simply ages. Sample both sides of the fast path.
			_, preTool := hooks["PreToolUse"]
			for _, seconds := range []int{1, 5, 30, 60, 119} {
				age := time.Duration(seconds) * time.Second
				if preTool {
					write("PreToolUse", "running", age)
				} else {
					write("Stop", "waiting", age)
				}
				if got, _ := cliPass(t, inst); got != StatusRunning {
					t.Fatalf("tool turn %ds after Stop = %s, want running (PreToolUse installed: %v)", seconds, got, preTool)
				}
			}
			write("Stop", "waiting", time.Second)
			if name == "live-spinner" {
				return // #2507 keeps a live spinner running; its own tests own this.
			}
			if got, _ := cliPass(t, inst); got != StatusWaiting {
				t.Fatalf("Stop after the tool turn = %s, want waiting", got)
			}
		})
	}
}

// Installing the PreToolUse receiver is idempotent, never touches a user's
// own PreToolUse matchers, and removal takes back only the agent-deck entry.
func TestPreToolUseInstallRemoveKeepsUserHooks(t *testing.T) {
	stubHookBinary(t, t.TempDir())
	dir := t.TempDir()
	user := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"user-guard","timeout":7}]}]},"userSetting":true}`
	path := writeSettings(t, dir, user)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := InjectClaudeHooks(dir); err != nil || !changed {
		t.Fatalf("first install = %v, %v", changed, err)
	}
	hooks, err := readClaudeHooksSection(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := claudeHookEventConfig{Event: "PreToolUse", Env: "AGENTDECK_PRETOOL_SYNC=1"} // literal: the installed marker is a contract
	if !eventHasAgentDeckHookMatchingConfig(hooks["PreToolUse"], cfg, true) {
		t.Fatalf("PreToolUse receiver missing: %s", hooks["PreToolUse"])
	}
	if !strings.Contains(string(hooks["PreToolUse"]), "user-guard") {
		t.Fatalf("user PreToolUse hook lost: %s", hooks["PreToolUse"])
	}
	if strings.Contains(string(hooks["PreToolUse"]), `"async":true`) {
		t.Fatalf("PreToolUse receiver must be synchronous: %s", hooks["PreToolUse"])
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := InjectClaudeHooks(dir); err != nil || changed {
		t.Fatalf("second install = %v, %v", changed, err)
	}
	again, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(installed, again) {
		t.Fatalf("second install changed bytes: %v", err)
	}
	if _, err := RemoveClaudeHooks(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeUser, _ := json.Marshal(withoutAgentDeckEntries(t, before))
	afterUser, _ := json.Marshal(withoutAgentDeckEntries(t, after))
	if !bytes.Equal(beforeUser, afterUser) {
		t.Fatalf("remove changed user settings: %s -> %s", beforeUser, afterUser)
	}
	if strings.Contains(string(after), "hook-handler") {
		t.Fatalf("remove left an agent-deck hook: %s", after)
	}
}
