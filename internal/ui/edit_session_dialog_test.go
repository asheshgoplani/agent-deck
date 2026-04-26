package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func sampleInstance() *session.Instance {
	return &session.Instance{
		ID:        "sess-1",
		Title:     "my-session",
		Tool:      "claude",
		Command:   "claude",
		Color:     "#ff00aa",
		Notes:     "initial notes",
		ExtraArgs: []string{"--model", "opus"},
		GroupPath: "projects/devops",
	}
}

func TestEditSessionDialog_InitiallyHidden(t *testing.T) {
	d := NewEditSessionDialog()
	if d == nil {
		t.Fatal("NewEditSessionDialog returned nil")
	}
	if d.IsVisible() {
		t.Error("dialog should be hidden after construction")
	}
	if got := d.View(); got != "" {
		t.Errorf("View() should return empty string when hidden, got %q", got)
	}
}

// Without this, blank fields would wipe the session to zero on save.
func TestEditSessionDialog_ShowPopulatesFromInstance(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	if !d.IsVisible() {
		t.Fatal("dialog should be visible after Show()")
	}
	if d.SessionID() != inst.ID {
		t.Errorf("SessionID() = %q, want %q", d.SessionID(), inst.ID)
	}

	view := d.View()
	for _, want := range []string{"my-session", "--model opus", "Skip permissions", "Auto mode"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() should contain %q; got:\n%s", want, view)
		}
	}
}

// Header parity with NewDialog: the group name (last segment of GroupPath)
// must render with the "in group:" prefix so the editor pair feels like one
// family.
func TestEditSessionDialog_View_RendersGroupHeader(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	view := d.View()
	if !strings.Contains(view, "in group:") {
		t.Errorf("View() should contain group header 'in group:'; got:\n%s", view)
	}
	if !strings.Contains(view, "devops") {
		t.Errorf("View() should contain group name 'devops' (last segment of projects/devops); got:\n%s", view)
	}
}

// Empty GroupPath must fall back to DefaultGroupName ("My Sessions") so the
// header never reads "in group: " with a blank tail.
func TestEditSessionDialog_DefaultGroupName_ForEmptyPath(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	inst.GroupPath = ""
	d.Show(inst)

	if !strings.Contains(d.View(), session.DefaultGroupName) {
		t.Errorf("View() should fall back to DefaultGroupName for empty GroupPath; got:\n%s", d.View())
	}
}

func TestEditSessionDialog_Hide(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	d.Hide()
	if d.IsVisible() {
		t.Error("dialog should be hidden after Hide()")
	}
}

// Tab/Shift+Tab must wrap, not clamp — a clamp would strand the user
// on the first or last field.
func TestEditSessionDialog_TabCyclesFocus(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	total := len(d.fields)
	if total == 0 {
		t.Fatal("expected at least one field after Show()")
	}

	for i := 1; i <= total; i++ {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyTab})
		want := i % total
		if d.focusIndex != want {
			t.Fatalf("after %d Tab(s), focusIndex=%d, want %d", i, d.focusIndex, want)
		}
	}

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if d.focusIndex != total-1 {
		t.Fatalf("Shift+Tab should wrap to last (%d), got %d", total-1, d.focusIndex)
	}
}

// Pills cursor must wrap on ←/→ — same convention as NewDialog command
// picker. Without wrap, navigating off either end would stick.
func TestEditSessionDialog_PillsArrowsWrap(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	toolIdx := -1
	for i, f := range d.fields {
		if f.key == session.FieldTool {
			toolIdx = i
			break
		}
	}
	if toolIdx < 0 {
		t.Fatal("expected Tool field present")
	}
	d.focusIndex = toolIdx
	d.updateFocus()

	f := &d.fields[toolIdx]
	total := len(f.pillOptions)
	if total < 2 {
		t.Skip("preset list too short to test wrap")
	}

	start := f.pillCursor

	// Forward: cycle through total options and back.
	for i := 0; i < total; i++ {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRight})
	}
	if d.fields[toolIdx].pillCursor != start {
		t.Errorf("after %d rights cursor=%d, want back to %d (wrap)", total, d.fields[toolIdx].pillCursor, start)
	}

	// Backward from start: should wrap to last.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	want := (start - 1 + total) % total
	if d.fields[toolIdx].pillCursor != want {
		t.Errorf("Left wrap: cursor=%d, want %d", d.fields[toolIdx].pillCursor, want)
	}
}

