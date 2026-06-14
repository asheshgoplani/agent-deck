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
	if err := writeConductorClaudeSettingsAt(dir); err != nil {
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
	if err := writeConductorClaudeSettingsAt(dir); err != nil {
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
	if err := writeConductorClaudeSettingsAt(dir); err != nil {
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

	if err := writeConductorClaudeSettingsAt(dir); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeConductorClaudeSettingsAt(dir); err != nil {
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

	if err := writeConductorClaudeSettingsAt(dir); err != nil {
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
