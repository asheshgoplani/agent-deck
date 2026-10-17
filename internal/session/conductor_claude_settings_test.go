package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for issue #1358: conductor .claude/settings.json auto-allow
// policy with maintainer tightenings (read-only auto-allowed; lifecycle/mutating
// commands prompted; conductor-dir executable/config writes NOT blanket-allowed).

type conductorPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

func loadConductorPerms(t *testing.T, dir string) conductorPermissions {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings.json not valid JSON: %v\n%s", err, data)
	}
	raw, ok := root["permissions"]
	if !ok {
		t.Fatalf("settings.json missing permissions key:\n%s", data)
	}
	var perms conductorPermissions
	if err := json.Unmarshal(raw, &perms); err != nil {
		t.Fatalf("permissions not valid JSON: %v", err)
	}
	return perms
}

func permContains(list []string, want string) bool {
	for _, e := range list {
		if e == want {
			return true
		}
	}
	return false
}

func anyHasPrefix(list []string, prefix string) bool {
	for _, e := range list {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// TestConductorClaudeSettings_ReadOnlyAutoAllowed verifies genuinely read-only
// commands are in the allow list with no prompt.
func TestConductorClaudeSettings_ReadOnlyAutoAllowed(t *testing.T) {
	dir := t.TempDir()
	if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
		t.Fatalf("writeConductorClaudeSettingsAt: %v", err)
	}
	perms := loadConductorPerms(t, dir)

	for _, cmd := range []string{
		"Bash(agent-deck status *)",
		"Bash(agent-deck list *)",
		"Bash(agent-deck session output *)",
		"Bash(agent-deck session show *)",
		"Bash(agent-deck inbox *)",
		"Bash(agent-deck session restart *)",
	} {
		if !permContains(perms.Allow, cmd) {
			t.Errorf("expected read-only/safe command auto-allowed: %q\nallow=%v", cmd, perms.Allow)
		}
	}
	// Shell commands must be Bash(...)-wrapped — a bare command string is read by
	// Claude Code as a tool name and silently never matches.
	for _, bare := range []string{"agent-deck status *", "agent-deck list *"} {
		if permContains(perms.Allow, bare) {
			t.Errorf("command must be Bash(...)-wrapped, found bare entry: %q", bare)
		}
	}
}

// TestConductorClaudeSettings_LifecycleAndMutatingPrompt verifies the dangerous
// command-replay / injection surfaces are NOT silently auto-allowed and instead
// go to the ask (prompt) list.
func TestConductorClaudeSettings_LifecycleAndMutatingPrompt(t *testing.T) {
	dir := t.TempDir()
	if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
		t.Fatalf("writeConductorClaudeSettingsAt: %v", err)
	}
	perms := loadConductorPerms(t, dir)

	for _, cmd := range []string{
		"Bash(agent-deck session start *)",
		"Bash(agent-deck session stop *)",
		"Bash(agent-deck session send *)",
		"Bash(agent-deck launch *)",
	} {
		if permContains(perms.Allow, cmd) {
			t.Errorf("mutating/lifecycle command must NOT be auto-allowed: %q\nallow=%v", cmd, perms.Allow)
		}
		if !permContains(perms.Ask, cmd) {
			t.Errorf("mutating/lifecycle command must be in ask list: %q\nask=%v", cmd, perms.Ask)
		}
	}
}

// TestConductorClaudeSettings_NoBlanketDirWrite is the core security guarantee:
// writes are NOT blanket-allowed over the conductor dir, and the executable/
// config paths are explicitly denied (deny takes precedence).
func TestConductorClaudeSettings_NoBlanketDirWrite(t *testing.T) {
	dir := t.TempDir()
	if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
		t.Fatalf("writeConductorClaudeSettingsAt: %v", err)
	}
	perms := loadConductorPerms(t, dir)

	// No recursive write/edit over the whole conductor dir.
	for _, bad := range []string{
		"Write(//" + dir + "/**)",
		"Edit(//" + dir + "/**)",
	} {
		if permContains(perms.Allow, bad) {
			t.Errorf("must NOT blanket write-allow the conductor dir: %q", bad)
		}
	}
	if anyHasPrefix(perms.Allow, "Write(//"+dir+"/**") || anyHasPrefix(perms.Allow, "Edit(//"+dir+"/**") {
		t.Errorf("must NOT have a recursive write-allow glob over the conductor dir\nallow=%v", perms.Allow)
	}

	// The executable/config paths must be explicitly denied.
	for _, deny := range []string{
		"Write(//" + dir + "/.claude/**)",
		"Write(//" + dir + "/.mcp.json)",
		"Write(//" + dir + "/.envrc)",
		"Write(//" + dir + "/*.sh)",
	} {
		if !permContains(perms.Deny, deny) {
			t.Errorf("expected deny rule for self-escalation path: %q\ndeny=%v", deny, perms.Deny)
		}
	}

	// Scoped data-file writes ARE allowed.
	for _, allow := range []string{
		"Write(//" + dir + "/state.json)",
		"Write(//" + dir + "/task-log.md)",
	} {
		if !permContains(perms.Allow, allow) {
			t.Errorf("expected scoped data-file write allowed: %q\nallow=%v", allow, perms.Allow)
		}
	}
}

