package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleGroup dispatches group subcommands
func handleGroup(profile string, args []string) {
	if len(args) == 0 {
		// Default to list
		handleGroupList(profile, nil)
		return
	}

	switch args[0] {
	case "list", "ls":
		handleGroupList(profile, args[1:])
	case "create", "new":
		handleGroupCreate(profile, args[1:])
	case "delete", "rm":
		handleGroupDelete(profile, args[1:])
	case "move", "mv":
		handleGroupMove(profile, args[1:])
	case "help", "--help", "-h":
		printGroupHelp()
		return
	default:
		fmt.Printf("Unknown group command: %s\n", args[0])
		fmt.Println()
		printGroupHelp()
		os.Exit(1)
	}
}

// printGroupHelp prints usage for group commands
func printGroupHelp() {
	fmt.Println("Usage: agent-deck group <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list              List all groups with session counts")
	fmt.Println("  create <name>     Create a new group")
	fmt.Println("  delete <name>     Delete a group")
	fmt.Println("  move <id> <group> Move session to a different group")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck group list")
	fmt.Println("  agent-deck group create mobile")
	fmt.Println("  agent-deck group create ios --parent mobile")
	fmt.Println("  agent-deck group delete experiments")
	fmt.Println("  agent-deck group delete work --force")
	fmt.Println("  agent-deck group move my-project work/frontend")
	fmt.Println("  agent-deck group move my-project \"\"          # Move to root")
}

