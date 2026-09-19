package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review round 3, finding 1: the heal rewrote settings.json through typed
// structs that only knew type/command/async, so every user hook sharing one
// of our events lost its timeout, a prompt hook became {"type":"prompt",
// "command":""}, and the top-level keys were re-sorted. The rewrite must be
// lossless: only the agent-deck entries change, everything else (unknown
// fields, key order, custom matchers, non-hook settings) round-trips.

// userSettingsFixture is a settings.json a real operator might have: user
// hooks with timeouts, a prompt hook, an http hook, custom matchers, keys in
// non-alphabetical order, a nested unknown object, plus a stale (marker-less)
// agent-deck Stop entry so the heal has something to repair.
func userSettingsFixture(exe string) string {
	return `{
  "permissions": {"allow": ["Bash(ls:*)"], "deny": []},
  "model": "opus",
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "/home/u/mine.sh", "timeout": 120},
          {"type": "command", "command": "` + exe + ` hook-handler"},
          {"type": "prompt", "prompt": "Check the work is done", "timeout": 30}
        ]
      },
      {
        "matcher": "custom",
        "hooks": [{"type": "http", "url": "http://localhost:9/stop", "statusMessage": "posting"}]
      }
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/u/guard.sh", "timeout": 5, "if": "Bash(rm:*)"}]}
    ],
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "/home/u/start.sh", "async": true, "timeout": 9}]},
      {"hooks": [{"type": "command", "command": "` + exe + ` hook-handler", "async": true}]}
    ],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "` + exe + ` hook-handler", "async": true}]}],
    "PermissionRequest": [{"hooks": [{"type": "command", "command": "` + exe + ` hook-handler"}]}],
    "Notification": [{"matcher": "permission_prompt|elicitation_dialog", "hooks": [{"type": "command", "command": "` + exe + ` hook-handler", "async": true}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "` + exe + ` hook-handler", "async": true}]}],
    "PreCompact": [{"hooks": [{"type": "command", "command": "` + exe + ` hook-handler"}]}]
  },
  "env": {"FOO": "bar"},
  "zeta": {"nested": [1, 2, {"deep": true}]},
  "alpha": 1
}
`
}

