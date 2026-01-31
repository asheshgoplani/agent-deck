package session

import (
	"sort"
	"strings"
	"unicode"
)

// DefaultGroupName is the display name for the default group where ungrouped sessions go
const DefaultGroupName = "My Sessions"

// DefaultGroupPath is the normalized path for the default group (used for lookups and protection)
const DefaultGroupPath = "my-sessions"

// ItemType represents the type of item in the flattened list
type ItemType int

const (
	ItemTypeGroup ItemType = iota
	ItemTypeSession
)

// Item represents a single item in the flattened group tree view
type Item struct {
	Type               ItemType
	Group              *Group
	Session            *Instance
	Level              int    // Indentation level (0 for root groups, 1 for sessions)
	Path               string // Group path for this item
	IsLastInGroup      bool   // True if this is the last session in its group (for tree rendering)
	RootGroupNum       int    // Pre-computed root group number for hotkey display (1-9, 0 if not a root group)
	IsSubSession       bool   // True if this session has a parent session
	IsLastSubSession   bool   // True if this is the last sub-session of its parent (for tree rendering)
	ParentIsLastInGroup bool  // True if parent session is last top-level item (for tree line rendering)
}

// Group represents a group of sessions
type Group struct {
	Name        string
	Path        string // Full path like "projects" or "projects/devops"
	Expanded    bool
	Sessions    []*Instance
	Order       int
	DefaultPath string // Most recent project path used for sessions in this group
}

// GroupTree manages hierarchical session organization
type GroupTree struct {
	Groups    map[string]*Group // path -> group
	GroupList []*Group          // Ordered list of groups
	Expanded  map[string]bool   // Collapsed state persistence
}

// NewGroupTree creates a new group tree from instances
func NewGroupTree(instances []*Instance) *GroupTree {
	tree := &GroupTree{
		Groups:   make(map[string]*Group),
		Expanded: make(map[string]bool),
	}

	// Build groups from instances
	for _, inst := range instances {
		groupPath := inst.GroupPath
		if groupPath == "" {
			groupPath = DefaultGroupPath
		}

		group, exists := tree.Groups[groupPath]
		if !exists {
			// Ensure parent groups exist for hierarchical paths
			tree.ensureParentGroupsExist(groupPath)
			// Use proper name for default group, otherwise extract name from path
			name := extractGroupName(groupPath)
			if groupPath == DefaultGroupPath {
				name = DefaultGroupName
			}
			group = &Group{
				Name:     name,
				Path:     groupPath,
				Expanded: true, // Default expanded
				Sessions: []*Instance{},
			}
			tree.Groups[groupPath] = group
			tree.Expanded[groupPath] = true
		}
		group.Sessions = append(group.Sessions, inst)
	}

	// Sort groups alphabetically and assign order
	tree.rebuildGroupList()

	// Update default paths for all groups
	for groupPath := range tree.Groups {
		tree.updateGroupDefaultPath(groupPath)
	}

	return tree
}

// NewGroupTreeWithGroups creates a group tree from instances and stored group data
func NewGroupTreeWithGroups(instances []*Instance, storedGroups []*GroupData) *GroupTree {
	tree := &GroupTree{
		Groups:   make(map[string]*Group),
		Expanded: make(map[string]bool),
	}

	// First, create groups from stored data (preserves empty groups)
	for _, gd := range storedGroups {
		group := &Group{
			Name:        gd.Name,
			Path:        gd.Path,
			Expanded:    gd.Expanded,
			Sessions:    []*Instance{},
			Order:       gd.Order,
			DefaultPath: gd.DefaultPath,
		}
		tree.Groups[gd.Path] = group
		tree.Expanded[gd.Path] = gd.Expanded
	}

	// Then add instances to their groups
	for _, inst := range instances {
		groupPath := inst.GroupPath
		if groupPath == "" {
			groupPath = DefaultGroupPath
		}

		group, exists := tree.Groups[groupPath]
		if !exists {
			// Ensure parent groups exist for hierarchical paths
			tree.ensureParentGroupsExist(groupPath)
			// Group doesn't exist in stored data, create it
			// Use proper name for default group, otherwise extract name from path
			name := extractGroupName(groupPath)
			if groupPath == DefaultGroupPath {
				name = DefaultGroupName
			}
			group = &Group{
				Name:     name,
				Path:     groupPath,
				Expanded: true,
				Sessions: []*Instance{},
			}
			tree.Groups[groupPath] = group
			tree.Expanded[groupPath] = true
		}
		group.Sessions = append(group.Sessions, inst)
	}

	// Rebuild group list maintaining stored order
	tree.rebuildGroupList()

	// Update default paths for all groups (may override stored if sessions have newer paths)
	for groupPath := range tree.Groups {
		tree.updateGroupDefaultPath(groupPath)
	}

	return tree
}