// handleGroupList lists all groups with session counts and status
func handleGroupList(profile string, args []string) {
	fs := flag.NewFlagSet("group list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck group list [options]")
		fmt.Println()
		fmt.Println("List all groups with session counts.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, *quiet || *quietShort)

	// Load sessions and groups
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Build group tree
	groupTree := session.NewGroupTreeWithGroups(instances, groups)

	if *jsonOutput {
		// Build JSON output structure
		type groupStatusJSON struct {
			Running int `json:"running"`
			Waiting int `json:"waiting"`
			Idle    int `json:"idle"`
			Error   int `json:"error"`
		}

		type groupJSON struct {
			Name         string           `json:"name"`
			Path         string           `json:"path"`
			SessionCount int              `json:"session_count"`
			Status       *groupStatusJSON `json:"status,omitempty"`
			Children     []groupJSON      `json:"children,omitempty"`
		}

		// Build hierarchical structure (with recursive session counts - Issue #48)
		buildGroupJSON := func(g *session.Group) groupJSON {
			// Count status recursively for this group and all subgroups
			status := groupStatusJSON{}
			for path, subGroup := range groupTree.Groups {
				if path == g.Path || strings.HasPrefix(path, g.Path+"/") {
					for _, sess := range subGroup.Sessions {
						_ = sess.UpdateStatus() // Refresh status
						switch sess.Status {
						case session.StatusRunning:
							status.Running++
						case session.StatusWaiting:
							status.Waiting++
						case session.StatusIdle:
							status.Idle++
						case session.StatusError:
							status.Error++
						}
					}
				}
			}

			// Use recursive session count
			sessCount := groupTree.SessionCountForGroup(g.Path)
			gj := groupJSON{
				Name:         g.Name,
				Path:         g.Path,
				SessionCount: sessCount,
			}
			if sessCount > 0 {
				gj.Status = &status
			}
			return gj
		}

		// Build top-level groups with their children
		groupsJSON := []groupJSON{}
		processedPaths := make(map[string]bool)

		for _, g := range groupTree.GroupList {
			// Skip if already processed as a child
			if processedPaths[g.Path] {
				continue
			}

			// Only process root-level groups here
			if session.GetGroupLevel(g.Path) > 0 {
				continue
			}

			gj := buildGroupJSON(g)

			// Find children
			for _, child := range groupTree.GroupList {
				if strings.HasPrefix(child.Path, g.Path+"/") {
					// Direct child (one level deeper)
					childLevel := session.GetGroupLevel(child.Path)
					if childLevel == session.GetGroupLevel(g.Path)+1 {
						gj.Children = append(gj.Children, buildGroupJSON(child))
						processedPaths[child.Path] = true
					}
				}
			}

			groupsJSON = append(groupsJSON, gj)
			processedPaths[g.Path] = true
		}

		// Count totals
		totalGroups := len(groupTree.Groups)
		totalSessions := groupTree.SessionCount()

		out.Print("", map[string]interface{}{
			"groups":         groupsJSON,
			"total_groups":   totalGroups,
			"total_sessions": totalSessions,
		})
		return
	}

	// Human-readable output
	if len(groupTree.Groups) == 0 {
		out.Print("No groups found.\n", nil)
		return
	}

	var sb strings.Builder
	sb.WriteString("Groups:\n\n")
	sb.WriteString(fmt.Sprintf("%-20s %-10s %s\n", "NAME", "SESSIONS", "STATUS"))
	sb.WriteString(strings.Repeat("-", 50) + "\n")

	// Track which groups we've printed to handle hierarchy
	printedPaths := make(map[string]bool)

	// Print groups in tree order
	for _, g := range groupTree.GroupList {
		if printedPaths[g.Path] {
			continue
		}

		// Calculate indent based on level
		level := session.GetGroupLevel(g.Path)
		indent := strings.Repeat("  ", level)
		prefix := ""
		if level > 0 {
			// Find if this is last sibling at its level
			parentPath := getParentGroupPath(g.Path)
			isLast := true
			foundCurrent := false
			for _, other := range groupTree.GroupList {
				otherParent := getParentGroupPath(other.Path)
				if otherParent == parentPath && session.GetGroupLevel(other.Path) == level {
					if foundCurrent && other.Path != g.Path {
						isLast = false
						break
					}
					if other.Path == g.Path {
						foundCurrent = true
					}
				}
			}
			if isLast {
				prefix = "└── "
			} else {
				prefix = "├── "
			}
		}

		// Count sessions and status for this group (including subgroups - Issue #48)
		sessCount := groupTree.SessionCountForGroup(g.Path)
		statusStr := ""
		if sessCount > 0 {
			running, waiting, idle := 0, 0, 0
			// Count status recursively for all sessions in this group and subgroups
			for path, subGroup := range groupTree.Groups {
				if path == g.Path || strings.HasPrefix(path, g.Path+"/") {
					for _, sess := range subGroup.Sessions {
						_ = sess.UpdateStatus()
						switch sess.Status {
						case session.StatusRunning:
							running++
						case session.StatusWaiting:
							waiting++
						case session.StatusIdle:
							idle++
						}
					}
				}
			}
			var parts []string
			if running > 0 {
				parts = append(parts, fmt.Sprintf("● %d", running))
			}
			if waiting > 0 {
				parts = append(parts, fmt.Sprintf("◐ %d", waiting))
			}
			if idle > 0 {
				parts = append(parts, fmt.Sprintf("○ %d", idle))
			}
			statusStr = strings.Join(parts, " ")
		}

		name := indent + prefix + g.Name
		sb.WriteString(fmt.Sprintf("%-20s %-10d %s\n", truncateGroupName(name, 20), sessCount, statusStr))
		printedPaths[g.Path] = true
	}

	totalGroups := len(groupTree.Groups)
	totalSessions := groupTree.SessionCount()
	sb.WriteString(fmt.Sprintf("\nTotal: %d groups, %d sessions\n", totalGroups, totalSessions))

	out.Print(sb.String(), nil)
}

// handleGroupCreate creates a new group
func handleGroupCreate(profile string, args []string) {
	fs := flag.NewFlagSet("group create", flag.ExitOnError)
	parent := fs.String("parent", "", "Create as subgroup under this parent")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck group create <name> [options]")
		fmt.Println()
		fmt.Println("Create a new group.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck group create mobile")
		fmt.Println("  agent-deck group create ios --parent mobile")
	}

	// Reorder args: move name to end so flags are parsed correctly
	// Go's flag package stops parsing at first non-flag argument
	// This allows: "group create ios --parent mobile" to work correctly
	args = reorderGroupArgs(args)

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, *quiet || *quietShort)

	name := fs.Arg(0)
	if name == "" {
		out.Error("group name is required", ErrCodeNotFound)
		fmt.Println("Usage: agent-deck group create <name> [--parent <group>]")
		os.Exit(1)
	}

	// Load sessions and groups
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Build group tree
	groupTree := session.NewGroupTreeWithGroups(instances, groups)

	var newGroup *session.Group
	var fullPath string

	if *parent != "" {
		// Verify parent exists
		parentPath := normalizeGroupPath(*parent)
		if _, exists := groupTree.Groups[parentPath]; !exists {
			out.Error(fmt.Sprintf("parent group '%s' not found", *parent), ErrCodeNotFound)
			os.Exit(2)
		}
		newGroup = groupTree.CreateSubgroup(parentPath, name)
		fullPath = newGroup.Path
	} else {
		newGroup = groupTree.CreateGroup(name)
		fullPath = newGroup.Path
	}

	// Check if group already existed
	existingGroup := false
	for _, g := range groups {
		if g.Path == fullPath {
			existingGroup = true
			break
		}
	}

	// Save
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	if existingGroup {
		out.Success(fmt.Sprintf("Group already exists: %s", fullPath), map[string]interface{}{
			"success": true,
			"name":    newGroup.Name,
			"path":    fullPath,
			"existed": true,
		})
	} else {
		out.Success(fmt.Sprintf("Created group: %s", fullPath), map[string]interface{}{
			"success": true,
			"name":    newGroup.Name,
			"path":    fullPath,
		})
	}
}