// TestConductorClaudeSettings_Idempotent verifies re-running setup does not
// duplicate entries and preserves unmanaged top-level keys plus user-added
// permissions.
func TestConductorClaudeSettings_Idempotent(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Pre-seed with an unmanaged key and a user-added permission.
	seed := `{"model":"opus","permissions":{"allow":["Bash(ls *)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
		t.Fatalf("second write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	// Unmanaged key preserved.
	if string(root["model"]) != `"opus"` {
		t.Errorf("unmanaged top-level key must be preserved, got model=%s", root["model"])
	}
	perms := loadConductorPerms(t, dir)
	// User-added permission preserved.
	if !permContains(perms.Allow, "Bash(ls *)") {
		t.Errorf("user-added permission must be preserved\nallow=%v", perms.Allow)
	}
	// No duplicate of a managed entry.
	count := 0
	for _, e := range perms.Allow {
		if e == "Bash(agent-deck status *)" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("managed entry duplicated after re-run: count=%d\nallow=%v", count, perms.Allow)
	}
}

// TestConductorClaudeSettings_PreservesNestedPermissionKeys verifies that
// unmanaged nested keys inside the permissions object (defaultMode,
// additionalDirectories, security-sensitive disableBypassPermissionsMode) are
// preserved across a merge — we only touch allow/ask/deny.
func TestConductorClaudeSettings_PreservesNestedPermissionKeys(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"permissions":{"defaultMode":"acceptEdits","disableBypassPermissionsMode":"disable","additionalDirectories":["/tmp/extra"],"allow":["Bash(ls *)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	var permsObj map[string]json.RawMessage
	if err := json.Unmarshal(root["permissions"], &permsObj); err != nil {
		t.Fatalf("permissions not valid JSON: %v", err)
	}
	// Compare semantically (re-marshal to canonical form) since MarshalIndent
	// reformats whitespace in preserved raw values.
	canon := func(raw json.RawMessage) string {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("re-unmarshal preserved value: %v", err)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	for key, want := range map[string]string{
		"defaultMode":                  `"acceptEdits"`,
		"disableBypassPermissionsMode": `"disable"`,
		"additionalDirectories":        `["/tmp/extra"]`,
	} {
		got := canon(permsObj[key])
		if got != want {
			t.Errorf("nested permissions key %q must be preserved: want %s, got %s", key, want, got)
		}
	}
	// And the user-added allow entry survives alongside the managed ones.
	perms := loadConductorPerms(t, dir)
	if !permContains(perms.Allow, "Bash(ls *)") {
		t.Errorf("user-added allow entry must survive\nallow=%v", perms.Allow)
	}
	if !permContains(perms.Allow, "Bash(agent-deck status *)") {
		t.Errorf("managed allow entry must be present\nallow=%v", perms.Allow)
	}
}

// TestConductorClaudeSettings_PermissionAskOff verifies [conductor]
// permission_ask = false: a fresh write has no ask list, the mutating commands
// are still not auto-allowed, and allow/deny match the default policy.
func TestConductorClaudeSettings_PermissionAskOff(t *testing.T) {
	dir := t.TempDir()
	if err := writeConductorClaudeSettingsAt(dir, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	perms := loadConductorPerms(t, dir)
	if perms.Ask != nil {
		t.Errorf("permission_ask = false must not write an ask list\nask=%v", perms.Ask)
	}
	for _, cmd := range conductorAskCommands {
		if permContains(perms.Allow, cmd) {
			t.Errorf("mutating/lifecycle command must NOT be auto-allowed: %q", cmd)
		}
	}

	withAsk := t.TempDir()
	if err := writeConductorClaudeSettingsAt(withAsk, true); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := loadConductorPerms(t, withAsk)
	rebase := func(list []string) string {
		return strings.ReplaceAll(strings.Join(list, "\n"), withAsk, dir)
	}
	if strings.Join(perms.Allow, "\n") != rebase(want.Allow) {
		t.Errorf("allow differs from the default policy:\ngot  %v\nwant %v", perms.Allow, want.Allow)
	}
	if strings.Join(perms.Deny, "\n") != rebase(want.Deny) {
		t.Errorf("deny differs from the default policy:\ngot  %v\nwant %v", perms.Deny, want.Deny)
	}
}

// TestConductorClaudeSettings_PermissionAskOffRemovesManagedEntries verifies
// turning permission_ask off over an existing file drops exactly the managed
// ask entries, keeps the user's own, drops an empty ask key, and that turning
// it back on restores the managed entries.
func TestConductorClaudeSettings_PermissionAskOffRemovesManagedEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		userAsk []string
	}{
		{name: "user entry kept", userAsk: []string{"Bash(git push *)"}},
		{name: "only managed entries", userAsk: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			claudeDir := filepath.Join(dir, ".claude")
			seed := `{"permissions":{"ask":["Bash(git push *)"]}}`
			if tc.userAsk == nil {
				seed = `{}`
			}
			if err := os.MkdirAll(claudeDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(seed), 0o644); err != nil {
				t.Fatalf("seed write: %v", err)
			}
			if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
				t.Fatalf("write with ask: %v", err)
			}
			before := loadConductorPerms(t, dir)

			if err := writeConductorClaudeSettingsAt(dir, false); err != nil {
				t.Fatalf("write without ask: %v", err)
			}
			got := loadConductorPerms(t, dir)
			if strings.Join(got.Ask, "\n") != strings.Join(tc.userAsk, "\n") {
				t.Errorf("ask = %v, want %v", got.Ask, tc.userAsk)
			}
			if tc.userAsk == nil {
				data, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
				if strings.Contains(string(data), `"ask"`) {
					t.Errorf("empty ask list must be dropped:\n%s", data)
				}
			}
			if strings.Join(got.Allow, "\n") != strings.Join(before.Allow, "\n") {
				t.Errorf("allow changed:\ngot  %v\nwant %v", got.Allow, before.Allow)
			}
			if strings.Join(got.Deny, "\n") != strings.Join(before.Deny, "\n") {
				t.Errorf("deny changed:\ngot  %v\nwant %v", got.Deny, before.Deny)
			}

			if err := writeConductorClaudeSettingsAt(dir, true); err != nil {
				t.Fatalf("write with ask again: %v", err)
			}
			if again := loadConductorPerms(t, dir); strings.Join(again.Ask, "\n") != strings.Join(before.Ask, "\n") {
				t.Errorf("turning permission_ask back on: ask = %v, want %v", again.Ask, before.Ask)
			}
		})
	}
}

// TestWriteConductorClaudeSettings_ReadsPermissionAskFromConfig verifies the
// named-conductor entry point honors [conductor] permission_ask from
// config.toml, and keeps the ask list when the key is absent.
func TestWriteConductorClaudeSettings_ReadsPermissionAskFromConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		wantAsk bool
	}{
		{name: "absent", config: "", wantAsk: true},
		{name: "true", config: "permission_ask = true\n", wantAsk: true},
		{name: "false", config: "permission_ask = false\n", wantAsk: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
			root := t.TempDir()
			cfgDir := filepath.Join(xdgConfigHome, "agent-deck")
			if err := os.MkdirAll(cfgDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			cfg := "[conductor]\ndir = \"" + root + "\"\n" + tc.config
			if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
				t.Fatalf("write config.toml: %v", err)
			}
			ClearUserConfigCache()
			t.Cleanup(ClearUserConfigCache)

			if err := WriteConductorClaudeSettings("alpha"); err != nil {
				t.Fatalf("WriteConductorClaudeSettings: %v", err)
			}
			perms := loadConductorPerms(t, filepath.Join(root, "alpha"))
			if got := permContains(perms.Ask, "Bash(agent-deck session send *)"); got != tc.wantAsk {
				t.Errorf("session send in ask = %v, want %v\nask=%v", got, tc.wantAsk, perms.Ask)
			}
		})
	}
}

// TestConductorClaudeSettings_PermissionAskOffIdenticalUserRule pins the
// documented edge case: setup does not track who wrote an ask entry, so with
// permission_ask = false a user rule identical to a managed one is removed,
// while a narrower user rule for the same command is kept.
func TestConductorClaudeSettings_PermissionAskOffIdenticalUserRule(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"permissions":{"ask":["Bash(agent-deck session send *)","Bash(agent-deck session send prod-*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := writeConductorClaudeSettingsAt(dir, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	perms := loadConductorPerms(t, dir)
	if permContains(perms.Ask, "Bash(agent-deck session send *)") {
		t.Errorf("identical-to-managed rule is treated as managed and must be removed\nask=%v", perms.Ask)
	}
	if !permContains(perms.Ask, "Bash(agent-deck session send prod-*)") {
		t.Errorf("narrower user rule must be kept\nask=%v", perms.Ask)
	}
}