// Note: GroupData is defined in storage.go in the same package

// rebuildGroupList rebuilds the ordered group list
func (t *GroupTree) rebuildGroupList() {
	t.GroupList = make([]*Group, 0, len(t.Groups))
	for _, g := range t.Groups {
		t.GroupList = append(t.GroupList, g)
	}
	sort.Slice(t.GroupList, func(i, j int) bool {
		// Sort hierarchically: parents before children, siblings by order
		pathI := t.GroupList[i].Path
		pathJ := t.GroupList[j].Path

		// If one is a prefix of the other (parent-child), parent comes first
		if strings.HasPrefix(pathJ, pathI+"/") {
			return true // i is parent of j
		}
		if strings.HasPrefix(pathI, pathJ+"/") {
			return false // j is parent of i
		}

		// Get parent paths for comparison
		parentI := getParentPath(pathI)
		parentJ := getParentPath(pathJ)

		// If they have the same parent, sort by order then name
		if parentI == parentJ {
			if t.GroupList[i].Order != t.GroupList[j].Order {
				return t.GroupList[i].Order < t.GroupList[j].Order
			}
			return t.GroupList[i].Name < t.GroupList[j].Name
		}

		// Different parents - compare at root level to keep tree structure
		// This ensures subgroups stay grouped with their root ancestors
		rootI := getRootPath(pathI)
		rootJ := getRootPath(pathJ)

		if rootI == rootJ {
			// Same root - find the branch ancestors at the divergence point and compare as siblings
			// Example: comparing "a/b/c" with "a/d" - find "b" and "d" (children of common ancestor "a")
			partsI := strings.Split(pathI, "/")
			partsJ := strings.Split(pathJ, "/")

			// Find the first point where paths diverge
			divergeLevel := 0
			for divergeLevel < len(partsI) && divergeLevel < len(partsJ) {
				if partsI[divergeLevel] != partsJ[divergeLevel] {
					break
				}
				divergeLevel++
			}

			// Get the branch paths at the divergence point
			branchPathI := strings.Join(partsI[:divergeLevel+1], "/")
			branchPathJ := strings.Join(partsJ[:divergeLevel+1], "/")

			// Compare branch roots as siblings (by Order, then Name)
			branchI := t.Groups[branchPathI]
			branchJ := t.Groups[branchPathJ]

			if branchI != nil && branchJ != nil {
				if branchI.Order != branchJ.Order {
					return branchI.Order < branchJ.Order
				}
				return branchI.Name < branchJ.Name
			}

			// Fallback to path comparison if branches not found
			return pathI < pathJ
		}

		// Different root ancestors - compare roots by order then name
		// This ensures entire subtrees are kept together
		rootGroupI := t.Groups[rootI]
		rootGroupJ := t.Groups[rootJ]
		if rootGroupI != nil && rootGroupJ != nil {
			if rootGroupI.Order != rootGroupJ.Order {
				return rootGroupI.Order < rootGroupJ.Order
			}
			return rootGroupI.Name < rootGroupJ.Name
		}

		// Fallback to full path comparison if root groups not found
		return pathI < pathJ
	})
	// Note: Do NOT reassign Order values here - this would destroy user-customized
	// order stored in sessions.json. Order values are set:
	// 1. When loaded from storage (preserved)
	// 2. When creating new groups (Order = len(GroupList))
	// 3. When manually reordering with K/J keys (MoveGroupUp/Down)
}