// handleGroupDelete deletes a group
func handleGroupDelete(profile string, args []string) {
	fs := flag.NewFlagSet("group delete", flag.ExitOnError)
	force := fs.Bool("force", false, "Move sessions to parent and delete")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck group delete <name> [options]")
		fmt.Println()
		fmt.Println("Delete a group.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck group delete experiments")
		fmt.Println("  agent-deck group delete work --force   # Move sessions to parent")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, *quiet || *quietShort)

	name := fs.Arg(0)
	if name == "" {
		out.Error("group name is required", ErrCodeNotFound)
		fmt.Println("Usage: agent-deck group delete <name> [--force]")
		os.Exit(1)
	}

	// Load sessions and groups
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Build group tree
	groupTree := session.NewGroupTreeWithGroups(instances, groups)

	// Find the group
	groupPath := normalizeGroupPath(name)
	group, exists := groupTree.Groups[groupPath]
	if !exists {
		// Try finding by name match
		for path, g := range groupTree.Groups {
			if strings.EqualFold(g.Name, name) {
				groupPath = path
				group = g
				exists = true
				break
			}
		}
	}

	if !exists {
		out.Error(fmt.Sprintf("group '%s' not found", name), ErrCodeNotFound)
		os.Exit(2)
	}

	// Check if group is protected (default group)
	if groupPath == session.DefaultGroupPath {
		out.Error("cannot delete the default group", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Count sessions in group and subgroups
	sessionCount := len(group.Sessions)
	for path, g := range groupTree.Groups {
		if strings.HasPrefix(path, groupPath+"/") {
			sessionCount += len(g.Sessions)
		}
	}

	// Check if group has sessions and --force not specified
	if sessionCount > 0 && !*force {
		out.Error(fmt.Sprintf("group '%s' has %d sessions. Use --force to move them to parent.", name, sessionCount), ErrCodeGroupNotEmpty)
		os.Exit(1)
	}

	// Determine where sessions will be moved
	parentPath := getParentGroupPath(groupPath)
	movedTo := parentPath
	if movedTo == "" {
		movedTo = session.DefaultGroupPath
	}

	// Delete the group (this also moves sessions to default group)
	movedSessions := groupTree.DeleteGroup(groupPath)

	// If we want to move to parent instead of default, do it manually
	if parentPath != "" && len(movedSessions) > 0 {
		// Move sessions from default to parent
		for _, sess := range movedSessions {
			sess.GroupPath = parentPath
		}
		// Re-sync the tree
		groupTree.SyncWithInstances(groupTree.GetAllInstances())
	}

	// Save
	if err := storage.SaveWithGroups(groupTree.GetAllInstances(), groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Deleted group: %s", name), map[string]interface{}{
		"success":        true,
		"name":           name,
		"sessions_moved": len(movedSessions),
		"moved_to":       movedTo,
	})
}

