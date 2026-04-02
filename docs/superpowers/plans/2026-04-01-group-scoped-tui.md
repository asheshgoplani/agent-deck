# Group-Scoped TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `--group` / `-g` CLI flag to launch the AgentDeck TUI locked to a specific group, showing only that group's sessions and children.

**Architecture:** Setter method on `Home` struct (`SetGroupScope`), called from `main.go` after construction. Group scope filtering in `rebuildFlatItems` runs after existing `statusFilter` logic. Hotkey handlers check `groupScope` to restrict navigation/mutation to scoped group hierarchy.

**Tech Stack:** Go, Bubble Tea TUI framework, existing session/groups package

---

### Task 1: Extract `--group` / `-g` flag in `main.go`

**Files:**
- Modify: `cmd/agent-deck/main.go:594-625` (add `extractGroupFlag` near `extractProfileFlag`)
- Modify: `cmd/agent-deck/main.go:185-190` (call it in `main()`)
- Test: `cmd/agent-deck/main_test.go`

- [ ] **Step 1: Write the failing test for flag extraction**

In `cmd/agent-deck/main_test.go`, add:

```go
func TestExtractGroupFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantGroup string
		wantRest  []string
	}{
		{
			name:      "no flag",
			args:      []string{"list"},
			wantGroup: "",
			wantRest:  []string{"list"},
		},
		{
			name:      "long flag with equals",
			args:      []string{"--group=work", "list"},
			wantGroup: "work",
			wantRest:  []string{"list"},
		},
		{
			name:      "long flag with space",
			args:      []string{"--group", "work", "list"},
			wantGroup: "work",
			wantRest:  []string{"list"},
		},
		{
			name:      "short flag with space",
			args:      []string{"-g", "work"},
			wantGroup: "work",
			wantRest:  nil,
		},
		{
			name:      "combined with profile",
			args:      []string{"-p", "dev", "-g", "work"},
			wantGroup: "work",
			wantRest:  []string{"-p", "dev"},
		},
		{
			name:      "subgroup path",
			args:      []string{"--group", "work/frontend"},
			wantGroup: "work/frontend",
			wantRest:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, rest := extractGroupFlag(tt.args)
			if group != tt.wantGroup {
				t.Errorf("group = %q, want %q", group, tt.wantGroup)
			}
			if len(rest) == 0 && len(tt.wantRest) == 0 {
				return
			}
			if len(rest) != len(tt.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tt.wantRest)
				return
			}
			for i := range rest {
				if rest[i] != tt.wantRest[i] {
					t.Errorf("rest[%d] = %q, want %q", i, rest[i], tt.wantRest[i])
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/agent-deck/ -run TestExtractGroupFlag -v`
Expected: FAIL with "undefined: extractGroupFlag"

- [ ] **Step 3: Implement `extractGroupFlag`**

In `cmd/agent-deck/main.go`, add after `extractProfileFlag` (after line 625):

```go
// extractGroupFlag extracts -g or --group from args, returning the group and remaining args.
// This only applies to the TUI launch path — subcommands like add/launch have their own -g flag.
func extractGroupFlag(args []string) (string, []string) {
	var group string
	var remaining []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if strings.HasPrefix(arg, "-g=") {
			group = strings.TrimPrefix(arg, "-g=")
			continue
		}
		if strings.HasPrefix(arg, "--group=") {
			group = strings.TrimPrefix(arg, "--group=")
			continue
		}

		if arg == "-g" || arg == "--group" {
			if i+1 < len(args) {
				group = args[i+1]
				i++
				continue
			}
		}

		remaining = append(remaining, arg)
	}

	return group, remaining
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/agent-deck/ -run TestExtractGroupFlag -v`
Expected: PASS

- [ ] **Step 5: Wire flag extraction in `main()`**

In `cmd/agent-deck/main.go`, modify the `main()` function. After line 187 (`profile, args := extractProfileFlag(os.Args[1:])`), add:

```go
	var groupScope string
	groupScope, args = extractGroupFlag(args)
```

This extracts `--group` / `-g` before subcommand dispatch, so subcommands never see it. The `groupScope` variable is used later when starting the TUI.

- [ ] **Step 6: Validate group and call setter before TUI launch**

In `cmd/agent-deck/main.go`, after line 445 (`homeModel := ui.NewHomeWithProfileAndMode(profile)`), add:

```go
	// Apply group scope if specified via --group / -g flag
	if groupScope != "" {
		homeModel.SetGroupScope(groupScope)
	}
```