// getParentPath returns the parent path of a group path
func getParentPath(path string) string {
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[:idx]
	}
	return "" // root level
}

// getRootPath returns the root-level path (first segment) of a hierarchical path
// e.g., "parent/child/grandchild" -> "parent", "root" -> "root"
func getRootPath(path string) string {
	if idx := strings.Index(path, "/"); idx != -1 {
		return path[:idx]
	}
	return path // already root level
}

// extractGroupName extracts the display name from a group path
// e.g., "parent/child" -> "child", "root" -> "root"
func extractGroupName(path string) string {
	if path == "" {
		return ""
	}
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[idx+1:]
	}
	return path // root level - path is the name
}

// ensureParentGroupsExist creates all parent groups for a given path if they don't exist
// e.g., for path "a/b/c", it creates groups "a" and "a/b" (but not "a/b/c")
func (t *GroupTree) ensureParentGroupsExist(path string) {
	parts := strings.Split(path, "/")
	if len(parts) <= 1 {
		return // No parents needed for root-level paths
	}

	// Create each parent level
	currentPath := ""
	for i := 0; i < len(parts)-1; i++ { // -1 to exclude the leaf
		if currentPath == "" {
			currentPath = parts[i]
		} else {
			currentPath = currentPath + "/" + parts[i]
		}

		if _, exists := t.Groups[currentPath]; !exists {
			name := extractGroupName(currentPath)
			group := &Group{
				Name:     name,
				Path:     currentPath,
				Expanded: true,
				Sessions: []*Instance{},
				Order:    len(t.GroupList),
			}
			t.Groups[currentPath] = group
			t.Expanded[currentPath] = true
		}
	}
}

// GetGroupLevel returns the nesting level of a group (0 for root, 1 for child, etc.)
func GetGroupLevel(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, "/")
}

