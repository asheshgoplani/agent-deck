package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Session lifecycle matrix for custom [tools.*] conversation ids
// (feat/persist-custom-tool-session-id). Sandboxed: no tmux required.
//
// Matrix rows (PASS expected):
//  1. set tool-session-id → Save → Load → no tmux → CanRestartGeneric + resume cmd
//  2. set then clear to "" → no resume after load
//  3. clearSessionBindingForFreshStart drops id without resume_flag on tool
//  4. RestartPolicyFor(tool-session-id) == RestartRequired
//  5. unknown tool (GetToolDef nil) — no panic; no false resume
//  6. builtin "claude" with GenericSessionID set — CanRestartGeneric false
//  7. shell + GenericSessionID, no resume_flag — no resume
//  8. after load, DisplaySessionID for custom tool returns generic id
//  9. two instances same project path, different ids — independent after load

func TestSessionLifecycleMatrix_CustomToolSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Cleanup(ClearUserConfigCache)
	writeResumeToolConfig(t, home, "matrix-tool", "--resume")

	// --- 4. Restart policy ---
	if got := RestartPolicyFor(FieldToolSessionID); got != FieldRestartRequired {
		t.Errorf("[4] RestartPolicyFor(tool-session-id) = %v, want FieldRestartRequired", got)
	}

	storage := newTestStorage(t)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}

	// Stable IDs: NewInstance generates random IDs; Title is only the display name.
	// --- 1. set → save → load → cold (no tmux) resume ---
	a := NewInstance("matrix-a", proj)
	a.ID = "matrix-a"
	a.Tool = "matrix-tool"
	a.Command = "matrix-tool"
	old, _, err := SetField(a, FieldToolSessionID, "sid-after-reboot", nil)
	if err != nil {
		t.Fatalf("[1] SetField: %v", err)
	}
	if old != "" {
		t.Fatalf("[1] old value want empty, got %q", old)
	}
	if a.GenericSessionID != "sid-after-reboot" {
		t.Fatalf("[1] GenericSessionID = %q", a.GenericSessionID)
	}
	if a.GenericDetectedAt.IsZero() {
		t.Fatal("[1] GenericDetectedAt should be stamped")
	}

	// --- 9. second instance same path, different id ---
	b := NewInstance("matrix-b", proj)
	b.ID = "matrix-b"
	b.Tool = "matrix-tool"
	b.Command = "matrix-tool"
	if _, _, err := SetField(b, FieldToolSessionID, "sid-other-instance", nil); err != nil {
		t.Fatalf("[9] SetField b: %v", err)
	}

	// --- 2 companion: instance that will be cleared before save ---
	c := NewInstance("matrix-c", proj)
	c.ID = "matrix-c"
	c.Tool = "matrix-tool"
	c.Command = "matrix-tool"
	if _, _, err := SetField(c, FieldToolSessionID, "sid-to-clear", nil); err != nil {
		t.Fatalf("[2] SetField c: %v", err)
	}
	if _, _, err := SetField(c, FieldToolSessionID, "", nil); err != nil {
		t.Fatalf("[2] clear SetField: %v", err)
	}
	if c.GenericSessionID != "" || !c.GenericDetectedAt.IsZero() {
		t.Fatalf("[2] clear left id=%q detected=%v", c.GenericSessionID, c.GenericDetectedAt)
	}

	// --- 5. unknown tool ---
	unk := NewInstance("matrix-unk", proj)
	unk.ID = "matrix-unk"
	unk.Tool = "totally-unknown-tool-xyz"
	unk.Command = "totally-unknown-tool-xyz"
	unk.GenericSessionID = "should-not-resume-unknown"
	unk.GenericDetectedAt = time.Now()

	// --- 6. builtin claude with accidental GenericSessionID ---
	claude := NewInstance("matrix-claude", proj)
	claude.ID = "matrix-claude"
	claude.Tool = "claude"
	claude.Command = "claude"
	claude.ClaudeSessionID = "claude-real-id"
	claude.GenericSessionID = "accidental-generic-on-claude"

	// --- 7. shell + GenericSessionID (builtin → GetToolDef nil) ---
	shellInst := NewInstance("matrix-shell", proj)
	shellInst.ID = "matrix-shell"
	shellInst.Tool = "shell"
	shellInst.Command = "bash"
	shellInst.GenericSessionID = "shell-should-not-resume"

	// --- 7b. custom tool without resume_flag ---
	// Single config.toml must list every custom tool used below.
	writeMultiToolConfig(t, home, map[string]string{
		"matrix-tool":    "--resume",
		"no-resume-tool": "", // empty = no resume_flag key
	})

	noResume := NewInstance("matrix-no-resume", proj)
	noResume.ID = "matrix-no-resume"
	noResume.Tool = "no-resume-tool"
	noResume.Command = "no-resume-tool"
	noResume.GenericSessionID = "present-but-no-flag"

	instances := []*Instance{a, b, c, unk, claude, shellInst, noResume}
	tree := NewGroupTreeWithGroups(instances, nil)
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	byID := map[string]*Instance{}
	for _, inst := range loaded {
		inst.tmuxSession = nil // reboot: no tmux
		byID[inst.ID] = inst
	}

	// --- 1 post-load ---
	la := byID["matrix-a"]
	if la == nil {
		t.Fatalf("[1] matrix-a missing after load; have %v", instanceIDs(byID))
	}
	if la.GenericSessionID != "sid-after-reboot" {
		t.Errorf("[1] loaded GenericSessionID = %q", la.GenericSessionID)
	}
	if !la.CanRestartGeneric() {
		t.Error("[1] CanRestartGeneric want true after cold load")
	}
	cmdA := la.buildGenericCommand(la.Command)
	if !strings.Contains(cmdA, "--resume") || !strings.Contains(cmdA, "sid-after-reboot") {
		t.Errorf("[1] buildGenericCommand = %q, want --resume sid-after-reboot", cmdA)
	}

	// --- 8. DisplaySessionID after load ---
	if got := la.DisplaySessionID(); got != "sid-after-reboot" {
		t.Errorf("[8] DisplaySessionID(custom) = %q, want sid-after-reboot", got)
	}

	// --- 9 independence ---
	lb := byID["matrix-b"]
	if lb == nil {
		t.Fatal("[9] matrix-b missing")
	}
	if lb.GenericSessionID != "sid-other-instance" {
		t.Errorf("[9] b GenericSessionID = %q", lb.GenericSessionID)
	}
	if la.GenericSessionID == lb.GenericSessionID {
		t.Error("[9] instances must keep independent session ids")
	}
	if la.ProjectPath != lb.ProjectPath {
		t.Errorf("[9] same project path expected, got %q vs %q", la.ProjectPath, lb.ProjectPath)
	}
	cmdB := lb.buildGenericCommand(lb.Command)
	if !strings.Contains(cmdB, "sid-other-instance") || strings.Contains(cmdB, "sid-after-reboot") {
		t.Errorf("[9] b command leaked a's id: %q", cmdB)
	}

	// --- 2 post-load clear ---
	lc := byID["matrix-c"]
	if lc == nil {
		t.Fatal("[2] matrix-c missing")
	}
	if lc.GenericSessionID != "" {
		t.Errorf("[2] cleared id reappeared after load: %q", lc.GenericSessionID)
	}
	if lc.CanRestartGeneric() {
		t.Error("[2] CanRestartGeneric must be false after clear")
	}
	cmdC := lc.buildGenericCommand(lc.Command)
	if strings.Contains(cmdC, "--resume") {
		t.Errorf("[2] cleared instance must not resume: %q", cmdC)
	}

	// --- 5 unknown tool ---
	lu := byID["matrix-unk"]
	if lu == nil {
		t.Fatal("[5] unknown missing")
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("[5] panic on unknown tool: %v", r)
			}
		}()
		if GetToolDef(lu.Tool) != nil {
			t.Errorf("[5] GetToolDef(%q) want nil", lu.Tool)
		}
		if lu.CanRestartGeneric() {
			t.Error("[5] CanRestartGeneric must be false for unknown tool")
		}
		cmd := lu.buildGenericCommand(lu.Command)
		if strings.Contains(cmd, "--resume") || strings.Contains(cmd, "should-not-resume-unknown") {
			t.Errorf("[5] unknown tool must not inject resume: %q", cmd)
		}
		// Display still surfaces the stored generic id (preview); resume does not.
		if got := lu.DisplaySessionID(); got != "should-not-resume-unknown" {
			t.Errorf("[5] DisplaySessionID = %q, want stored generic id", got)
		}
	}()

	// --- 6 builtin claude ---
	lcl := byID["matrix-claude"]
	if lcl == nil {
		t.Fatal("[6] claude missing")
	}
	if GetToolDef("claude") != nil {
		t.Error("[6] GetToolDef(claude) must be nil (builtins not custom)")
	}
	if lcl.CanRestartGeneric() {
		t.Error("[6] CanRestartGeneric must be false for builtin claude")
	}
	// Display uses ClaudeSessionID, not the accidental GenericSessionID.
	if got := lcl.DisplaySessionID(); got != "claude-real-id" {
		t.Errorf("[6] DisplaySessionID = %q, want claude-real-id", got)
	}
	// buildGenericCommand on a claude-named tool without custom def is defensive only.
	cmdClaude := lcl.buildGenericCommand("claude")
	if strings.Contains(cmdClaude, "accidental-generic-on-claude") {
		t.Errorf("[6] must not resume accidental generic id: %q", cmdClaude)
	}

	// --- 7 shell ---
	ls := byID["matrix-shell"]
	if ls == nil {
		t.Fatal("[7] shell missing")
	}
	if GetToolDef("shell") != nil {
		t.Error("[7] GetToolDef(shell) must be nil")
	}
	if ls.CanRestartGeneric() {
		t.Error("[7] shell CanRestartGeneric must be false")
	}
	cmdShell := ls.buildGenericCommand(ls.Command)
	if strings.Contains(cmdShell, "--resume") || strings.Contains(cmdShell, "shell-should-not-resume") {
		t.Errorf("[7] shell must not resume: %q", cmdShell)
	}

	// --- 7b custom without resume_flag ---
	lnr := byID["matrix-no-resume"]
	if lnr == nil {
		t.Fatal("[7b] no-resume missing")
	}
	def := GetToolDef("no-resume-tool")
	if def == nil {
		t.Fatal("[7b] no-resume-tool should be registered as custom tool")
	}
	if def.ResumeFlag != "" {
		t.Fatalf("[7b] ResumeFlag = %q, want empty", def.ResumeFlag)
	}
	if lnr.CanRestartGeneric() {
		t.Error("[7b] CanRestartGeneric must be false without resume_flag")
	}
	cmdNR := lnr.buildGenericCommand(lnr.Command)
	if strings.Contains(cmdNR, "--resume") || strings.Contains(cmdNR, "present-but-no-flag") {
		t.Errorf("[7b] no resume_flag must not inject id: %q", cmdNR)
	}

	// --- 3. clearSessionBindingForFreshStart without resume_flag ---
	fresh := &Instance{
		ID:                "fresh-clear",
		Tool:              "no-resume-tool",
		GenericSessionID:  "drop-me-no-flag",
		GenericDetectedAt: time.Now(),
	}
	fresh.clearSessionBindingForFreshStart()
	if fresh.GenericSessionID != "" || !fresh.GenericDetectedAt.IsZero() {
		t.Errorf("[3] clearSessionBindingForFreshStart left id=%q at=%v",
			fresh.GenericSessionID, fresh.GenericDetectedAt)
	}
	// Also on a tool with resume_flag
	fresh2 := &Instance{
		ID:                "fresh-clear-2",
		Tool:              "matrix-tool",
		GenericSessionID:  "drop-me-with-flag",
		GenericDetectedAt: time.Now(),
	}
	fresh2.clearSessionBindingForFreshStart()
	if fresh2.GenericSessionID != "" {
		t.Errorf("[3] with resume_flag still left id %q", fresh2.GenericSessionID)
	}
	if fresh2.CanRestartGeneric() {
		t.Error("[3] after clear, CanRestartGeneric must be false")
	}
}