// handleGroupMove moves a session to a different group
func handleGroupMove(profile string, args []string) {
	fs := flag.NewFlagSet("group move", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck group move <session-id> <group>")
		fmt.Println()
		fmt.Println("Move a session to a different group.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  <session-id>   Session title, ID prefix, or path")
		fmt.Println("  <group>        Target group path (use \"\" or root for ungrouped)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck group move my-project work/frontend")
		fmt.Println("  agent-deck group move my-project \"\"              # Move to root")
		fmt.Println("  agent-deck group move my-project root            # Move to root")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, *quiet || *quietShort)

	sessionID := fs.Arg(0)
	targetGroup := fs.Arg(1)

	if sessionID == "" {
		out.Error("session identifier is required", ErrCodeNotFound)
		fmt.Println("Usage: agent-deck group move <session-id> <group>")
		os.Exit(1)
	}

	if fs.NArg() < 2 {
		out.Error("target group is required", ErrCodeNotFound)
		fmt.Println("Usage: agent-deck group move <session-id> <group>")
		os.Exit(1)
	}

	// Load sessions and groups
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Find the session
	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Normalize target group
	// Handle special cases for moving to root/default group
	targetGroupPath := normalizeGroupPath(targetGroup)
	if targetGroup == "root" || targetGroup == "" {
		targetGroupPath = session.DefaultGroupPath
	}

	// Store original group for output
	fromGroup := inst.GroupPath
	if fromGroup == "" {
		fromGroup = session.DefaultGroupPath
	}

	// Build group tree
	groupTree := session.NewGroupTreeWithGroups(instances, groups)

	// Check if target group exists (unless moving to default)
	if targetGroupPath != session.DefaultGroupPath && targetGroupPath != "" {
		if _, exists := groupTree.Groups[targetGroupPath]; !exists {
			// Create the group
			groupTree.CreateGroup(targetGroupPath)
		}
	}

	// Move the session
	groupTree.MoveSessionToGroup(inst, targetGroupPath)

	// Save
	if err := storage.SaveWithGroups(groupTree.GetAllInstances(), groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	toGroup := targetGroupPath
	if toGroup == "" {
		toGroup = session.DefaultGroupPath
	}

	out.Success(fmt.Sprintf("Moved %s to %s", inst.Title, toGroup), map[string]interface{}{
		"success": true,
		"session": inst.Title,
		"from":    fromGroup,
		"to":      toGroup,
	})
}

// getParentGroupPath returns the parent path of a group path
func getParentGroupPath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[:idx]
	}
	return "" // root level
}

// normalizeGroupPath converts a group name/path to its normalized path form
func normalizeGroupPath(name string) string {
	// Already looks like a path
	if strings.Contains(name, "/") {
		return strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	}
	// Simple name
	return strings.ToLower(strings.ReplaceAll(name, " ", "-"))
}

// truncateGroupName shortens a group name for display
func truncateGroupName(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// reorderGroupArgs reorders arguments so flags come before positional args
// This fixes Go's flag package limitation where flags after positional args are ignored
// e.g., "ios --parent mobile" becomes "--parent mobile ios"
func reorderGroupArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}

	// Known flags that take a value
	valueFlags := map[string]bool{
		"--parent": true,
	}

	var flags []string
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Check if it's a flag
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)

			// Check if this flag takes a value (and value is separate)
			if !strings.Contains(arg, "=") && valueFlags[arg] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, arg)
		}
	}

	// Return flags first, then positional args
	return append(flags, positional...)
}