// Flatten returns a flat list of items for cursor navigation
func (t *GroupTree) Flatten() []Item {
	items := []Item{}

	for _, group := range t.GroupList {
		// Calculate group nesting level from path
		groupLevel := GetGroupLevel(group.Path)

		// Check if parent group is collapsed - if so, skip this group
		if groupLevel > 0 {
			idx := strings.LastIndex(group.Path, "/")
			if idx == -1 {
				continue // Malformed path, skip
			}
			parentPath := group.Path[:idx]
			if parentGroup, exists := t.Groups[parentPath]; exists && !parentGroup.Expanded {
				continue // Parent is collapsed, skip this subgroup
			}
		}

		// Add group header
		items = append(items, Item{
			Type:  ItemTypeGroup,
			Group: group,
			Level: groupLevel,
			Path:  group.Path,
		})

		// Add sessions if expanded
		if group.Expanded {
			// Separate parent sessions from sub-sessions
			parentSessions := []*Instance{}
			subSessionsByParent := make(map[string][]*Instance) // parentID -> sub-sessions

			for _, sess := range group.Sessions {
				if sess.IsSubSession() {
					subSessionsByParent[sess.ParentSessionID] = append(subSessionsByParent[sess.ParentSessionID], sess)
				} else {
					parentSessions = append(parentSessions, sess)
				}
			}

			// Count total top-level items (parent sessions + orphan sub-sessions whose parent is in different group)
			// For determining IsLastInGroup, we need to know how many top-level items there are
			topLevelCount := len(parentSessions)
			for parentID, subs := range subSessionsByParent {
				// Check if parent is in this group
				parentInGroup := false
				for _, p := range parentSessions {
					if p.ID == parentID {
						parentInGroup = true
						break
					}
				}
				if !parentInGroup {
					// Parent is not in this group, so sub-sessions appear as top-level
					topLevelCount += len(subs)
				}
			}

			topLevelIndex := 0
			for _, sess := range parentSessions {
				isLastTopLevel := topLevelIndex == topLevelCount-1

				// Get sub-sessions for this parent
				subs := subSessionsByParent[sess.ID]
				// If this session has sub-sessions, it's not the last in group visually
				isLastInGroup := isLastTopLevel && len(subs) == 0

				items = append(items, Item{
					Type:          ItemTypeSession,
					Session:       sess,
					Level:         groupLevel + 1,
					Path:          group.Path,
					IsLastInGroup: isLastInGroup,
				})

				// Add sub-sessions immediately after parent
				for subIdx, sub := range subs {
					isLastSub := subIdx == len(subs)-1
					// Sub-session is last in group if parent was last top-level and this is last sub
					isSubLastInGroup := isLastTopLevel && isLastSub

					items = append(items, Item{
						Type:                ItemTypeSession,
						Session:             sub,
						Level:               groupLevel + 2, // One more level of indentation
						Path:                group.Path,
						IsLastInGroup:       isSubLastInGroup,
						IsSubSession:        true,
						IsLastSubSession:    isLastSub,
						ParentIsLastInGroup: isLastTopLevel, // For tree line rendering (│ vs spaces)
					})
				}

				// Remove these subs from the map so we don't add them again
				delete(subSessionsByParent, sess.ID)

				topLevelIndex++
			}

			// Add any orphaned sub-sessions (parent not in this group)
			for _, subs := range subSessionsByParent {
				for _, sub := range subs {
					topLevelIndex++
					items = append(items, Item{
						Type:          ItemTypeSession,
						Session:       sub,
						Level:         groupLevel + 1,
						Path:          group.Path,
						IsLastInGroup: topLevelIndex == topLevelCount,
						IsSubSession:  true, // Still a sub-session, just orphaned in this group
					})
				}
			}
		}
	}

	return items
}

// ToggleGroup toggles the expanded state of a group
func (t *GroupTree) ToggleGroup(path string) {
	if group, exists := t.Groups[path]; exists {
		group.Expanded = !group.Expanded
		t.Expanded[path] = group.Expanded
	}
}

// ExpandGroup expands a group
func (t *GroupTree) ExpandGroup(path string) {
	if group, exists := t.Groups[path]; exists {
		group.Expanded = true
		t.Expanded[path] = true
	}
}

// ExpandGroupWithParents expands a group and all its parent groups
// This ensures the group and its contents are visible in the flattened view
func (t *GroupTree) ExpandGroupWithParents(path string) {
	// Expand all parent groups first
	parts := strings.Split(path, "/")
	currentPath := ""
	for i := 0; i < len(parts); i++ {
		if currentPath == "" {
			currentPath = parts[i]
		} else {
			currentPath = currentPath + "/" + parts[i]
		}
		if group, exists := t.Groups[currentPath]; exists {
			group.Expanded = true
			t.Expanded[currentPath] = true
		}
	}
}

// CollapseGroup collapses a group
func (t *GroupTree) CollapseGroup(path string) {
	if group, exists := t.Groups[path]; exists {
		group.Expanded = false
		t.Expanded[path] = false
	}
}

// MoveGroupUp moves a group up in the order (only within siblings at same level)
func (t *GroupTree) MoveGroupUp(path string) {
	parentPath := getParentPath(path)

	for i, g := range t.GroupList {
		if g.Path == path && i > 0 {
			// Only swap if previous item is a sibling (same parent)
			prevParent := getParentPath(t.GroupList[i-1].Path)
			if prevParent == parentPath {
				t.GroupList[i], t.GroupList[i-1] = t.GroupList[i-1], t.GroupList[i]
				t.GroupList[i].Order = i
				t.GroupList[i-1].Order = i - 1
			}
			break
		}
	}
}