func TestEditSessionDialog_TypingUpdatesFocusedInput(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	d.focusIndex = 0 // Title by construction
	d.updateFocus()

	title := d.fields[0].input.Value()
	for range title {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "renamed" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if got := d.fields[0].input.Value(); got != "renamed" {
		t.Errorf("Title input value = %q, want %q", got, "renamed")
	}
}

func TestEditSessionDialog_GetChanges_EmptyWhenUnchanged(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)
	changes := d.GetChanges(inst)
	if len(changes) != 0 {
		t.Errorf("GetChanges on untouched dialog = %d changes, want 0: %v", len(changes), changes)
	}
}

func TestEditSessionDialog_GetChanges_DetectsTextEdit(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.fields[i].input.SetValue("new-title")
			break
		}
	}

	changes := d.GetChanges(inst)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %v", len(changes), changes)
	}
	c := changes[0]
	if c.Field != session.FieldTitle || c.Value != "new-title" || !c.IsLive {
		t.Errorf("got %+v, want Field=title Value=new-title IsLive=true", c)
	}
}

// Pills must surface as a normal Change with the picked preset value, so
// home.go's existing SetField loop stays unaware of the pill widget.
func TestEditSessionDialog_GetChanges_DetectsPillsToolSwitch(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	var toolIdx = -1
	for i, f := range d.fields {
		if f.key == session.FieldTool {
			toolIdx = i
			break
		}
	}
	if toolIdx < 0 {
		t.Fatal("expected Tool field present")
	}

	target := ""
	targetIdx := -1
	for j, p := range d.fields[toolIdx].pillOptions {
		if p == "gemini" {
			target = p
			targetIdx = j
			break
		}
	}
	if target == "" {
		t.Skip("gemini not in preset list — skip")
	}
	d.fields[toolIdx].pillCursor = targetIdx

	changes := d.GetChanges(inst)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %v", len(changes), changes)
	}
	c := changes[0]
	if c.Field != session.FieldTool || c.Value != "gemini" || c.IsLive {
		t.Errorf("got %+v, want Field=tool Value=gemini IsLive=false (restart-required)", c)
	}
}

func TestEditSessionDialog_HasRestartRequiredChanges(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.fields[i].input.SetValue("renamed")
			break
		}
	}
	if d.HasRestartRequiredChanges(inst) {
		t.Error("Title-only edit must not flag restart-required")
	}

	for i := range d.fields {
		if d.fields[i].key == session.FieldExtraArgs {
			d.fields[i].input.SetValue("--model haiku")
			break
		}
	}
	if !d.HasRestartRequiredChanges(inst) {
		t.Error("ExtraArgs edit must flag restart-required")
	}
}

func TestEditSessionDialog_Validate_Title(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.fields[i].input.SetValue("   ")
			break
		}
	}
	if msg := d.Validate(); !strings.Contains(strings.ToLower(msg), "title") {
		t.Errorf("Validate() = %q, should mention empty title", msg)
	}
}

// instanceWithExplicitClaudeFlags pre-seeds ToolOptionsJSON so the test
// doesn't depend on the dev's ~/.agent-deck/config.toml dangerous-mode
// default (the dialog falls back to config when ToolOptionsJSON is empty
// — see readClaudeFlags). Without this, a developer running the suite
// with `dangerous_mode = true` in their personal config would see the
// SkipPermissions checkbox start at `[x]`, and a "toggle from off" test
// would assert the wrong direction.
func instanceWithExplicitClaudeFlags(skip, auto bool) *session.Instance {
	inst := sampleInstance()
	if err := inst.SetClaudeOptions(&session.ClaudeOptions{
		SkipPermissions: skip,
		AutoMode:        auto,
	}); err != nil {
		panic(err)
	}
	return inst
}