Note: `SetGroupScope` doesn't exist yet — it will be created in Task 2. This code compiles but the method call will fail until Task 2 is done.

- [ ] **Step 7: Commit**

```bash
git add cmd/agent-deck/main.go cmd/agent-deck/main_test.go
git commit -m "feat: extract --group/-g CLI flag for TUI group scope"
```

---

### Task 2: Add `groupScope` field and `SetGroupScope` setter to `Home`

**Files:**
- Modify: `internal/ui/home.go:207` (add field after `statusFilter`)
- Modify: `internal/ui/home.go:926` (add setter after `SetCostBudget`)
- Test: `internal/ui/home_test.go`

- [ ] **Step 1: Write the failing test for SetGroupScope**

In `internal/ui/home_test.go`, add:

```go
func TestSetGroupScope(t *testing.T) {
	h := &Home{}

	h.SetGroupScope("work")
	if h.groupScope != "work" {
		t.Errorf("groupScope = %q, want %q", h.groupScope, "work")
	}

	h.SetGroupScope("work/frontend")
	if h.groupScope != "work/frontend" {
		t.Errorf("groupScope = %q, want %q", h.groupScope, "work/frontend")
	}
}

func TestGroupScopeNormalization(t *testing.T) {
	h := &Home{}

	h.SetGroupScope("Work")
	if h.groupScope != "work" {
		t.Errorf("groupScope = %q, want %q", h.groupScope, "work")
	}

	h.SetGroupScope("My Projects")
	if h.groupScope != "my-projects" {
		t.Errorf("groupScope = %q, want %q", h.groupScope, "my-projects")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestSetGroupScope -v`
Expected: FAIL with "h.groupScope undefined"

- [ ] **Step 3: Add field and setter**

In `internal/ui/home.go`, add field after `statusFilter` (line 207):

```go
	statusFilter   session.Status // Filter sessions by status ("" = all, or specific status)
	groupScope     string         // When set, TUI is locked to this group + children (immutable after set)
```

Add setter method after `SetCostBudget` (after line 926):

```go
// SetGroupScope locks the TUI to show only the specified group and its children.
// The scope is immutable once set — there is no way to clear it from the TUI.
// Path is normalized to lowercase with spaces replaced by hyphens.
func (h *Home) SetGroupScope(path string) {
	h.groupScope = strings.ToLower(strings.ReplaceAll(path, " ", "-"))
}
```

Ensure `strings` is already imported (it is — used extensively in home.go).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -run TestSetGroupScope -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ui/home.go internal/ui/home_test.go
git commit -m "feat: add groupScope field and SetGroupScope setter to Home"
```

---

### Task 3: Filter `rebuildFlatItems` by group scope

**Files:**
- Modify: `internal/ui/home.go:1155-1157` (add group scope filter after statusFilter block)
- Test: `internal/ui/home_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/ui/home_test.go`, add:

```go
func TestRebuildFlatItemsGroupScope(t *testing.T) {
	h := &Home{}
	h.groupScope = "work"

	// Build a minimal group tree with sessions in different groups
	instances := []*session.Instance{
		session.NewInstanceWithGroup("s1", "/tmp/p1", "work"),
		session.NewInstanceWithGroup("s2", "/tmp/p2", "work/frontend"),
		session.NewInstanceWithGroup("s3", "/tmp/p3", "personal"),
	}
	h.groupTree = session.NewGroupTree(instances)
	h.windowsCollapsed = make(map[string]bool)

	h.rebuildFlatItems()

	// Should only contain items from "work" and "work/frontend", not "personal"
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			if item.Session.GroupPath == "personal" {
				t.Errorf("found session in 'personal' group, expected only work and children")
			}
		}
		if item.Type == session.ItemTypeGroup && item.Path == "personal" {
			t.Errorf("found 'personal' group header, expected only work and children")
		}
	}

	// Verify work sessions are present
	found := map[string]bool{}
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			found[item.Session.Title] = true
		}
	}
	if !found["s1"] {
		t.Error("missing session s1 (work group)")
	}
	if !found["s2"] {
		t.Error("missing session s2 (work/frontend group)")
	}
}