// MoveGroupDown moves a group down in the order (only within siblings at same level)
func (t *GroupTree) MoveGroupDown(path string) {
	parentPath := getParentPath(path)

	for i, g := range t.GroupList {
		if g.Path == path && i < len(t.GroupList)-1 {
			// Only swap if next item is a sibling (same parent)
			nextParent := getParentPath(t.GroupList[i+1].Path)
			if nextParent == parentPath {
				t.GroupList[i], t.GroupList[i+1] = t.GroupList[i+1], t.GroupList[i]
				t.GroupList[i].Order = i
				t.GroupList[i+1].Order = i + 1
			}
			break
		}
	}
}

// MoveSessionUp moves a session up within its group
func (t *GroupTree) MoveSessionUp(inst *Instance) {
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}

	for i, s := range group.Sessions {
		if s.ID == inst.ID && i > 0 {
			group.Sessions[i], group.Sessions[i-1] = group.Sessions[i-1], group.Sessions[i]
			break
		}
	}
}

// MoveSessionDown moves a session down within its group
func (t *GroupTree) MoveSessionDown(inst *Instance) {
	group, exists := t.Groups[inst.GroupPath]
	if !exists {
		return
	}

	for i, s := range group.Sessions {
		if s.ID == inst.ID && i < len(group.Sessions)-1 {
			group.Sessions[i], group.Sessions[i+1] = group.Sessions[i+1], group.Sessions[i]
			break
		}
	}
}

// MoveSessionToGroup moves a session to a different group
func (t *GroupTree) MoveSessionToGroup(inst *Instance, newGroupPath string) {
	oldGroupPath := inst.GroupPath

	// Remove from old group
	if oldGroup, exists := t.Groups[oldGroupPath]; exists {
		for i, s := range oldGroup.Sessions {
			if s.ID == inst.ID {
				oldGroup.Sessions = append(oldGroup.Sessions[:i], oldGroup.Sessions[i+1:]...)
				break
			}
		}
		// NOTE: We do NOT delete empty groups here - user-created groups should persist
	}

	// Add to new group
	inst.GroupPath = newGroupPath
	newGroup, exists := t.Groups[newGroupPath]
	if !exists {
		newGroup = &Group{
			Name:     newGroupPath,
			Path:     newGroupPath,
			Expanded: true,
			Sessions: []*Instance{},
		}
		t.Groups[newGroupPath] = newGroup
		t.rebuildGroupList()
	}
	newGroup.Sessions = append(newGroup.Sessions, inst)

	// Update default paths for both old and new groups
	t.updateGroupDefaultPath(oldGroupPath)
	t.updateGroupDefaultPath(newGroupPath)
}

// sanitizeGroupName removes dangerous characters from group names
// to prevent path traversal and other security issues
func sanitizeGroupName(name string) string {
	// Remove or replace dangerous characters
	var result strings.Builder
	result.Grow(len(name))

	for _, r := range name {
		// Allow letters, digits, spaces, hyphens, and underscores
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' {
			result.WriteRune(r)
		} else if r == '/' || r == '\\' || r == '.' {
			// Replace path separators and dots with hyphens
			result.WriteRune('-')
		}
		// Other characters are dropped
	}

	// Clean up multiple consecutive hyphens
	cleaned := result.String()
	for strings.Contains(cleaned, "--") {
		cleaned = strings.ReplaceAll(cleaned, "--", "-")
	}

	// Trim leading/trailing hyphens and spaces
	cleaned = strings.Trim(cleaned, "- ")

	// If the result is empty after sanitization, use a default
	if cleaned == "" {
		return "unnamed"
	}

	return cleaned
}