// SkipPermissions / AutoMode space-toggle must round-trip into a valid
// boolean Change with IsLive=false. Restart-required because the flags
// land in claude argv at launch time, not via a runtime tmux command.
func TestEditSessionDialog_GetChanges_SkipPermissionsToggle(t *testing.T) {
	d := NewEditSessionDialog()
	inst := instanceWithExplicitClaudeFlags(false, false)
	d.Show(inst)

	idx := -1
	for i, f := range d.fields {
		if f.key == session.FieldSkipPermissions {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("expected SkipPermissions field on a claude session")
	}
	d.focusIndex = idx
	d.updateFocus()
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	changes := d.GetChanges(inst)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %v", len(changes), changes)
	}
	c := changes[0]
	if c.Field != session.FieldSkipPermissions || c.Value != "true" || c.IsLive {
		t.Errorf("got %+v, want Field=skip-permissions Value=true IsLive=false", c)
	}
}

func TestEditSessionDialog_GetChanges_AutoModeToggle(t *testing.T) {
	d := NewEditSessionDialog()
	inst := instanceWithExplicitClaudeFlags(false, false)
	d.Show(inst)

	idx := -1
	for i, f := range d.fields {
		if f.key == session.FieldAutoMode {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("expected AutoMode field on a claude session")
	}
	d.fields[idx].checked = true

	changes := d.GetChanges(inst)
	if len(changes) != 1 || changes[0].Field != session.FieldAutoMode ||
		changes[0].Value != "true" || changes[0].IsLive {
		t.Errorf("got %v; want one auto-mode=true restart-required Change", changes)
	}
}

// readClaudeFlags falls back to UserConfig defaults when ToolOptionsJSON
// is empty — so a session created before any options panel touched it
// (the common case in the wild) shows the actual effective flag state in
// the dialog rather than `[ ]` for everything. Dev-config-independent:
// pre-seeded ToolOptionsJSON shadows the fallback.
func TestEditSessionDialog_PrePopulatedToolOptions_ChecksReflectJSON(t *testing.T) {
	d := NewEditSessionDialog()
	inst := instanceWithExplicitClaudeFlags(true, false)
	d.Show(inst)

	for _, f := range d.fields {
		if f.key == session.FieldSkipPermissions && !f.checked {
			t.Errorf("Skip permissions should reflect ToolOptionsJSON skip=true; got [ ]")
		}
		if f.key == session.FieldAutoMode && f.checked {
			t.Errorf("Auto mode should reflect ToolOptionsJSON auto=false; got [x]")
		}
	}
}

// Shell sessions don't surface skip/auto checkboxes — they're claude-only
// flags that SetField would reject anyway.
func TestEditSessionDialog_NoClaudeFlagsForShellTool(t *testing.T) {
	inst := sampleInstance()
	inst.Tool = "shell"
	d := NewEditSessionDialog()
	d.Show(inst)

	for _, f := range d.fields {
		if f.key == session.FieldSkipPermissions || f.key == session.FieldAutoMode {
			t.Errorf("field %q should be hidden for non-claude tool", f.key)
		}
	}
}

// Hiding ExtraArgs for shell/gemini sessions is friendlier UX than letting
// the user submit and watch SetField reject "claude only".
func TestEditSessionDialog_ExtraArgsHiddenForNonClaudeTool(t *testing.T) {
	inst := sampleInstance()
	inst.Tool = "shell"
	inst.ExtraArgs = nil

	d := NewEditSessionDialog()
	d.Show(inst)

	for _, f := range d.fields {
		if f.key == session.FieldExtraArgs {
			t.Errorf("ExtraArgs should be hidden for non-claude tool %q", inst.Tool)
		}
	}
}

// Stale custom-tool regression: opening the dialog on a session whose Tool
// isn't in buildPresetCommands() (custom tool removed from config, or
// claude-compatible variant like "claude-trace") must NOT silently land the
// pill cursor on shell. If it did, pressing Enter without touching pills
// would emit Change{FieldTool, ""} and rewrite the session to shell on
// next restart. Reviewer-flagged MEDIUM.
func TestEditSessionDialog_GetChanges_NoSpuriousToolWipeForStaleCustom(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	inst.Tool = "claude-trace" // not in standard presets, IsClaudeCompatible
	d.Show(inst)

	// User does nothing — just opens and saves.
	if changes := d.GetChanges(inst); len(changes) != 0 {
		t.Errorf("opening dialog on stale/unknown tool %q must produce zero changes; got %v", inst.Tool, changes)
	}

	// And the pill cursor must point at the stale tool itself, not slot 0.
	for _, f := range d.fields {
		if f.key != session.FieldTool {
			continue
		}
		if f.pillCursor < 0 || f.pillCursor >= len(f.pillOptions) {
			t.Fatalf("pillCursor=%d out of range for %d options", f.pillCursor, len(f.pillOptions))
		}
		if f.pillOptions[f.pillCursor] != "claude-trace" {
			t.Errorf("pill cursor on stale tool: pillOptions[%d]=%q, want %q",
				f.pillCursor, f.pillOptions[f.pillCursor], "claude-trace")
		}
		return
	}
	t.Fatal("Tool field not found")
}

// Title-locked / NoTransitionNotify / Wrapper / Channels are deliberately
// CLI-only. Pinning their absence here prevents a future "let's surface
// everything" regression that would re-bloat the dialog.
func TestEditSessionDialog_DropsRareFields(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	rare := map[string]bool{
		session.FieldTitleLocked:        true,
		session.FieldNoTransitionNotify: true,
		session.FieldWrapper:            true,
		session.FieldChannels:           true,
	}
	for _, f := range d.fields {
		if rare[f.key] {
			t.Errorf("field %q must stay CLI-only — drop from dialog", f.key)
		}
	}
}

// Esc/Enter must reach the outer router as commit/cancel intent — the
// dialog must not absorb them.
func TestEditSessionDialog_EscReturnsWithoutSwallow(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Errorf("Esc should not emit a tea.Cmd; got %v", cmd)
	}
	if len(d.GetChanges(inst)) != 0 {
		t.Error("Esc should not mutate dialog state")
	}
}