// TestClearToolSessionID_SurvivesStickyMergeOnSave covers the regression where
// WriteGenericSessionIDToToolData deleted keys on clear (omission), and
// MergeToolDataExtras then re-hydrated the previous generic_session_id from the
// existing row — undoing `session set tool-session-id ""` after SaveWithGroups.
func TestClearToolSessionID_SurvivesStickyMergeOnSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("sticky-clear", "/tmp/sticky-clear")
	inst.ID = "sticky-clear"
	inst.Tool = "shell"
	inst.GenericSessionID = "must-not-resurrect"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	// Intentional clear (CLI/TUI SetField path) then full-table save.
	if _, _, err := SetField(inst, FieldToolSessionID, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len=%d", len(loaded))
	}
	if got := loaded[0].GenericSessionID; got != "" {
		t.Fatalf("cleared id resurrected via sticky merge: %q", got)
	}
	if !loaded[0].GenericDetectedAt.IsZero() {
		t.Fatalf("detected_at should be zero after clear, got %v", loaded[0].GenericDetectedAt)
	}
}

// TestClearToolSessionID_FlagConsumedAfterSave prevents a long-lived Instance
// (TUI) that once cleared tool-session-id from re-emitting explicit empty on
// every later full save — which would wipe a concurrent re-bind written via
// WriteGenericSessionBinding (live capture or another process).
func TestClearToolSessionID_FlagConsumedAfterSave(t *testing.T) {
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
		t.Fatal("genericSessionIDCleared must be consumed after successful SaveWithGroups")
	}

	// Concurrent re-bind (live capture / other process) after our clear saved.
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "rebinding-id", inst.Tool, inst.Command, LocationOf(inst).String(), time.Now()); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "rebinding-id" {
		t.Fatalf("rebind failed: %q", loaded[0].GenericSessionID)
	}

	// Unrelated full-table save from the long-lived in-memory Instance that
	// still has empty GenericSessionID (never reloaded). Must NOT wipe rebind.
	inst.Title = "renamed-after-clear"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded2[0].GenericSessionID; got != "rebinding-id" {
		t.Fatalf("stale clear flag wiped concurrent rebind: got %q want rebinding-id", got)
	}
	if loaded2[0].Title != "renamed-after-clear" {
		t.Fatalf("title=%q", loaded2[0].Title)
	}
}