// CreateGroup creates a new empty group
func (t *GroupTree) CreateGroup(name string) *Group {
	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(name)
	path := strings.ToLower(strings.ReplaceAll(sanitizedName, " ", "-"))
	if _, exists := t.Groups[path]; exists {
		return t.Groups[path]
	}

	// Count existing root-level groups to assign sibling-relative order
	rootCount := 0
	for p := range t.Groups {
		if getParentPath(p) == "" { // Root level
			rootCount++
		}
	}

	group := &Group{
		Name:     sanitizedName,
		Path:     path,
		Expanded: true,
		Sessions: []*Instance{},
		Order:    rootCount, // Order among root groups
	}
	t.Groups[path] = group
	t.Expanded[path] = true
	t.rebuildGroupList()
	return group
}

// CreateSubgroup creates a new empty group under a parent group
func (t *GroupTree) CreateSubgroup(parentPath, name string) *Group {
	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(name)
	childPath := strings.ToLower(strings.ReplaceAll(sanitizedName, " ", "-"))
	fullPath := parentPath + "/" + childPath

	if _, exists := t.Groups[fullPath]; exists {
		return t.Groups[fullPath]
	}

	// Count existing siblings to assign sibling-relative order
	siblingCount := 0
	for p := range t.Groups {
		if getParentPath(p) == parentPath {
			siblingCount++
		}
	}

	group := &Group{
		Name:     sanitizedName,
		Path:     fullPath,
		Expanded: true,
		Sessions: []*Instance{},
		Order:    siblingCount, // Order among siblings
	}
	t.Groups[fullPath] = group
	t.Expanded[fullPath] = true
	t.rebuildGroupList()
	return group
}

// RenameGroup renames a group and updates all subgroups
func (t *GroupTree) RenameGroup(oldPath, newName string) {
	group, exists := t.Groups[oldPath]
	if !exists {
		return
	}

	// Sanitize name to prevent path traversal and security issues
	sanitizedName := sanitizeGroupName(newName)
	newBasePath := strings.ToLower(strings.ReplaceAll(sanitizedName, " ", "-"))

	// Preserve parent path for subgroups
	parentPath := getParentPath(oldPath)
	var newPath string
	if parentPath != "" {
		newPath = parentPath + "/" + newBasePath
	} else {
		newPath = newBasePath
	}

	if newPath == oldPath {
		group.Name = sanitizedName
		return
	}

	// Update all sessions in the group
	for _, sess := range group.Sessions {
		sess.GroupPath = newPath
	}

	// Update all subgroups (groups whose path starts with oldPath + "/")
	subgroupsToUpdate := make(map[string]*Group)
	for path, g := range t.Groups {
		if strings.HasPrefix(path, oldPath+"/") {
			newSubPath := newPath + path[len(oldPath):] // Replace prefix
			// Update sessions in subgroup
			for _, sess := range g.Sessions {
				sess.GroupPath = newSubPath
			}
			g.Path = newSubPath
			subgroupsToUpdate[path] = g
		}
	}

	// Remove old subgroup entries and add with new paths
	for oldSubPath, g := range subgroupsToUpdate {
		delete(t.Groups, oldSubPath)
		t.Groups[g.Path] = g
		expanded := t.Expanded[oldSubPath]
		delete(t.Expanded, oldSubPath)
		t.Expanded[g.Path] = expanded
	}

	// Update the main group
	group.Name = sanitizedName
	group.Path = newPath

	// Update maps for main group
	delete(t.Groups, oldPath)
	t.Groups[newPath] = group
	delete(t.Expanded, oldPath)
	t.Expanded[newPath] = group.Expanded

	t.rebuildGroupList()
}