func TestEditSessionDialog_EnterReturnsWithoutSwallow(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	d.Show(inst)

	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("Enter should not emit a tea.Cmd; got %v", cmd)
	}
	if len(d.GetChanges(inst)) != 0 {
		t.Error("Enter alone (no edits) should leave changes empty")
	}
}

func TestEditSessionDialog_SetErrorRendersInline(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	d.SetError("channels only supported for claude sessions")

	if !strings.Contains(d.View(), "channels only supported") {
		t.Error("View() should display the error message set via SetError")
	}

	d.ClearError()
	if strings.Contains(d.View(), "channels only supported") {
		t.Error("ClearError should remove the inline error")
	}
}

// Space must reach a focused text input as a literal — titles with spaces
// have to be typable. (No checkboxes survive in the slim set, so space is
// no longer a checkbox-toggle keybind.)
func TestEditSessionDialog_SpaceInsideTextInput(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())

	for i := range d.fields {
		if d.fields[i].key == session.FieldTitle {
			d.focusIndex = i
			break
		}
	}
	d.updateFocus()

	title := d.fields[d.focusIndex].input.Value()
	for range title {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "a b" {
		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if got := d.fields[d.focusIndex].input.Value(); got != "a b" {
		t.Errorf("text input should accept literal space; got %q, want %q", got, "a b")
	}
}

// A stale error from a prior Show() must not bleed into a new session.
func TestEditSessionDialog_ReShowResetsError(t *testing.T) {
	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	d.SetError("previous boom")

	other := sampleInstance()
	other.ID = "sess-2"
	other.Title = "other"
	d.Show(other)

	if strings.Contains(d.View(), "previous boom") {
		t.Error("Show() should clear any inline error from a prior Show()")
	}
}