func writeSettings(t *testing.T, configDir, content string) string {
	t.Helper()
	p := filepath.Join(configDir, "settings.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// withoutAgentDeckEntries strips every agent-deck entry (and the matcher or
// event that held nothing else) from a decoded settings tree so two trees
// can be compared "except for the agent-deck entries".
func withoutAgentDeckEntries(t *testing.T, data []byte) any {
	t.Helper()
	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatalf("parse: %v\n%s", err, data)
	}
	hooks, _ := tree["hooks"].(map[string]any)
	for event, raw := range hooks {
		matchers, _ := raw.([]any)
		var keptMatchers []any
		for _, m := range matchers {
			mm, _ := m.(map[string]any)
			entries, _ := mm["hooks"].([]any)
			var kept []any
			for _, e := range entries {
				em, _ := e.(map[string]any)
				cmd, _ := em["command"].(string)
				if !isAgentDeckHookCommand(cmd) {
					kept = append(kept, e)
				}
			}
			if len(kept) == 0 {
				continue // a matcher holding only our entry
			}
			mm["hooks"] = kept
			keptMatchers = append(keptMatchers, mm)
		}
		if len(keptMatchers) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = keptMatchers
	}
	return tree
}

func topLevelKeyOrder(t *testing.T, data []byte) []string {
	t.Helper()
	var obj jsonObject
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range obj {
		keys = append(keys, f.Key)
	}
	return keys
}

func TestHealClaudeHooks_PreservesUserHooksByteForByte(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	before := userSettingsFixture(exe)
	path := writeSettings(t, configDir, before)

	res, err := HealClaudeHooks(configDir, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healed || len(res.Reasons) == 0 {
		t.Fatalf("the marker-less Stop entry must be healed: %+v", res)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Every non-agent-deck byte of meaning survives: unknown fields, custom
	// matchers, other events, non-hook settings.
	wantTree := withoutAgentDeckEntries(t, []byte(before))
	gotTree := withoutAgentDeckEntries(t, after)
	wantJSON, _ := json.Marshal(wantTree)
	gotJSON, _ := json.Marshal(gotTree)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("user content changed\nbefore: %s\nafter:  %s", wantJSON, gotJSON)
	}
	for _, needle := range []string{
		`"timeout": 120`, `"prompt": "Check the work is done"`, `"timeout": 30`,
		`"url": "http://localhost:9/stop"`, `"statusMessage": "posting"`, `"if": "Bash(rm:*)"`,
		`"matcher": "custom"`, `"matcher": "startup"`, `"timeout": 9`,
	} {
		if !strings.Contains(string(after), needle) {
			t.Errorf("user field %s missing after heal:\n%s", needle, after)
		}
	}
	// Key order is preserved (the old rewrite re-sorted the top level).
	if got, want := topLevelKeyOrder(t, after), topLevelKeyOrder(t, []byte(before)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("top-level key order changed: got %v want %v", got, want)
	}
	// Only the agent-deck entries changed, and they are now the pinned form.
	cmds := readHookCommands(t, configDir)
	if want := StopHookSyncMarkerEnv + "=1 " + exe + " hook-handler"; cmds["Stop"][1] != want {
		t.Fatalf("Stop agent-deck entry = %q want %q (position must be kept)", cmds["Stop"][1], want)
	}
	if cmds["Stop"][0] != "/home/u/mine.sh" {
		t.Fatalf("user hook moved or changed: %v", cmds["Stop"])
	}
	// The one-time backup exists and holds the pre-heal bytes.
	backups, _ := filepath.Glob(filepath.Join(configDir, "settings.json.bak-agentdeck-*"))
	if len(backups) != 1 {
		t.Fatalf("exactly one backup expected, got %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != before {
		t.Fatalf("backup must hold the pre-heal file:\n%s", b)
	}

	// Idempotent: a second heal changes nothing, writes nothing, adds no backup.
	info1, _ := os.Stat(path)
	again, err := HealClaudeHooks(configDir, "9.9.9")
	if err != nil || again.Healed || len(again.Reasons) != 0 {
		t.Fatalf("second heal must be a no-op: %+v err=%v", again, err)
	}
	after2, _ := os.ReadFile(path)
	if string(after2) != string(after) {
		t.Fatal("second heal changed the file")
	}
	info2, _ := os.Stat(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("second heal rewrote the file")
	}
	if backups2, _ := filepath.Glob(filepath.Join(configDir, "settings.json.bak-agentdeck-*")); len(backups2) != 1 {
		t.Fatalf("backup must be one-time, got %v", backups2)
	}
}

func TestHealClaudeHooks_MalformedSettingsIsAnErrorNotAWrite(t *testing.T) {
	stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	broken := `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "agent-deck hook-handler"}]}]}` // missing closing brace
	path := writeSettings(t, configDir, broken)
	info, _ := os.Stat(path)

	res, err := HealClaudeHooks(configDir, "9.9.9")
	if err == nil {
		t.Fatalf("malformed settings.json must surface an error, got %+v", res)
	}
	if res.Healed {
		t.Fatal("malformed settings.json must never be healed")
	}
	after, _ := os.ReadFile(path)
	if string(after) != broken {
		t.Fatalf("malformed settings.json was rewritten:\n%s", after)
	}
	if info2, _ := os.Stat(path); !info.ModTime().Equal(info2.ModTime()) {
		t.Fatal("malformed settings.json was touched")
	}
	if backups, _ := filepath.Glob(filepath.Join(configDir, "settings.json.bak-agentdeck-*")); len(backups) != 0 {
		t.Fatalf("no backup on a refused heal, got %v", backups)
	}
}

// The explicit install and the uninstall go through the same lossless
// round trip.
func TestInjectAndRemoveClaudeHooks_LosslessForUserHooks(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	// A user file WITHOUT agent-deck entries, keys out of order.
	user := `{
  "zeta": 1,
  "hooks": {
    "Stop": [{"hooks": [{"type": "prompt", "prompt": "done?", "timeout": 30}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/g.sh", "timeout": 5}]}]
  },
  "alpha": {"b": 2, "a": 1}
}
`
	path := writeSettings(t, configDir, user)
	if installed, err := InjectClaudeHooks(configDir); err != nil || !installed {
		t.Fatalf("install: %v %v", installed, err)
	}
	after, _ := os.ReadFile(path)
	wantJSON, _ := json.Marshal(withoutAgentDeckEntries(t, []byte(user)))
	gotJSON, _ := json.Marshal(withoutAgentDeckEntries(t, after))
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("install changed user content\nbefore: %s\nafter:  %s", wantJSON, gotJSON)
	}
	if got, want := topLevelKeyOrder(t, after), []string{"zeta", "hooks", "alpha"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("install changed key order: %v", got)
	}
	if !strings.Contains(string(after), `"a": 1`) || strings.Index(string(after), `"b": 2`) > strings.Index(string(after), `"a": 1`) {
		t.Fatalf("nested key order changed:\n%s", after)
	}
	if cmds := readHookCommands(t, configDir); cmds["Stop"][0] != "" && !strings.Contains(strings.Join(cmds["Stop"], " "), exe) {
		t.Fatalf("agent-deck Stop entry missing: %v", cmds["Stop"])
	}

	if removed, err := RemoveClaudeHooks(configDir); err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	final, _ := os.ReadFile(path)
	var want, got any
	_ = json.Unmarshal([]byte(user), &want)
	if err := json.Unmarshal(final, &got); err != nil {
		t.Fatal(err)
	}
	w, _ := json.Marshal(want)
	g, _ := json.Marshal(got)
	if string(w) != string(g) {
		t.Fatalf("uninstall must restore the user's file\nwant: %s\ngot:  %s", w, g)
	}
}