func TestSetField_ToolSessionID_TrimsWhitespace(t *testing.T) {
	inst := &Instance{ID: "trim", Tool: "shell"}
	if _, _, err := SetField(inst, FieldToolSessionID, "  sid-trim  ", nil); err != nil {
		t.Fatal(err)
	}
	if inst.GenericSessionID != "sid-trim" {
		t.Fatalf("want trimmed sid-trim, got %q", inst.GenericSessionID)
	}
	if _, _, err := SetField(inst, FieldToolSessionID, "   ", nil); err != nil {
		t.Fatal(err)
	}
	if inst.GenericSessionID != "" {
		t.Fatalf("whitespace-only should clear, got %q", inst.GenericSessionID)
	}
}

func TestDisplaySessionID_CustomToolGeneric(t *testing.T) {
	inst := &Instance{
		Tool:             "some-custom",
		GenericSessionID: "gen-display-1",
	}
	if got := inst.DisplaySessionID(); got != "gen-display-1" {
		t.Fatalf("DisplaySessionID = %q, want gen-display-1", got)
	}
}

func instanceIDs(m map[string]*Instance) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// writeMultiToolConfig writes [tools.<name>] blocks. Non-empty resumeFlag
// sets resume_flag; empty omits the key (tool registered, no resume).
func writeMultiToolConfig(t *testing.T, home string, tools map[string]string) {
	t.Helper()
	var b strings.Builder
	for name, resumeFlag := range tools {
		b.WriteString("\n[tools.")
		b.WriteString(name)
		b.WriteString("]\ncommand = \"")
		b.WriteString(name)
		b.WriteString("\"\n")
		if resumeFlag != "" {
			b.WriteString("resume_flag = \"")
			b.WriteString(resumeFlag)
			b.WriteString("\"\n")
		}
	}
	content := b.String()
	for _, dir := range []string{
		filepath.Join(home, ".config", "agent-deck"),
		filepath.Join(home, ".agent-deck"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ClearUserConfigCache()
}