// DeleteGroup deletes a group, all its subgroups, and moves all sessions to default
func (t *GroupTree) DeleteGroup(path string) []*Instance {
	group, exists := t.Groups[path]
	if !exists || path == DefaultGroupPath {
		return nil
	}

	// Collect all sessions from this group and all subgroups
	allMovedSessions := []*Instance{}

	// Find and delete all subgroups first (groups whose path starts with this path + "/")
	subgroupPaths := []string{}
	for groupPath := range t.Groups {
		if strings.HasPrefix(groupPath, path+"/") {
			subgroupPaths = append(subgroupPaths, groupPath)
		}
	}

	// Collect sessions from subgroups and delete them
	for _, subPath := range subgroupPaths {
		if subGroup, exists := t.Groups[subPath]; exists {
			allMovedSessions = append(allMovedSessions, subGroup.Sessions...)
			delete(t.Groups, subPath)
			delete(t.Expanded, subPath)
		}
	}

	// Add sessions from the main group
	allMovedSessions = append(allMovedSessions, group.Sessions...)

	// Move all sessions to default group
	for _, sess := range allMovedSessions {
		sess.GroupPath = DefaultGroupPath
	}

	// Ensure default group exists
	defaultGroup, exists := t.Groups[DefaultGroupPath]
	if !exists {
		defaultGroup = &Group{
			Name:     DefaultGroupName,
			Path:     DefaultGroupPath,
			Expanded: true,
			Sessions: []*Instance{},
		}
		t.Groups[DefaultGroupPath] = defaultGroup
	}
	defaultGroup.Sessions = append(defaultGroup.Sessions, allMovedSessions...)

	// Remove the main group
	delete(t.Groups, path)
	delete(t.Expanded, path)
	t.rebuildGroupList()

	return allMovedSessions
}

// GetAllInstances returns all instances in order
func (t *GroupTree) GetAllInstances() []*Instance {
	instances := []*Instance{}
	for _, group := range t.GroupList {
		instances = append(instances, group.Sessions...)
	}
	return instances
}

// GetGroupNames returns all group names for selection
func (t *GroupTree) GetGroupNames() []string {
	names := make([]string, len(t.GroupList))
	for i, g := range t.GroupList {
		names[i] = g.Name
	}
	return names
}

// SessionCount returns total session count
func (t *GroupTree) SessionCount() int {
	count := 0
	for _, g := range t.Groups {
		count += len(g.Sessions)
	}
	return count
}

// SessionCountForGroup returns session count for a group INCLUDING all its subgroups
// This enables hierarchical counts like "Project (5)" where 5 includes all nested sessions
func (t *GroupTree) SessionCountForGroup(groupPath string) int {
	count := 0
	for path, g := range t.Groups {
		// Count this group if it matches OR is a subgroup (prefix match)
		if path == groupPath || strings.HasPrefix(path, groupPath+"/") {
			count += len(g.Sessions)
		}
	}
	return count
}

// GroupCount returns total group count
func (t *GroupTree) GroupCount() int {
	return len(t.Groups)
}

// AddSession adds a session to the appropriate group
func (t *GroupTree) AddSession(inst *Instance) {
	groupPath := inst.GroupPath
	if groupPath == "" {
		groupPath = DefaultGroupPath
		inst.GroupPath = groupPath
	}

	group, exists := t.Groups[groupPath]
	if !exists {
		// Ensure parent groups exist for hierarchical paths
		t.ensureParentGroupsExist(groupPath)
		// Use proper name for default group, otherwise extract name from path
		name := extractGroupName(groupPath)
		if groupPath == DefaultGroupPath {
			name = DefaultGroupName
		}
		group = &Group{
			Name:     name,
			Path:     groupPath,
			Expanded: true,
			Sessions: []*Instance{},
			Order:    len(t.GroupList),
		}
		t.Groups[groupPath] = group
		t.Expanded[groupPath] = true
		t.rebuildGroupList()
	}
	group.Sessions = append(group.Sessions, inst)
	t.updateGroupDefaultPath(groupPath)
}

// RemoveSession removes a session from its group
func (t *GroupTree) RemoveSession(inst *Instance) {
	groupPath := inst.GroupPath
	if groupPath == "" {
		groupPath = DefaultGroupPath
	}

	if group, exists := t.Groups[groupPath]; exists {
		for i, s := range group.Sessions {
			if s.ID == inst.ID {
				group.Sessions = append(group.Sessions[:i], group.Sessions[i+1:]...)
				break
			}
		}
		// NOTE: We do NOT delete empty groups - they persist until explicitly deleted
	}
}