func TestRebuildFlatItemsGroupScopeComposesWithStatusFilter(t *testing.T) {
	h := &Home{}
	h.groupScope = "work"
	h.statusFilter = session.StatusRunning

	instances := []*session.Instance{
		session.NewInstanceWithGroup("running-work", "/tmp/p1", "work"),
		session.NewInstanceWithGroup("idle-work", "/tmp/p2", "work"),
		session.NewInstanceWithGroup("running-personal", "/tmp/p3", "personal"),
	}
	// Set statuses
	instances[0].Status = session.StatusRunning
	instances[1].Status = session.StatusIdle
	instances[2].Status = session.StatusRunning

	h.groupTree = session.NewGroupTree(instances)
	h.windowsCollapsed = make(map[string]bool)

	h.rebuildFlatItems()

	// Should only contain running sessions from work group
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			if item.Session.GroupPath == "personal" {
				t.Errorf("found personal session, expected only work group")
			}
			if item.Session.Status != session.StatusRunning {
				t.Errorf("found non-running session %q, expected only running", item.Session.Title)
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestRebuildFlatItemsGroupScope -v`
Expected: FAIL — "personal" group items will be present because no scope filtering exists yet

- [ ] **Step 3: Implement group scope filtering in `rebuildFlatItems`**

In `internal/ui/home.go`, in the `rebuildFlatItems` function, replace the block at lines 1155-1157:

```go
	} else {
		h.flatItems = allItems
	}
```

with:

```go
	} else {
		h.flatItems = allItems
	}

	// Apply group scope filter (composes with status filter above)
	if h.groupScope != "" {
		scoped := make([]session.Item, 0, len(h.flatItems))
		for _, item := range h.flatItems {
			if item.Path == h.groupScope || strings.HasPrefix(item.Path, h.groupScope+"/") {
				scoped = append(scoped, item)
			}
		}
		h.flatItems = scoped
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -run TestRebuildFlatItemsGroupScope -v`
Expected: PASS

- [ ] **Step 5: Verify existing tests still pass**

Run: `go test ./internal/ui/ -v -count=1`
Expected: All existing tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/ui/home.go internal/ui/home_test.go
git commit -m "feat: filter rebuildFlatItems by group scope"
```

---

### Task 4: Validate group exists at startup

**Files:**
- Modify: `cmd/agent-deck/main.go` (validate after homeModel construction)

- [ ] **Step 1: Write the failing test**

In `cmd/agent-deck/main_test.go`, add:

```go
func TestGroupScopeValidation(t *testing.T) {
	// Test that normalizeGroupPath produces the expected path format
	// (used to validate user input matches stored paths)
	tests := []struct {
		input string
		want  string
	}{
		{"work", "work"},
		{"Work", "work"},
		{"My Projects", "my-projects"},
		{"work/frontend", "work/frontend"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeGroupPath(tt.input)
			if got != tt.want {
				t.Errorf("normalizeGroupPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./cmd/agent-deck/ -run TestGroupScopeValidation -v`
Expected: PASS (normalizeGroupPath already exists)

- [ ] **Step 3: Add group validation in `main()`**

In `cmd/agent-deck/main.go`, after the `homeModel.SetGroupScope(groupScope)` call added in Task 1 Step 6, expand the block to:

```go
	// Apply group scope if specified via --group / -g flag
	if groupScope != "" {
		normalizedGroup := normalizeGroupPath(groupScope)
		// Validate group exists by loading current sessions
		if storage, err := session.NewStorageWithProfile(profile); err == nil {
			if _, groups, err := storage.LoadWithGroups(); err == nil {
				groupTree := session.NewGroupTreeWithGroups(nil, groups)
				if _, exists := groupTree.Groups[normalizedGroup]; !exists {
					fmt.Fprintf(os.Stderr, "Error: group '%s' not found\n", groupScope)
					os.Exit(2)
				}
			}
		}
		homeModel.SetGroupScope(normalizedGroup)
	}
```

Note: We pass `nil` for instances since we only need to check group existence, not session data. The full session load happens later in the TUI init.

- [ ] **Step 4: Manual test**

Run: `go build -o /tmp/agent-deck ./cmd/agent-deck && /tmp/agent-deck -g nonexistent-group`
Expected: `Error: group 'nonexistent-group' not found` and exit code 2

- [ ] **Step 5: Commit**

```bash
git add cmd/agent-deck/main.go cmd/agent-deck/main_test.go
git commit -m "feat: validate group exists at startup for --group flag"
```

---

### Task 5: Scope hotkey handlers (`n`, `g`, `M`, `d`)

**Files:**
- Modify: `internal/ui/home.go:5021-5029` (M hotkey)
- Modify: `internal/ui/home.go:5059-5098` (g hotkey)
- Modify: `internal/ui/home.go:5153-5244` (n hotkey)
- Modify: `internal/ui/home.go:5257-5269` (d hotkey)

- [ ] **Step 1: Add helper method `isInGroupScope`**

In `internal/ui/home.go`, add after `SetGroupScope`:

```go
// isInGroupScope returns true if the given path is within the active group scope.
// Returns true for all paths when no scope is set.
func (h *Home) isInGroupScope(path string) bool {
	if h.groupScope == "" {
		return true
	}
	return path == h.groupScope || strings.HasPrefix(path, h.groupScope+"/")
}

// scopedGroupPaths returns group paths filtered to the active scope.
// Returns all paths when no scope is set.
func (h *Home) scopedGroupPaths() []string {
	allPaths := h.groupTree.GetGroupPaths()
	if h.groupScope == "" {
		return allPaths
	}
	var scoped []string
	for _, p := range allPaths {
		if h.isInGroupScope(p) {
			scoped = append(scoped, p)
		}
	}
	return scoped
}
```

- [ ] **Step 2: Scope the `M` (move) hotkey**

In `internal/ui/home.go`, at the `case "M", "shift+m":` handler (line ~5021), change:

```go
			h.groupDialog.ShowMove(h.groupTree.GetGroupPaths())
```

to:

```go
			h.groupDialog.ShowMove(h.scopedGroupPaths())
```

- [ ] **Step 3: Scope the `g` (create group) hotkey**

In `internal/ui/home.go`, at the `case "g":` handler, after the gg detection block (line ~5074), replace the context-aware group creation block:

```go
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				// On group header: default to subgroup mode
				h.groupDialog.ShowCreateWithContext(item.Group.Path, item.Group.Name)
			} else if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.GroupPath != "" {
				// On grouped session: default to root, Tab toggles to subgroup
				gPath := item.Session.GroupPath
				gName := gPath
				if idx := strings.LastIndex(gPath, "/"); idx >= 0 {
					gName = gPath[idx+1:]
				}
				h.groupDialog.ShowCreateWithContextDefaultRoot(gPath, gName)
			} else {
				// Ungrouped: root only, no toggle
				h.groupDialog.ShowCreateWithContext("", "")
			}
		} else {
			h.groupDialog.ShowCreateWithContext("", "")
		}
```

with:

```go
		if h.groupScope != "" {
			// Scoped mode: create subgroups under scope root or its children
			if h.cursor < len(h.flatItems) {
				item := h.flatItems[h.cursor]
				if item.Type == session.ItemTypeGroup {
					h.groupDialog.ShowCreateSubgroup(item.Group.Path, item.Group.Name)
				} else {
					// Default to creating under scope root
					scopeName := h.groupScope
					if idx := strings.LastIndex(h.groupScope, "/"); idx >= 0 {
						scopeName = h.groupScope[idx+1:]
					}
					h.groupDialog.ShowCreateSubgroup(h.groupScope, scopeName)
				}
			} else {
				scopeName := h.groupScope
				if idx := strings.LastIndex(h.groupScope, "/"); idx >= 0 {
					scopeName = h.groupScope[idx+1:]
				}
				h.groupDialog.ShowCreateSubgroup(h.groupScope, scopeName)
			}
		} else if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeGroup {
				// On group header: default to subgroup mode
				h.groupDialog.ShowCreateWithContext(item.Group.Path, item.Group.Name)
			} else if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.GroupPath != "" {
				// On grouped session: default to root, Tab toggles to subgroup
				gPath := item.Session.GroupPath
				gName := gPath
				if idx := strings.LastIndex(gPath, "/"); idx >= 0 {
					gName = gPath[idx+1:]
				}
				h.groupDialog.ShowCreateWithContextDefaultRoot(gPath, gName)
			} else {
				// Ungrouped: root only, no toggle
				h.groupDialog.ShowCreateWithContext("", "")
			}
		} else {
			h.groupDialog.ShowCreateWithContext("", "")
		}
```

- [ ] **Step 4: Scope the `n` (new session) hotkey**

In `internal/ui/home.go`, at the `case "n":` handler, replace the group auto-selection block (lines ~5226-5243):

```go
		// Auto-select parent group from current cursor position
		groupPath := session.DefaultGroupPath
		groupName := session.DefaultGroupName
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
			case session.ItemTypeGroup:
				groupPath = item.Group.Path
				groupName = item.Group.Name
			case session.ItemTypeSession:
				// Use the session's group
				groupPath = item.Path
				if group, exists := h.groupTree.Groups[groupPath]; exists {
					groupName = group.Name
				}
			}
		}
		defaultPath := h.getDefaultPathForGroup(groupPath)
		h.newDialog.ShowInGroup(groupPath, groupName, defaultPath)
```

with:

```go
		// Auto-select parent group from current cursor position
		groupPath := session.DefaultGroupPath
		groupName := session.DefaultGroupName
		if h.groupScope != "" {
			// Scoped mode: default to scope root
			groupPath = h.groupScope
			if group, exists := h.groupTree.Groups[h.groupScope]; exists {
				groupName = group.Name
			}
		}
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			switch item.Type {
			case session.ItemTypeGroup:
				groupPath = item.Group.Path
				groupName = item.Group.Name
			case session.ItemTypeSession:
				// Use the session's group
				groupPath = item.Path
				if group, exists := h.groupTree.Groups[groupPath]; exists {
					groupName = group.Name
				}
			}
		}
		defaultPath := h.getDefaultPathForGroup(groupPath)
		h.newDialog.ShowInGroup(groupPath, groupName, defaultPath)
```

- [ ] **Step 5: Scope the `d` (delete) hotkey for groups**

In `internal/ui/home.go`, at the `case "d":` handler (line ~5257), change the group deletion condition:

```go
		} else if item.Type == session.ItemTypeGroup && item.Path != session.DefaultGroupPath {
```

to:

```go
		} else if item.Type == session.ItemTypeGroup && item.Path != session.DefaultGroupPath && item.Path != h.groupScope {
```

This prevents deleting the scoped root group. Children within scope can still be deleted.

- [ ] **Step 6: Verify the build compiles**

Run: `go build ./cmd/agent-deck/`
Expected: Build succeeds

- [ ] **Step 7: Commit**

```bash
git add internal/ui/home.go
git commit -m "feat: scope hotkey handlers to group scope (n, g, M, d)"
```

---

### Task 6: Visual indicator in title bar

**Files:**
- Modify: `internal/ui/home.go:7482-7489` (View() title rendering)

- [ ] **Step 1: Add group scope to title rendering**

In `internal/ui/home.go`, in the `View()` method, find the title rendering block (line ~7482):

```go
	titleText := "Agent Deck"
	if h.profile != "" && h.profile != session.DefaultProfile {
		profileStyle := lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true)
		titleText = "Agent Deck " + profileStyle.Render("["+h.profile+"]")
	}
```

Replace with:

```go
	titleText := "Agent Deck"
	if h.profile != "" && h.profile != session.DefaultProfile {
		profileStyle := lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true)
		titleText = "Agent Deck " + profileStyle.Render("["+h.profile+"]")
	}
	if h.groupScope != "" {
		scopeStyle := lipgloss.NewStyle().
			Foreground(ColorPurple).
			Bold(true)
		scopeName := h.groupScope
		if group, exists := h.groupTree.Groups[h.groupScope]; exists {
			scopeName = group.Name
		}
		titleText += " " + scopeStyle.Render("["+scopeName+"]")
	}
```

Note: Check that `ColorPurple` exists in `internal/ui/styles.go`. If not, use `ColorCyan` or another existing color. The color choice should visually distinguish scope from profile.

- [ ] **Step 2: Verify the color constant exists**

Run: `grep -n 'ColorPurple\|ColorMagenta\|ColorViolet' internal/ui/styles.go`

If no purple color exists, define it or use an existing color like `ColorComment` (typically a muted color). Adjust the code in step 1 accordingly.

- [ ] **Step 3: Verify build compiles and test manually**

Run: `go build ./cmd/agent-deck/`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add internal/ui/home.go
git commit -m "feat: show group scope indicator in TUI title bar"
```

---

### Task 7: Empty state for scoped view

**Files:**
- Modify: `internal/ui/home.go` (empty state rendering in View())

- [ ] **Step 1: Modify the empty state in `renderSessionList`**

In `internal/ui/home.go`, find the `renderSessionList` method (line ~8923). The empty state is at line ~8927:

```go
	if len(h.flatItems) == 0 {
		// Responsive empty state - adapts to available space
		contentWidth := width - 4
		contentHeight := height - 2
		...
		emptyContent := renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "⬡",
			Title:    "No Sessions Yet",
			Subtitle: "Get started by creating your first session",
			Hints:    hints,
		}, contentWidth, contentHeight)
```

Add a group-scope check before the existing empty state block. Insert at line ~8928, before the generic empty state:

```go
	if len(h.flatItems) == 0 {
		contentWidth := width - 4
		contentHeight := height - 2
		if contentWidth < 10 {
			contentWidth = 10
		}

		// Group-scoped empty state
		if h.groupScope != "" {
			scopeName := h.groupScope
			if group, exists := h.groupTree.Groups[h.groupScope]; exists {
				scopeName = group.Name
			}
			hints := []string{}
			if key := h.actionKey(hotkeyNewSession); key != "" {
				hints = append(hints, fmt.Sprintf("Press %s to create a session", key))
			}
			return renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "⬡",
				Title:    "No sessions in " + scopeName,
				Subtitle: "This group is empty",
				Hints:    hints,
			}, contentWidth, contentHeight)
		}

		// Responsive empty state - adapts to available space (existing code continues)
```

Also apply the same pattern in `renderPreviewPane` (line ~9933) where it checks `len(h.flatItems) == 0`. Add a scoped variant before the generic "Ready to Go" message:

```go
		if len(h.flatItems) == 0 {
			// Group-scoped empty state for preview pane
			if h.groupScope != "" {
				scopeName := h.groupScope
				if group, exists := h.groupTree.Groups[h.groupScope]; exists {
					scopeName = group.Name
				}
				return renderEmptyStateResponsive(EmptyStateConfig{
					Icon:     "✦",
					Title:    scopeName,
					Subtitle: "Group scope active",
					Hints:    []string{"Only sessions in this group are shown"},
				}, width, height)
			}
			// existing "Ready to Go" code continues...
```

- [ ] **Step 2: Verify build compiles**

Run: `go build ./cmd/agent-deck/`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add internal/ui/home.go
git commit -m "feat: show scoped empty state when group has no sessions"
```

---

### Task 8: Help text update

**Files:**
- Modify: `cmd/agent-deck/main.go` (printHelp function)
- Modify: `internal/ui/help.go` (help overlay)

- [ ] **Step 1: Add `--group` to CLI help**

In `cmd/agent-deck/main.go`, find `printHelp()` (line ~2263). After the `--profile` line (line 2270):

```go
	fmt.Println("  -p, --profile <name>   Use specific profile (default: 'default')")
```

Add:

```go
	fmt.Println("  -g, --group <name>     Launch TUI scoped to a specific group")
```

Also update the Usage line (line 2267) from:

```go
	fmt.Println("Usage: agent-deck [-p profile] [command]")
```

to:

```go
	fmt.Println("Usage: agent-deck [-p profile] [-g group] [command]")
```

- [ ] **Step 2: Add scope note to TUI help overlay**

In `internal/ui/help.go`, the help overlay renders static text. Add a line in the "Filtering" or "Navigation" section noting the `--group` flag. Find the section with status filter keys and add after it:

```go
	"  --group <name>  Launch scoped to a group",
```

This is informational only -- the help overlay is static text and doesn't need runtime state.

- [ ] **Step 3: Verify build compiles**

Run: `go build ./cmd/agent-deck/`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add cmd/agent-deck/main.go internal/ui/help.go
git commit -m "feat: add --group flag to help text"
```

---

### Task 9: Integration test and final verification

**Files:**
- Test: `cmd/agent-deck/main_test.go`

- [ ] **Step 1: Run the full test suite**

Run: `go test ./... -count=1`
Expected: All tests PASS

- [ ] **Step 2: Run linter**

Run: `go vet ./...`
Expected: No issues

- [ ] **Step 3: Manual end-to-end test**

Build and test the full flow:

```bash
go build -o /tmp/agent-deck ./cmd/agent-deck

# Test invalid group
/tmp/agent-deck -g nonexistent  # Should error with exit 2

# Test valid group (create one first if needed)
/tmp/agent-deck group create testgroup
/tmp/agent-deck -g testgroup    # Should launch TUI scoped to testgroup

# Verify: title shows [testgroup], only testgroup sessions visible
# Verify: n creates session in testgroup
# Verify: g creates subgroup under testgroup
# Verify: d cannot delete testgroup itself
```

- [ ] **Step 4: Clean up test group**

```bash
/tmp/agent-deck group delete testgroup --force
```

- [ ] **Step 5: Final commit if any fixes needed**

```bash
git add -A
git commit -m "fix: address issues found during integration testing"
```

Only commit this if there were actual fixes. Skip if everything passed cleanly.