// GetGroupPaths returns all group paths for selection
func (t *GroupTree) GetGroupPaths() []string {
	paths := make([]string, len(t.GroupList))
	for i, g := range t.GroupList {
		paths[i] = g.Path
	}
	return paths
}

// SyncWithInstances updates the tree with a new set of instances
// while preserving existing group structure (including empty groups)
func (t *GroupTree) SyncWithInstances(instances []*Instance) {
	// Clear all sessions from groups (but keep the groups)
	for _, group := range t.Groups {
		group.Sessions = []*Instance{}
	}

	// Re-add all instances to their groups
	for _, inst := range instances {
		groupPath := inst.GroupPath
		if groupPath == "" {
			groupPath = DefaultGroupPath
			inst.GroupPath = groupPath
		}

		group, exists := t.Groups[groupPath]
		if !exists {
			// Ensure parent groups exist for hierarchical paths
			t.ensureParentGroupsExist(groupPath)
			// Create new group for this session's path
			// Use proper name for default group, otherwise extract name from path
			name := extractGroupName(groupPath)
			if groupPath == DefaultGroupPath {
				name = DefaultGroupName
			}
			group = &Group{
				Name:     name,
				Path:     groupPath,
				Expanded: true,
				Sessions: []*Instance{},
				Order:    len(t.GroupList),
			}
			t.Groups[groupPath] = group
			t.Expanded[groupPath] = true
			t.rebuildGroupList()
		}
		group.Sessions = append(group.Sessions, inst)
	}

	// Always rebuild GroupList at the end to ensure consistency between
	// Groups map and GroupList slice. This fixes the bug where flatItems
	// could be empty while instances has data (filter bar shows counts
	// but main panel shows "No Sessions Yet").
	t.rebuildGroupList()

	// Update default paths for all groups after syncing
	for groupPath := range t.Groups {
		t.updateGroupDefaultPath(groupPath)
	}
}

// ShallowCopyForSave creates a copy of the GroupTree that's safe to use
// from a goroutine for saving purposes. It deep copies the Group structs
// to prevent data races when the main thread modifies group fields
// (Name, Path, Expanded, Order) while a background goroutine reads them.
func (t *GroupTree) ShallowCopyForSave() *GroupTree {
	if t == nil {
		return nil
	}

	// Deep copy Group structs to prevent data races
	// The save goroutine reads Name, Path, Expanded, Order fields
	// which could be modified by the main thread (e.g., renaming, collapsing)
	groupListCopy := make([]*Group, len(t.GroupList))
	for i, g := range t.GroupList {
		groupListCopy[i] = &Group{
			Name:        g.Name,
			Path:        g.Path,
			Expanded:    g.Expanded,
			Order:       g.Order,
			DefaultPath: g.DefaultPath,
			// Don't copy Sessions - not needed for save, only metadata is saved
		}
	}

	return &GroupTree{
		GroupList: groupListCopy,
		// Groups and Expanded maps not needed since only GroupList is iterated in save
	}
}

// mostRecentPathForSessions returns the project path from the most recently accessed session
// in the given list. Returns empty string if no sessions have paths.
func mostRecentPathForSessions(sessions []*Instance) string {
	if len(sessions) == 0 {
		return ""
	}

	var mostRecent *Instance
	for _, s := range sessions {
		if s.ProjectPath == "" {
			continue
		}
		if mostRecent == nil || s.LastAccessedAt.After(mostRecent.LastAccessedAt) {
			mostRecent = s
		}
	}

	if mostRecent != nil {
		return mostRecent.ProjectPath
	}
	return ""
}

// updateGroupDefaultPath updates the DefaultPath of a group based on its sessions
func (t *GroupTree) updateGroupDefaultPath(groupPath string) {
	group, exists := t.Groups[groupPath]
	if !exists {
		return
	}

	if path := mostRecentPathForSessions(group.Sessions); path != "" {
		group.DefaultPath = path
	}
}
